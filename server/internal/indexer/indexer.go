// Package indexer turns a freshly-uploaded file into AI-indexed rows.
//
// Flow:
//  1. Upload handler (api/handlePutFile) returns a `*file.File`
//  2. api spawns `go indexer.IndexFile(file)` — fire-and-forget
//  3. indexer flips `files.index_status` to 'processing'
//  4. indexer calls workerclient.Index → ProcessResponse
//  5. indexer writes bounded processor facts and review suggestions
//  6. indexer inserts chunked embeddings into `embeddings_text`
//  7. on any failure: index_status='failed', the error is logged
//
// Persistence is done in a single transaction so failures roll back cleanly.
package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/enrichmentkey"
	"github.com/PeterGuy326/mem/server/internal/face"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/indexmeta"
	"github.com/PeterGuy326/mem/server/internal/managedusage"
	"github.com/PeterGuy326/mem/server/internal/modeltext"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
	"github.com/PeterGuy326/mem/server/internal/workerpb"
)

// RelatorIface is what the indexer needs from the relator package. Kept as
// an interface to break a potential import cycle.
type RelatorIface interface {
	ComputeForFile(ctx context.Context, fileID uuid.UUID) error
}

// Service is the indexer. Construct with New. Safe for concurrent use.
type Service struct {
	pool           *pgxpool.Pool
	client         *workerclient.Client
	relator        RelatorIface
	face           *face.Service
	log            *slog.Logger
	profiles       aiProfileResolver
	requireProfile bool
	managedUsage   managedUsageCoordinator
}

// aiProfileResolver is kept narrow so indexer tests and future profile-store
// changes do not pull HTTP authorization into the asynchronous file pipeline.
type aiProfileResolver interface {
	ResolveForOwner(context.Context, uuid.UUID) (*aiprofile.Selection, error)
}

// managedUsageCoordinator is the pre-invocation commercial boundary for
// profile-managed stages. Keeping it narrow lets the async indexer exercise
// the same reservation protocol as synchronous managed search without giving
// a Worker or a client request any access to entitlement storage.
type managedUsageCoordinator interface {
	Prepare(context.Context, managedusage.Command) (*managedusage.Handle, error)
}

// providerRoute is the complete, request-local model routing decision for one
// indexing job.  AIProfile is mutually exclusive with the legacy fields:
// once a workspace has selected a profile, no mutable provider_settings row
// or Worker process default may be blended into that job.
type providerRoute struct {
	WorkspaceID             uuid.UUID
	ProfileID               string
	ProfileRevision         string
	DataEgress              string
	EmbeddingProvider       string
	VisualEmbeddingProvider string
	LLMProvider             string
	VLMProvider             string
	ASRProvider             string
	AIProfile               *workerclient.AIProfileOptions
	AllowedMIMETypes        []string
}

const (
	aiProfileContract        = "mem.ai-profile/v1"
	textEmbeddingSchemaDim   = 768
	visualEmbeddingSchemaDim = 512
)

// New constructs an indexer Service. If client is nil or disabled, IndexFile
// becomes a no-op that just leaves index_status='pending'. The relator and
// face services are optional — leave nil to skip those steps.
func New(pool *pgxpool.Pool, client *workerclient.Client, relator RelatorIface, faceSvc *face.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, client: client, relator: relator, face: faceSvc, log: log}
}

// SetAIProfiles makes an explicitly selected workspace profile authoritative
// for asynchronous indexing. In SaaS, requireProfile blocks the old global
// default path so an unselected workspace cannot consume a platform key.
func (s *Service) SetAIProfiles(resolver aiProfileResolver, requireProfile bool) {
	if s == nil {
		return
	}
	s.profiles = resolver
	s.requireProfile = requireProfile
}

// SetManagedUsage installs the server-owned reservation coordinator used for
// managed profile stages. A managed route without this coordinator fails
// closed before it can dispatch source data to the Worker.
func (s *Service) SetManagedUsage(usage managedUsageCoordinator) {
	if s == nil {
		return
	}
	s.managedUsage = usage
}

// IndexFileByID resolves the file row by id and dispatches to IndexFile.
//
// Returns an error (rather than swallowing) because the queue consumer needs
// to distinguish retriable failures from poison pills.
func (s *Service) IndexFileByID(ctx context.Context, fileID, userID uuid.UUID) error {
	f, err := s.loadFile(ctx, fileID, userID)
	if err != nil {
		return fmt.Errorf("load file %s: %w", fileID, err)
	}
	s.IndexFile(ctx, f)
	// IndexFile swallows errors and reflects them in index_status. For the
	// queue we want a structured signal: re-fetch status and return error
	// when 'failed'.
	status, err := s.fetchStatus(ctx, fileID)
	if err != nil {
		return fmt.Errorf("fetch status: %w", err)
	}
	if status == "failed" {
		return fmt.Errorf("indexer: index_status=failed for %s", fileID)
	}
	return nil
}

func (s *Service) loadFile(ctx context.Context, fileID, userID uuid.UUID) (*file.File, error) {
	var f file.File
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, name, size, sha256, mime, storage_key, index_status
		   FROM files WHERE id = $1 AND user_id = $2`,
		fileID, userID,
	).Scan(
		&f.ID, &f.UserID, &f.Name, &f.Size, &f.SHA256, &f.MIME,
		&f.StorageKey, &f.IndexStatus,
	)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *Service) fetchStatus(ctx context.Context, fileID uuid.UUID) (string, error) {
	var status string
	err := s.pool.QueryRow(ctx,
		`SELECT index_status FROM files WHERE id = $1`, fileID,
	).Scan(&status)
	return status, err
}

// IndexFile runs the AI pipeline for one file end-to-end.
//
// Designed to be called in a goroutine; the supplied context should be a
// background context with a generous timeout, NOT the request context (which
// gets cancelled when the HTTP client disconnects).
func (s *Service) IndexFile(ctx context.Context, f *file.File) {
	if s == nil || s.client == nil || !s.client.Enabled() {
		s.log.Info("indexer.skipped",
			"file_id", f.ID, "reason", "worker_disabled")
		return
	}
	isLatest, unlockFile := indexmeta.LockFileIndexing(f.ID)
	defer unlockFile()
	if !isLatest {
		s.log.Info("indexer.skipped",
			"file_id", f.ID, "reason", "superseded_index_trigger")
		return
	}
	unlockIndexing := indexmeta.LockIndexing(f.UserID)
	defer unlockIndexing()

	if err := s.setStatus(ctx, f.ID, "processing"); err != nil {
		s.log.Error("indexer.set_processing", "file_id", f.ID, "err", err)
		return
	}

	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	providers, err := s.providerOverrides(ctx, f.UserID)
	if err != nil {
		s.log.Error("indexer.provider_lookup_failed", "file_id", f.ID, "err", err)
		_ = s.setStatus(ctx, f.ID, "failed")
		return
	}
	if !providers.allowsMIME(f.MIME) {
		// A selected profile is an allowlist for data egress as well as a
		// model choice. Do not send an unsupported source blob to the Worker
		// merely to learn that it has no processor for it.
		s.log.Info("indexer.skipped", "file_id", f.ID, "reason", "profile_mime_not_allowed")
		_ = s.setStatus(ctx, f.ID, "done")
		return
	}
	usageHandle, err := s.prepareManagedUsage(rpcCtx, f, providers)
	if err != nil {
		// Prepare may return a handle only when it could not prove that an
		// earlier no-invocation cleanup completed. Retry that safe release,
		// but never dispatch the Worker after a failed reservation decision.
		if usageHandle != nil {
			if releaseErr := releaseManagedUsage(ctx, usageHandle); releaseErr != nil {
				s.log.Error("indexer.managed_usage_release_failed", "file_id", f.ID, "err", releaseErr)
			}
		}
		s.log.Warn("indexer.managed_usage_unavailable", "file_id", f.ID, "err", err)
		_ = s.setStatus(ctx, f.ID, "partial")
		return
	}
	if usageHandle != nil && usageHandle.HasReplay() {
		// A replayed reservation proves that an identical managed call already
		// succeeded. File indexing has no safe, persisted Worker replay body,
		// so invoking again would create an unaccounted duplicate provider
		// call. Release any newly-created sibling reservations before returning:
		// no Worker invocation began for them, and retaining them would consume
		// quota until reconciliation despite no provider work.
		if releaseErr := releaseManagedReplaySiblings(ctx, usageHandle); releaseErr != nil {
			s.log.Error("indexer.managed_usage_replay_release_failed", "file_id", f.ID, "err", releaseErr)
		}
		// Leave a visible partial state for the explicit rebuild path.
		s.log.Warn("indexer.managed_usage_replay", "file_id", f.ID)
		_ = s.setStatus(ctx, f.ID, "partial")
		return
	}
	// For legacy requests, always pin a CLIP image-tower embedder so the visual
	// vector lands in the same 512-d latent space the search visual route
	// queries. A profile carries this same explicit stage in its contract.
	visualProv := providers.VisualEmbeddingProvider
	if providers.AIProfile == nil && strings.HasPrefix(f.MIME, "image/") {
		visualProv = "clip:ViT-B-32"
	}
	resp, err := s.client.Index(rpcCtx, workerclient.FileMeta{
		FileID:                  f.ID.String(),
		UserID:                  f.UserID.String(),
		Name:                    f.Name,
		MIME:                    f.MIME,
		SHA256:                  f.SHA256,
		StorageKey:              f.StorageKey,
		EmbeddingProvider:       providers.EmbeddingProvider,
		VisualEmbeddingProvider: visualProv,
		VLMProvider:             providers.VLMProvider,
		LLMProvider:             providers.LLMProvider,
		ASRProvider:             providers.ASRProvider,
		AIProfile:               providers.AIProfile,
	})
	if err != nil {
		if usageHandle != nil {
			if markErr := markManagedUsageIndeterminate(ctx, usageHandle); markErr != nil {
				s.log.Error("indexer.managed_usage_mark_failed", "file_id", f.ID, "err", markErr)
			}
		}
		s.log.Error("indexer.worker_failed", "file_id", f.ID, "err", err)
		_ = s.setStatus(ctx, f.ID, "failed")
		return
	}
	if resp.Status == workerpb.ProcessStatus_STATUS_FAILED {
		if usageHandle != nil {
			if markErr := markManagedUsageIndeterminate(ctx, usageHandle); markErr != nil {
				s.log.Error("indexer.managed_usage_mark_failed", "file_id", f.ID, "err", markErr)
			}
		}
		s.log.Error("indexer.process_failed", "file_id", f.ID, "err", resp.Error)
		_ = s.setStatus(ctx, f.ID, "failed")
		return
	}
	if resp.Status == workerpb.ProcessStatus_STATUS_SKIPPED {
		if usageHandle != nil {
			if releaseErr := releaseManagedUsage(ctx, usageHandle); releaseErr != nil {
				s.log.Error("indexer.managed_usage_release_failed", "file_id", f.ID, "err", releaseErr)
				_ = s.setStatus(ctx, f.ID, "partial")
				return
			}
		}
		s.log.Info("indexer.skipped", "file_id", f.ID, "reason", "unsupported_mime")
		_ = s.setStatus(ctx, f.ID, "done")
		return
	}
	if resp.Status == workerpb.ProcessStatus_STATUS_PARTIAL {
		// A partial response can mean a managed adapter was attempted but did
		// not return a usable result. Retain the reservation until an operator
		// reconciles it rather than treating an upstream timeout as a free call.
		if usageHandle != nil {
			if markErr := markManagedUsageIndeterminate(ctx, usageHandle); markErr != nil {
				s.log.Error("indexer.managed_usage_mark_failed", "file_id", f.ID, "err", markErr)
				_ = s.setStatus(ctx, f.ID, "failed")
				return
			}
		}
	} else if usageHandle != nil {
		if finalizeErr := finalizeManagedUsage(ctx, usageHandle); finalizeErr != nil {
			// Do not persist a successful-looking model result unless its usage
			// record is committed. Retain the reservation as indeterminate if
			// possible; an identical retry must not call the provider again.
			if markErr := markManagedUsageIndeterminate(ctx, usageHandle); markErr != nil {
				s.log.Error("indexer.managed_usage_mark_failed", "file_id", f.ID, "err", markErr)
			}
			s.log.Error("indexer.managed_usage_finalize_failed", "file_id", f.ID, "err", finalizeErr)
			_ = s.setStatus(ctx, f.ID, "partial")
			return
		}
	}
	if text := resp.Embeddings["text"]; text != nil && len(text.Rows) > 0 {
		// Re-resolve after the long worker RPC. The in-process coordinator
		// prevents local switches; this second check also fails safely if a
		// different memd instance changed the setting while work was in flight.
		latestProviders, resolveErr := s.providerOverrides(ctx, f.UserID)
		if resolveErr != nil {
			s.log.Error("indexer.embedding_provider_recheck_failed",
				"file_id", f.ID, "err", resolveErr)
			_ = s.setStatus(ctx, f.ID, "failed")
			return
		}
		expected := providers.EmbeddingProvider
		if latestProviders.EmbeddingProvider != "" {
			expected = latestProviders.EmbeddingProvider
		}
		if expected != "" && text.Provider != expected {
			s.log.Error("indexer.embedding_provider_mismatch",
				"file_id", f.ID, "expected", expected, "actual", text.Provider)
			_ = s.setStatus(ctx, f.ID, "failed")
			return
		}
	}

	if err := s.persist(ctx, f.ID, resp); err != nil {
		s.log.Error("indexer.persist_failed", "file_id", f.ID, "err", err)
		_ = s.setStatus(ctx, f.ID, "failed")
		return
	}
	s.log.Info("indexer.done",
		"file_id", f.ID,
		"processor", resp.Processor,
		"embeddings", embeddingKinds(resp),
		"tags", len(resp.Tags),
	)

	// Compute relations after embeddings are committed. Failures here are
	// soft — file_relations is a derived index that can be rebuilt later.
	if s.relator != nil {
		if err := s.relator.ComputeForFile(ctx, f.ID); err != nil {
			s.log.Warn("indexer.relate_failed", "file_id", f.ID, "err", err)
		}
	}
}

// persist writes the worker output into PG in one transaction. Model-produced
// descriptions/tags become pending annotations; only explicit user decisions
// may change the backwards-compatible files.summary/files.tags projections.
func (s *Service) persist(ctx context.Context, fileID uuid.UUID, resp *workerpb.ProcessResponse) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck — committed below on success

	enrichment := parseWorkerEnrichment(resp)
	status := "done"
	if resp.Status == workerpb.ProcessStatus_STATUS_PARTIAL || enrichment.Partial {
		status = "partial"
	}

	_, err = tx.Exec(ctx,
		`UPDATE files
		   SET caption = CASE WHEN $1::boolean THEN $2::text ELSE caption END,
		       processor_metadata = $3::jsonb,
		       timeline_at = COALESCE(timeline_at, $4::timestamptz),
		       geo = COALESCE(geo, $5::point),
		       index_status = $6,
		       updated_at = now()
		 WHERE id = $7`,
		enrichment.CaptionSet && !enrichment.CaptionFromReview,
		enrichment.Caption,
		enrichment.ProcessorMetadata,
		enrichment.Timeline,
		enrichment.Geo,
		status,
		fileID,
	)
	if err != nil {
		return fmt.Errorf("update files: %w", err)
	}
	if err := persistAnnotationSuggestions(
		ctx,
		tx,
		fileID,
		enrichment.Annotations,
		enrichment.ReconcileAnnotations,
	); err != nil {
		return err
	}
	if enrichment.CaptionSet && enrichment.CaptionFromReview {
		if err := file.RefreshReviewableDescriptionProjections(ctx, tx, fileID); err != nil {
			return err
		}
	}

	text := resp.Embeddings["text"]
	if text != nil && len(text.Rows) > 0 && strings.TrimSpace(text.Provider) == "" {
		return fmt.Errorf("text embedding provider identity is empty")
	}

	hasReplacementText := text != nil && len(text.Rows) > 0
	// A partial retry that did not produce text embeddings must not erase the
	// last usable index. Successful empty extraction still clears stale rows,
	// while an actual replacement remains idempotent.
	if status != "partial" || hasReplacementText {
		if _, err := tx.Exec(ctx, `DELETE FROM embeddings_text WHERE file_id = $1`, fileID); err != nil {
			return fmt.Errorf("delete old text embeddings: %w", err)
		}
	}

	if hasReplacementText {
		batch := &pgx.Batch{}
		for _, row := range text.Rows {
			batch.Queue(
				`INSERT INTO embeddings_text (file_id, chunk_index, chunk_text, embedding, provider)
				 VALUES ($1, $2, $3, $4::vector, $5)`,
				fileID, row.Index, row.ChunkText, vectorLiteral(row.Values), text.Provider,
			)
		}
		br := tx.SendBatch(ctx, batch)
		for range text.Rows {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return fmt.Errorf("insert text embeddings: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return fmt.Errorf("close batch: %w", err)
		}
	}

	if visual := resp.Embeddings["visual"]; visual != nil && len(visual.Rows) > 0 {
		// Only first row is meaningful for whole-image embeddings.
		row := visual.Rows[0]
		if _, err := tx.Exec(ctx,
			`INSERT INTO embeddings_visual (file_id, embedding)
			 VALUES ($1, $2::vector)
			 ON CONFLICT (file_id) DO UPDATE SET embedding = EXCLUDED.embedding`,
			fileID, vectorLiteral(row.Values),
		); err != nil {
			return fmt.Errorf("upsert visual embedding: %w", err)
		}
	}

	// Face embeddings (SPEC §F6.1).
	if faces := resp.Embeddings["face"]; faces != nil && len(faces.Rows) > 0 && s.face != nil {
		userID, err := s.fileUserID(ctx, tx, fileID)
		if err != nil {
			return fmt.Errorf("file owner for faces: %w", err)
		}
		rows := make([]face.FaceRow, 0, len(faces.Rows))
		for _, r := range faces.Rows {
			fr := face.FaceRow{Embedding: r.Values}
			if len(r.MetadataJson) > 0 {
				var meta struct {
					BBox []float64 `json:"bbox"`
				}
				_ = json.Unmarshal(r.MetadataJson, &meta)
				fr.BBox = meta.BBox
			}
			rows = append(rows, fr)
		}
		if err := s.face.Persist(ctx, tx, fileID, userID, rows); err != nil {
			return fmt.Errorf("persist faces: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (s *Service) fileUserID(ctx context.Context, tx pgx.Tx, fileID uuid.UUID) (uuid.UUID, error) {
	var uid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT user_id FROM files WHERE id = $1`, fileID).Scan(&uid)
	return uid, err
}

// providerOverrides resolves one model route for the job. A selected profile
// wins over all legacy settings and Worker defaults. The legacy branch remains
// for private deployments that have deliberately not adopted profiles yet.
func (s *Service) providerOverrides(ctx context.Context, userID uuid.UUID) (providerRoute, error) {
	if s.profiles != nil {
		selection, err := s.profiles.ResolveForOwner(ctx, userID)
		if err == nil {
			route, routeErr := routeFromAIProfile(selection)
			if routeErr != nil {
				return providerRoute{}, routeErr
			}
			if err := s.requireProfileCorpusCompatibility(ctx, userID, route.EmbeddingProvider); err != nil {
				return providerRoute{}, err
			}
			return route, nil
		}
		if !errors.Is(err, aiprofile.ErrNotFound) {
			return providerRoute{}, fmt.Errorf("resolve workspace AI profile: %w", err)
		}
	}
	if s.requireProfile {
		return providerRoute{}, fmt.Errorf("workspace AI profile is required before indexing")
	}

	return s.legacyProviderOverrides(ctx, userID)
}

// legacyProviderOverrides reads the user's saved provider_settings rows so
// the worker can be told which model to use for THIS user — important after a
// `mem provider set embedding ...` switches dims.
//
// Done as a separate package-internal query (not via provider.Service) to
// avoid an import cycle. When no setting exists, an existing corpus's recorded
// provider is sent explicitly so worker environment changes cannot drift it.
func (s *Service) legacyProviderOverrides(ctx context.Context, userID uuid.UUID) (providerRoute, error) {
	var route providerRoute
	rows, err := s.pool.Query(ctx,
		`SELECT kind, spec FROM provider_settings WHERE user_id = $1`, userID)
	if err != nil {
		return providerRoute{}, fmt.Errorf("query provider settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, spec string
		if err := rows.Scan(&kind, &spec); err != nil {
			return providerRoute{}, fmt.Errorf("scan provider setting: %w", err)
		}
		switch kind {
		case "embedding":
			route.EmbeddingProvider = spec
		case "vlm":
			route.VLMProvider = spec
		case "llm":
			route.LLMProvider = spec
		case "asr":
			route.ASRProvider = spec
		}
	}
	if err := rows.Err(); err != nil {
		return providerRoute{}, err
	}
	corpusProvider, hasCorpus, err := indexmeta.TextProvider(ctx, s.pool, userID)
	if err != nil {
		if route.EmbeddingProvider != "" && (errors.Is(err, indexmeta.ErrUnknownProvider) ||
			errors.Is(err, indexmeta.ErrMixedProviders)) {
			// Explicit recovery path: an owner has selected a provider and is
			// rebuilding legacy rows whose historical model was not recorded.
			return route, nil
		}
		return providerRoute{}, err
	}
	if hasCorpus {
		if route.EmbeddingProvider != "" && route.EmbeddingProvider != corpusProvider {
			return providerRoute{}, fmt.Errorf(
				"configured embedding provider %q differs from corpus provider %q",
				route.EmbeddingProvider, corpusProvider,
			)
		}
		route.EmbeddingProvider = corpusProvider
	}
	return route, nil
}

// routeFromAIProfile converts a persisted, server-selected snapshot to the
// strict Worker contract. It validates the storage dimensions again at the
// boundary so a corrupted historical row cannot route a file into a different
// vector space.
func routeFromAIProfile(selection *aiprofile.Selection) (providerRoute, error) {
	if err := aiprofile.ValidateSelection(selection); err != nil {
		return providerRoute{}, fmt.Errorf("invalid workspace AI profile selection: %w", err)
	}
	if !selection.Embedding.Enabled || selection.Embedding.Provider == "" ||
		selection.Embedding.Dimensions != textEmbeddingSchemaDim {
		return providerRoute{}, fmt.Errorf("workspace AI profile has an invalid text embedding stage")
	}
	if !selection.VisualEmbedding.Enabled || selection.VisualEmbedding.Provider == "" ||
		selection.VisualEmbedding.Dimensions != visualEmbeddingSchemaDim {
		return providerRoute{}, fmt.Errorf("workspace AI profile has an invalid visual embedding stage")
	}
	profile := &workerclient.AIProfileOptions{
		Contract:         aiProfileContract,
		ID:               selection.ProfileID,
		Revision:         selection.ProfileRevision,
		PipelineRevision: selection.PipelineRevision,
		Embedding:        routeStage(selection.Embedding),
		VisualEmbedding:  routeStage(selection.VisualEmbedding),
		LLM:              routeStage(selection.LLM),
		VLM:              routeStage(selection.VLM),
		ASR:              routeStage(selection.ASR),
		Rerank:           routeStage(selection.Rerank),
	}
	return providerRoute{
		WorkspaceID:             selection.WorkspaceID,
		ProfileID:               selection.ProfileID,
		ProfileRevision:         selection.ProfileRevision,
		DataEgress:              selection.DataEgress,
		EmbeddingProvider:       selection.Embedding.Provider,
		VisualEmbeddingProvider: selection.VisualEmbedding.Provider,
		LLMProvider:             selection.LLM.Provider,
		VLMProvider:             selection.VLM.Provider,
		ASRProvider:             selection.ASR.Provider,
		AIProfile:               profile,
		AllowedMIMETypes:        append([]string(nil), selection.AllowedMIMETypes...),
	}, nil
}

func routeStage(stage aiprofile.Stage) workerclient.ProviderStage {
	if !stage.Enabled {
		return workerclient.ProviderStage{Enabled: false}
	}
	return workerclient.ProviderStage{
		Enabled: stage.Enabled, Provider: stage.Provider, Dimensions: stage.Dimensions,
	}
}

// requireProfileCorpusCompatibility enforces that a profile snapshot never
// silently mixes its declared embedding provider with rows from a different
// vector space. Profile selection performs the same guard before persistence;
// this second check covers direct database corruption and multi-process races.
func (s *Service) requireProfileCorpusCompatibility(
	ctx context.Context,
	userID uuid.UUID,
	provider string,
) error {
	corpusProvider, hasCorpus, err := indexmeta.TextProvider(ctx, s.pool, userID)
	if err != nil {
		return fmt.Errorf("workspace AI profile corpus identity: %w", err)
	}
	if hasCorpus && corpusProvider != provider {
		return fmt.Errorf("workspace AI profile embedding provider %q differs from corpus provider %q", provider, corpusProvider)
	}
	return nil
}

// allowsMIME is intentionally meaningful only for profile routes. Legacy
// behavior delegates MIME support to the Worker registry for compatibility.
func (route providerRoute) allowsMIME(raw string) bool {
	if route.AIProfile == nil {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil || mediaType == "" {
		return false
	}
	for _, allowed := range route.AllowedMIMETypes {
		if allowed == mediaType {
			return true
		}
		if strings.HasSuffix(allowed, "/*") {
			prefix := strings.TrimSuffix(allowed, "*")
			if strings.HasPrefix(mediaType, prefix) {
				return true
			}
		}
	}
	return false
}

// prepareManagedUsage reserves every managed stage that this exact MIME route
// can invoke before the source is dispatched to the Worker. The compiled
// profile is the only source of provider identities; neither file metadata
// nor a client request can add a billable stage.
func (s *Service) prepareManagedUsage(
	ctx context.Context,
	f *file.File,
	route providerRoute,
) (*managedusage.Handle, error) {
	if route.AIProfile == nil || route.DataEgress != aiprofile.DataEgressManagedIdealab {
		return nil, nil
	}
	if s == nil || s.managedUsage == nil {
		return nil, managedusage.ErrEntitlementUnavailable
	}
	stages := managedStagesForMIME(route, f.MIME)
	if len(stages) == 0 {
		return nil, nil
	}
	return s.managedUsage.Prepare(ctx, managedusage.Command{
		WorkspaceID:     route.WorkspaceID,
		FileID:          f.ID,
		ContentSHA256:   f.SHA256,
		ProfileID:       route.ProfileID,
		ProfileRevision: route.ProfileRevision,
		Stages:          stages,
	})
}

// managedStagesForMIME mirrors the Worker profile dispatcher. It deliberately
// lists only stages that the selected processor can reach; rerank is not
// included because mem has no verified Idealab rerank invocation path.
func managedStagesForMIME(route providerRoute, rawMIME string) []managedusage.StageSpec {
	if route.AIProfile == nil || route.DataEgress != aiprofile.DataEgressManagedIdealab {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(rawMIME)
	if err != nil {
		return nil
	}
	profile := route.AIProfile
	stages := make([]managedusage.StageSpec, 0, 3)
	appendStage := func(stage managedusage.Stage, provider workerclient.ProviderStage) {
		if provider.Enabled && provider.Provider != "" {
			stages = append(stages, managedusage.StageSpec{
				Stage: stage, ProviderSpec: provider.Provider,
			})
		}
	}
	appendTextStages := func() {
		appendStage(managedusage.StageEmbedding, profile.Embedding)
		appendStage(managedusage.StageLLM, profile.LLM)
	}

	switch {
	case strings.HasPrefix(mediaType, "text/"), isExtraTextMIME(mediaType), mediaType == "application/pdf":
		appendTextStages()
	case strings.HasPrefix(mediaType, "image/"):
		// CLIP visual embedding is included so the coordinator can prove it
		// is a fixed local stage and therefore must not consume managed quota.
		appendStage(managedusage.StageVisualEmbedding, profile.VisualEmbedding)
		appendStage(managedusage.StageVLM, profile.VLM)
	case strings.HasPrefix(mediaType, "audio/"):
		// AudioProcessor exits before text enrichment when ASR is disabled.
		// If a future reviewed profile enables ASR, reserve it before the
		// transcription and the downstream text stages it can unlock.
		if profile.ASR.Enabled {
			appendStage(managedusage.StageASR, profile.ASR)
			appendTextStages()
		}
	}
	if len(stages) == 0 {
		return nil
	}
	return stages
}

func isExtraTextMIME(mediaType string) bool {
	switch mediaType {
	case "application/json", "application/xml", "application/yaml",
		"application/x-yaml", "application/javascript", "application/typescript",
		"application/x-sh", "application/x-python", "application/x-toml":
		return true
	default:
		return false
	}
}

const managedUsageSettlementTimeout = 5 * time.Second

func finalizeManagedUsage(ctx context.Context, handle *managedusage.Handle) error {
	if handle == nil {
		return nil
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managedUsageSettlementTimeout)
	defer cancel()
	return handle.Finalize(settleCtx)
}

func releaseManagedUsage(ctx context.Context, handle *managedusage.Handle) error {
	if handle == nil {
		return nil
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managedUsageSettlementTimeout)
	defer cancel()
	return handle.ReleaseUninvoked(settleCtx)
}

// releaseManagedReplaySiblings releases only reservations that were newly
// created alongside an already-succeeded replay. It must run before returning
// from a replay path because no Worker invocation is allowed after HasReplay.
// Handle.ReleaseUninvoked deliberately leaves replayed entries untouched.
func releaseManagedReplaySiblings(ctx context.Context, handle *managedusage.Handle) error {
	if handle == nil || !handle.HasReplay() {
		return nil
	}
	return releaseManagedUsage(ctx, handle)
}

func markManagedUsageIndeterminate(ctx context.Context, handle *managedusage.Handle) error {
	if handle == nil {
		return nil
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managedUsageSettlementTimeout)
	defer cancel()
	return handle.MarkIndeterminate(settleCtx)
}

func (s *Service) setStatus(ctx context.Context, fileID uuid.UUID, status string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE files SET index_status = $1, updated_at = now() WHERE id = $2`,
		status, fileID,
	)
	return err
}

// --- helpers ---

// vectorLiteral encodes []float32 into pgvector's text input format: "[v1,v2,...]".
func vectorLiteral(vs []float32) string {
	if len(vs) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.Grow(len(vs) * 12)
	b.WriteByte('[')
	for i, v := range vs {
		if i > 0 {
			b.WriteByte(',')
		}
		// %g keeps it short; pgvector accepts plain decimal.
		fmt.Fprintf(&b, "%g", v)
	}
	b.WriteByte(']')
	return b.String()
}

const maxWorkerMetadataBytes = 256 << 10

var safeProcessorMetadataKeys = map[string]struct{}{
	"format":                 {},
	"mode":                   {},
	"width":                  {},
	"height":                 {},
	"camera_make":            {},
	"camera_model":           {},
	"orientation":            {},
	"timeline_at":            {},
	"taken_at":               {},
	"decode_empty":           {},
	"byte_length":            {},
	"char_length":            {},
	"chunk_count":            {},
	"chunk_size":             {},
	"chunk_overlap":          {},
	"page_count":             {},
	"text_empty":             {},
	"note":                   {},
	"extracted_char_length":  {},
	"source_mime":            {},
	"language":               {},
	"duration_sec":           {},
	"transcript_char_length": {},
	"asr_provider":           {},
	"transcript_empty":       {},
	"annotations_complete":   {},
	"ai_profile":             {},
}

var degradedMetadataKeys = map[string]string{
	"annotation_parse_error": "annotation_parse",
	"asr_error":              "speech_recognition",
	"decode_error":           "decode",
	"embed_error":            "embedding",
	"error":                  "processor",
	"face_error":             "face",
	"parse_error":            "parse",
	"summary_error":          "annotation_model",
	"vlm_error":              "vision_model",
}

type annotationSuggestion struct {
	StableKey       string
	Kind            string
	Value           string
	Confidence      float64
	Source          string
	Provider        string
	Processor       string
	AnalysisVersion string
}

type workerEnrichment struct {
	ProcessorMetadata    []byte
	Timeline             any
	Geo                  any
	Annotations          []annotationSuggestion
	AnnotationsComplete  bool
	ReconcileAnnotations bool
	Caption              *string
	CaptionSet           bool
	CaptionFromReview    bool
	Partial              bool
}

// parseWorkerEnrichment treats Worker metadata as untrusted. Only bounded,
// explicitly allow-listed processor facts survive; model suggestions are
// independently validated before they can reach the review table.
func parseWorkerEnrichment(resp *workerpb.ProcessResponse) workerEnrichment {
	result := workerEnrichment{ProcessorMetadata: []byte(`{}`)}
	if resp == nil {
		result.Partial = true
		return result
	}
	result.Partial = resp.Status != workerpb.ProcessStatus_STATUS_OK

	root := make(map[string]any)
	metadataValid := len(resp.MetadataJson) <= maxWorkerMetadataBytes
	if metadataValid && len(resp.MetadataJson) > 0 {
		metadataValid = json.Unmarshal(resp.MetadataJson, &root) == nil && root != nil
	}
	if !metadataValid {
		root = map[string]any{}
		result.Partial = true
	}

	sanitized := make(map[string]any)
	invalidFields := make([]string, 0)
	if resp.Processor != "" {
		if validMachineIdentifier(resp.Processor, 64) {
			sanitized["processor"] = resp.Processor
		} else {
			invalidFields = append(invalidFields, "processor")
			result.Partial = true
		}
	}
	for key := range safeProcessorMetadataKeys {
		if value, exists := root[key]; exists {
			if bounded, ok := sanitizedProcessorFact(key, value); ok {
				sanitized[key] = bounded
			} else {
				invalidFields = append(invalidFields, key)
				result.Partial = true
			}
		}
	}
	if gps, ok := sanitizedGPS(root["gps"]); ok {
		sanitized["gps"] = gps
		result.Geo = pgtype.Point{
			P:     pgtype.Vec2{X: gps["lng"].(float64), Y: gps["lat"].(float64)},
			Valid: true,
		}
	} else if _, exists := root["gps"]; exists {
		invalidFields = append(invalidFields, "gps")
		result.Partial = true
	}
	if len(invalidFields) > 0 {
		sort.Strings(invalidFields)
		sanitized["invalid_metadata_fields"] = invalidFields
	}

	degraded := make([]string, 0)
	for rawKey, step := range degradedMetadataKeys {
		if _, exists := root[rawKey]; exists {
			degraded = append(degraded, step)
		}
	}
	if len(degraded) > 0 {
		sort.Strings(degraded)
		sanitized["degraded_steps"] = degraded
		result.Partial = true
	}

	result.Timeline = extractTimelineFromMap(sanitized)
	if result.Timeline == nil && hasNaiveTimeline(sanitized) {
		sanitized["timeline_timezone_unknown"] = true
	}

	rawAnnotations, annotationsPresent := root["annotations"]
	rawAnnotationsComplete, annotationsCompletePresent := root["annotations_complete"]
	annotationsComplete, annotationsCompleteValid := rawAnnotationsComplete.(bool)
	if annotationsPresent {
		annotations, ok := parseAnnotationSuggestions(rawAnnotations, resp.Processor)
		if ok {
			result.Annotations = annotations
			result.AnnotationsComplete = annotationsCompleteValid && annotationsComplete
			result.ReconcileAnnotations = (!annotationsCompletePresent &&
				len(annotations) > 0) || result.AnnotationsComplete
			result.CaptionSet = result.ReconcileAnnotations
			result.CaptionFromReview = result.ReconcileAnnotations
			if annotationsCompletePresent &&
				annotationsCompleteValid &&
				!annotationsComplete &&
				len(annotations) > 0 {
				sanitized["annotation_completion_inconsistent"] = true
				result.Partial = true
			}
			for index := range annotations {
				if annotations[index].Kind == file.AnnotationKindDescription {
					if caption, ok := modeltext.NormalizePlain(
						annotations[index].Value,
						2000,
					); ok {
						result.Caption = &caption
						result.CaptionSet = true
						result.CaptionFromReview = true
					}
					break
				}
			}
			workerCaption, captionOK := boundedWorkerCaption(resp.Caption)
			if !captionOK {
				sanitized["caption_payload_invalid"] = true
				result.Partial = true
			} else if workerCaption != "" &&
				(result.Caption == nil || workerCaption != *result.Caption) {
				sanitized["caption_annotation_mismatch"] = true
				result.Partial = true
			}
		} else {
			sanitized["annotation_payload_invalid"] = true
			if resp.Caption != "" {
				sanitized["caption_payload_invalid"] = true
			}
			result.Partial = true
		}
	} else if annotationsCompletePresent {
		if annotationsCompleteValid && annotationsComplete {
			sanitized["annotation_payload_invalid"] = true
			result.Partial = true
		}
		if resp.Caption != "" || resp.Summary != "" || len(resp.Tags) > 0 {
			sanitized["legacy_fields_suppressed"] = true
			if annotationsCompleteValid && !annotationsComplete {
				sanitized["annotation_completion_inconsistent"] = true
			}
			result.Partial = true
		}
	} else if metadataValid {
		if _, ok := boundedWorkerCaption(resp.Caption); !ok {
			sanitized["caption_payload_invalid"] = true
			result.Partial = true
		}
		var legacyValid bool
		result.Annotations, legacyValid = legacyAnnotationSuggestions(resp)
		if !legacyValid {
			sanitized["legacy_annotation_payload_invalid"] = true
			result.Partial = true
		}
		for index := range result.Annotations {
			if result.Annotations[index].Kind == file.AnnotationKindDescription {
				caption := result.Annotations[index].Value
				result.Caption = &caption
				result.CaptionSet = true
				result.CaptionFromReview = true
				break
			}
		}
	} else if resp.Caption != "" || resp.Summary != "" || len(resp.Tags) > 0 {
		sanitized["legacy_fields_suppressed"] = true
	}

	if result.Partial {
		sanitized["status"] = "partial"
	} else if resp.Status == workerpb.ProcessStatus_STATUS_OK {
		sanitized["status"] = "ok"
	}
	if encoded, err := json.Marshal(sanitized); err == nil {
		result.ProcessorMetadata = encoded
	}
	return result
}

func boundedWorkerCaption(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	return modeltext.NormalizePlain(value, 2000)
}

func sanitizedProcessorFact(key string, value any) (any, bool) {
	switch key {
	case "format", "mode":
		text, ok := value.(string)
		return text, ok && validMachineIdentifier(text, 64)
	case "camera_make", "camera_model":
		text, ok := value.(string)
		return text, ok && validBoundedText(text, 255, false)
	case "orientation":
		return boundedJSONInteger(value, 1, 8)
	case "timeline_at", "taken_at":
		text, ok := value.(string)
		if !ok || !validProcessorTimeline(text) {
			return nil, false
		}
		return text, true
	case "width", "height":
		return boundedJSONInteger(value, 1, 1_000_000)
	case "byte_length", "char_length", "extracted_char_length",
		"transcript_char_length":
		return boundedJSONInteger(value, 0, 1<<53-1)
	case "chunk_count", "page_count":
		return boundedJSONInteger(value, 0, 1_000_000_000)
	case "chunk_size":
		return boundedJSONInteger(value, 1, 10_000_000)
	case "chunk_overlap":
		return boundedJSONInteger(value, 0, 10_000_000)
	case "duration_sec":
		number, ok := finiteJSONNumber(value)
		return number, ok && number >= 0 && number <= 3_200_000_000
	case "annotations_complete", "decode_empty", "text_empty", "transcript_empty":
		flag, ok := value.(bool)
		return flag, ok
	case "note":
		text, ok := value.(string)
		return text, ok &&
			text == "no extractable text layer (OCR fallback not yet implemented)"
	case "source_mime":
		text, ok := value.(string)
		if !ok || !validBoundedText(text, 255, false) {
			return nil, false
		}
		mediaType, _, err := mime.ParseMediaType(text)
		return mediaType, err == nil && mediaType != ""
	case "language":
		text, ok := value.(string)
		return text, ok && validMachineIdentifier(text, 64)
	case "asr_provider":
		text, ok := value.(string)
		return text, ok && validMachineIdentifier(text, 255)
	case "ai_profile":
		return sanitizedAIProfileProvenance(value)
	default:
		return nil, false
	}
}

// sanitizedAIProfileProvenance accepts only the small Worker projection of a
// server-resolved profile. It deliberately does not preserve provider URLs,
// credentials, prompts, raw upstream replies, or arbitrary metadata supplied
// by a compromised worker.
func sanitizedAIProfileProvenance(value any) (map[string]string, bool) {
	object, ok := value.(map[string]any)
	if !ok || len(object) != 4 {
		return nil, false
	}
	contract, contractOK := object["contract"].(string)
	id, idOK := object["id"].(string)
	revision, revisionOK := object["revision"].(string)
	pipeline, pipelineOK := object["pipeline_revision"].(string)
	if !contractOK || contract != aiProfileContract || !idOK ||
		!revisionOK || !pipelineOK || !validMachineIdentifier(id, 64) ||
		!validMachineIdentifier(revision, 64) || !validMachineIdentifier(pipeline, 64) {
		return nil, false
	}
	for key := range object {
		switch key {
		case "contract", "id", "revision", "pipeline_revision":
		default:
			return nil, false
		}
	}
	return map[string]string{
		"contract":          contract,
		"id":                id,
		"revision":          revision,
		"pipeline_revision": pipeline,
	}, true
}

func boundedJSONInteger(value any, minimum, maximum float64) (any, bool) {
	number, ok := finiteJSONNumber(value)
	return number, ok &&
		number == math.Trunc(number) &&
		number >= minimum &&
		number <= maximum
}

func validProcessorTimeline(value string) bool {
	if !validBoundedText(value, 64, false) {
		return false
	}
	if _, err := time.Parse(time.RFC3339, value); err == nil {
		return true
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func validMachineIdentifier(value string, maxRunes int) bool {
	if !validBoundedText(value, maxRunes, false) {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("._:/@+-", character) {
			continue
		}
		return false
	}
	return true
}

func sanitizedGPS(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	lat, latOK := finiteJSONNumber(object["lat"])
	lng, lngOK := finiteJSONNumber(object["lng"])
	if !latOK || !lngOK || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return nil, false
	}
	return map[string]any{"lat": lat, "lng": lng, "source": "exif"}, true
}

func finiteJSONNumber(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0)
}

// extractTimelineFromMap accepts only timestamps with an explicit RFC3339
// offset. Naive EXIF values remain visible in processor_metadata with an
// uncertainty marker instead of being silently reinterpreted as UTC.
func extractTimelineFromMap(metadata map[string]any) any {
	for _, key := range []string{"timeline_at", "taken_at"} {
		value, ok := metadata[key].(string)
		if !ok || value == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed
		}
	}
	return nil
}

func hasNaiveTimeline(metadata map[string]any) bool {
	for _, key := range []string{"timeline_at", "taken_at"} {
		value, ok := metadata[key].(string)
		if !ok || value == "" {
			continue
		}
		for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if _, err := time.Parse(layout, value); err == nil {
				return true
			}
		}
	}
	return false
}

func parseAnnotationSuggestions(value any, defaultProcessor string) ([]annotationSuggestion, bool) {
	items, ok := value.([]any)
	if !ok || len(items) > 21 {
		return nil, false
	}
	out := make([]annotationSuggestion, 0, len(items))
	seen := make(map[string]struct{})
	descriptionSeen := false
	tagCount := 0
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok || !annotationFieldsAllowed(object) {
			return nil, false
		}
		kind, kindOK := object["kind"].(string)
		rawValue, valueOK := object["value"].(string)
		confidence, confidenceOK := finiteJSONNumber(object["confidence"])
		source, sourceOK := object["source"].(string)
		provider, providerOK := object["provider"].(string)
		processor, processorOK := object["processor"].(string)
		analysisVersion, versionOK := object["analysis_version"].(string)
		if !kindOK || !valueOK || !confidenceOK || !sourceOK ||
			!providerOK || !processorOK || !versionOK ||
			confidence < 0 || confidence > 1 || source != "model" {
			return nil, false
		}
		switch kind {
		case file.AnnotationKindDescription:
			normalized, valid := modeltext.NormalizePlain(rawValue, 2000)
			if descriptionSeen || !valid || normalized != rawValue {
				return nil, false
			}
			descriptionSeen = true
		case file.AnnotationKindTag:
			tagCount++
			normalized, valid := modeltext.NormalizePlain(rawValue, 64)
			if tagCount > 20 || !valid || normalized != rawValue {
				return nil, false
			}
		default:
			return nil, false
		}
		if processor == "" {
			processor = defaultProcessor
		}
		if !validAnnotationProvenance(provider, 255, true) ||
			!validAnnotationProvenance(processor, 64, true) ||
			!validAnnotationProvenance(analysisVersion, 64, false) {
			return nil, false
		}
		stableKey := annotationStableKey(kind, source, analysisVersion, rawValue)
		if _, duplicate := seen[stableKey]; duplicate {
			return nil, false
		}
		seen[stableKey] = struct{}{}
		out = append(out, annotationSuggestion{
			StableKey:       stableKey,
			Kind:            kind,
			Value:           rawValue,
			Confidence:      confidence,
			Source:          source,
			Provider:        provider,
			Processor:       processor,
			AnalysisVersion: analysisVersion,
		})
	}
	return out, true
}

func annotationFieldsAllowed(object map[string]any) bool {
	allowed := map[string]struct{}{
		"kind": {}, "value": {}, "confidence": {}, "source": {},
		"provider": {}, "processor": {}, "analysis_version": {},
	}
	for key := range object {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

func legacyAnnotationSuggestions(
	resp *workerpb.ProcessResponse,
) ([]annotationSuggestion, bool) {
	if resp == nil {
		return nil, false
	}
	valid := true
	items := make([]map[string]any, 0, 1+len(resp.Tags))
	description := resp.Caption
	if description == "" {
		description = resp.Summary
	}
	if description != "" {
		if value, ok := modeltext.NormalizePlain(description, 2000); ok {
			items = append(items, map[string]any{
				"kind": "description", "value": value, "confidence": 0.5,
				"source": "model", "provider": "", "processor": resp.Processor,
				"analysis_version": "legacy-worker-v1",
			})
		} else {
			valid = false
		}
	}
	seenTags := make(map[string]struct{})
	for _, rawTag := range resp.Tags {
		if len(items) >= 21 {
			valid = false
			continue
		}
		tag := strings.TrimSpace(rawTag)
		if !validBoundedText(tag, 64, false) ||
			modeltext.ContainsHiddenReasoning(tag) {
			valid = false
			continue
		}
		key := strings.ToLower(strings.Join(strings.Fields(tag), " "))
		if _, duplicate := seenTags[key]; duplicate {
			continue
		}
		seenTags[key] = struct{}{}
		items = append(items, map[string]any{
			"kind": "tag", "value": tag, "confidence": 0.5,
			"source": "model", "provider": "", "processor": resp.Processor,
			"analysis_version": "legacy-worker-v1",
		})
	}
	raw := make([]any, len(items))
	for index := range items {
		raw[index] = items[index]
	}
	parsed, ok := parseAnnotationSuggestions(raw, resp.Processor)
	if !ok {
		return nil, false
	}
	return parsed, valid
}

func validBoundedText(value string, maxRunes int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || (!allowEmpty && value == "") ||
		utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func validAnnotationProvenance(value string, maxRunes int, allowEmpty bool) bool {
	return validBoundedText(value, maxRunes, allowEmpty) &&
		!modeltext.ContainsNonDisplay(value)
}

func annotationStableKey(kind, source, analysisVersion, value string) string {
	return enrichmentkey.Stable(kind, source, analysisVersion, value)
}

func persistAnnotationSuggestions(
	ctx context.Context,
	tx pgx.Tx,
	fileID uuid.UUID,
	suggestions []annotationSuggestion,
	reconcileModel bool,
) error {
	if len(suggestions) == 0 && !reconcileModel {
		return nil
	}
	groups := make(map[string][]string)
	if reconcileModel {
		for _, suggestion := range suggestions {
			groups[suggestion.Source] = append(
				groups[suggestion.Source],
				suggestion.StableKey,
			)
		}
		if _, exists := groups["model"]; !exists {
			groups["model"] = []string{}
		}
	}
	for source, stableKeys := range groups {
		if _, err := tx.Exec(ctx, `
			UPDATE file_annotations
			   SET status = 'superseded',
			       state_version = state_version + 1,
			       updated_at = now()
			 WHERE file_id = $1
			   AND source = $2
			   AND status = 'pending'
			   AND NOT (stable_key = ANY($3::text[]))
		`, fileID, source, stableKeys); err != nil {
			return fmt.Errorf("supersede stale file annotations: %w", err)
		}
	}
	for _, suggestion := range suggestions {
		if _, err := tx.Exec(ctx, `
			INSERT INTO file_annotations (
				file_id, stable_key, kind, value_text, confidence,
				source, provider, processor, analysis_version
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (file_id, stable_key) DO UPDATE
			   SET value_text = EXCLUDED.value_text,
			       confidence = EXCLUDED.confidence,
			       provider = EXCLUDED.provider,
			       processor = EXCLUDED.processor,
			       analysis_version = EXCLUDED.analysis_version,
			       status = 'pending',
			       decided_by_user_id = NULL,
			       decided_at = NULL,
			       state_version = file_annotations.state_version + 1,
			       updated_at = now()
			 WHERE file_annotations.status IN ('pending', 'superseded')
			   AND (
					file_annotations.status = 'superseded' OR
					file_annotations.value_text IS DISTINCT FROM EXCLUDED.value_text OR
					file_annotations.confidence IS DISTINCT FROM EXCLUDED.confidence OR
					file_annotations.provider IS DISTINCT FROM EXCLUDED.provider OR
					file_annotations.processor IS DISTINCT FROM EXCLUDED.processor OR
					file_annotations.analysis_version IS DISTINCT FROM EXCLUDED.analysis_version
			   )
		`,
			fileID,
			suggestion.StableKey,
			suggestion.Kind,
			suggestion.Value,
			suggestion.Confidence,
			suggestion.Source,
			suggestion.Provider,
			suggestion.Processor,
			suggestion.AnalysisVersion,
		); err != nil {
			return fmt.Errorf("upsert file annotation: %w", err)
		}
	}
	return nil
}

func embeddingKinds(r *workerpb.ProcessResponse) []string {
	out := make([]string, 0, len(r.Embeddings))
	for k := range r.Embeddings {
		out = append(out, k)
	}
	return out
}
