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
	"slices"
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
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/face"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/indexmeta"
	"github.com/PeterGuy326/mem/server/internal/managedusage"
	"github.com/PeterGuy326/mem/server/internal/modeltext"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
	"github.com/PeterGuy326/mem/server/internal/workerpb"
	"github.com/PeterGuy326/mem/server/internal/workspacelock"
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

	// aiProfileResultCommitHook is a package-private integration-test seam.
	// Production services leave it nil. It pauses a workspace-coordinated
	// result after all writes but before COMMIT while the workspace/profile
	// lock is held.
	aiProfileResultCommitHook func(context.Context) error

	// managedUsageResultCommitHook is a package-private integration-test seam.
	// Production services leave it nil. Tests use it to hold a result/outbox
	// transaction immediately before COMMIT while competing entitlement work
	// proves the database lock ordering.
	managedUsageResultCommitHook func(context.Context) error
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
	SettleUsage(context.Context, uuid.UUID, managedusage.Outcome) error
}

// providerRoute is the complete, request-local model routing decision for one
// indexing job.  AIProfile is mutually exclusive with the legacy fields:
// once a workspace has selected a profile, no mutable provider_settings row
// or Worker process default may be blended into that job.
type providerRoute struct {
	WorkspaceID             uuid.UUID
	ProfileID               string
	ProfileRevision         string
	PipelineRevision        string
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
	faceEmbeddingSchemaDim   = 512
	legacyFaceProvider       = "insightface:buffalo_l"
)

var (
	errManagedUsageCommitUnknown = errors.New(
		"managed usage result commit outcome is unknown",
	)
	errManagedUsageReservationTerminal = errors.New(
		"managed usage result reservation is already terminal",
	)
	errAIProfileResultStale = errors.New(
		"workspace AI profile changed before result commit",
	)
	errAIProfileResultContract = errors.New(
		"workspace AI profile result embedding contract mismatch",
	)
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
	if status != "done" && status != "partial" {
		return fmt.Errorf("indexer: index_status=%s for %s", status, fileID)
	}
	pending, err := s.hasPendingManagedUsageSettlement(ctx, fileID)
	if err != nil {
		return fmt.Errorf("inspect managed usage settlement: %w", err)
	}
	if pending {
		return fmt.Errorf("indexer: managed usage settlement pending for %s", fileID)
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
	settlementReplay, settlementErr := s.resumeManagedUsageSettlement(
		ctx,
		f,
		providers,
	)
	if settlementReplay {
		if settlementErr != nil {
			s.log.Error(
				"indexer.managed_usage_settlement_resume_failed",
				"file_id", f.ID,
				"err", settlementErr,
			)
		} else {
			s.log.Info("indexer.managed_usage_settlement_replayed", "file_id", f.ID)
		}
		// A matching settlement row is committed atomically with the durable
		// file result. Never invoke the Worker again even when the post-commit
		// accounting transition still needs an operator/reconciler retry.
		return
	}
	if settlementErr != nil {
		s.log.Error(
			"indexer.managed_usage_settlement_lookup_failed",
			"file_id", f.ID,
			"err", settlementErr,
		)
		_ = s.setStatus(ctx, f.ID, "partial")
		return
	}
	if err := s.setStatus(ctx, f.ID, "processing"); err != nil {
		s.log.Error("indexer.set_processing", "file_id", f.ID, "err", err)
		return
	}

	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
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
		s.log.Warn("indexer.managed_usage_replay", "file_id", f.ID)
		// A succeeded reservation is now written only after the Worker result
		// has committed. Preserve that durable state on duplicate delivery;
		// historical or otherwise unproved replays stay visibly partial.
		replayStatus := f.IndexStatus
		if replayStatus != "done" && replayStatus != "partial" {
			replayStatus = "partial"
		}
		_ = s.setStatus(ctx, f.ID, replayStatus)
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
		plan := indeterminateManagedUsagePlan(usageHandle)
		if persistErr := s.persistFailedWithSettlement(
			ctx,
			f.ID,
			usageHandle,
			plan,
			providers,
			f.SHA256,
		); persistErr != nil {
			s.terminalizeRejectedIndexResult(
				ctx,
				f.ID,
				"failed",
				persistErr,
			)
			s.log.Error(
				"indexer.managed_usage_failure_persist_failed",
				"file_id", f.ID,
				"err", persistErr,
			)
		}
		s.log.Error("indexer.worker_failed", "file_id", f.ID, "err", err)
		if _, settleErr := s.settleManagedUsageForAttempt(ctx, usageHandle); settleErr != nil {
			s.log.Error("indexer.managed_usage_settle_failed", "file_id", f.ID, "err", settleErr)
		}
		return
	}
	if resp.Status == workerpb.ProcessStatus_STATUS_FAILED {
		plan, receiptErr := managedUsagePlanFromResponse(usageHandle, resp)
		if receiptErr != nil {
			plan = indeterminateManagedUsagePlan(usageHandle)
			s.log.Error("indexer.managed_usage_receipt_invalid", "file_id", f.ID, "err", receiptErr)
		}
		plan = failedResultManagedUsagePlan(plan)
		if persistErr := s.persistFailedWithSettlement(
			ctx,
			f.ID,
			usageHandle,
			plan,
			providers,
			f.SHA256,
		); persistErr != nil {
			s.terminalizeRejectedIndexResult(
				ctx,
				f.ID,
				"failed",
				persistErr,
			)
			s.log.Error(
				"indexer.managed_usage_failure_persist_failed",
				"file_id", f.ID,
				"err", persistErr,
			)
		}
		s.log.Error("indexer.process_failed", "file_id", f.ID, "err", resp.Error)
		if _, settleErr := s.settleManagedUsageForAttempt(ctx, usageHandle); settleErr != nil {
			s.log.Error("indexer.managed_usage_settle_failed", "file_id", f.ID, "err", settleErr)
		}
		return
	}
	if resp.Status == workerpb.ProcessStatus_STATUS_SKIPPED {
		plan, receiptErr := managedUsagePlanFromResponse(usageHandle, resp)
		if receiptErr != nil {
			plan = indeterminateManagedUsagePlan(usageHandle)
			resp.Status = workerpb.ProcessStatus_STATUS_PARTIAL
			s.log.Error("indexer.managed_usage_receipt_invalid", "file_id", f.ID, "err", receiptErr)
		}
		if persistErr := s.persistWithSettlement(
			ctx,
			f.ID,
			resp,
			usageHandle,
			plan,
			providers,
			f.SHA256,
		); persistErr != nil {
			s.terminalizeRejectedIndexResult(
				ctx,
				f.ID,
				"partial",
				persistErr,
			)
			s.log.Error("indexer.persist_failed", "file_id", f.ID, "err", persistErr)
			if _, settleErr := s.settleManagedUsageForAttempt(
				ctx,
				usageHandle,
			); settleErr != nil {
				s.log.Error(
					"indexer.managed_usage_settle_failed",
					"file_id", f.ID,
					"err", settleErr,
				)
			}
			return
		}
		s.log.Info("indexer.skipped", "file_id", f.ID, "reason", "unsupported_mime")
		if _, settleErr := s.settleManagedUsageForAttempt(ctx, usageHandle); settleErr != nil {
			s.log.Error("indexer.managed_usage_settle_failed", "file_id", f.ID, "err", settleErr)
		}
		return
	}
	usagePlan, receiptErr := managedUsagePlanFromResponse(usageHandle, resp)
	if receiptErr != nil {
		usagePlan = indeterminateManagedUsagePlan(usageHandle)
		resp.Status = workerpb.ProcessStatus_STATUS_PARTIAL
		s.log.Error("indexer.managed_usage_receipt_invalid", "file_id", f.ID, "err", receiptErr)
	} else if usagePlan.hasIndeterminate() {
		resp.Status = workerpb.ProcessStatus_STATUS_PARTIAL
	}
	if err := s.persistWithSettlement(ctx, f.ID, resp, usageHandle, usagePlan, providers, f.SHA256); err != nil {
		if resultRejectedBeforeCommit(err) {
			s.terminalizeRejectedIndexResult(ctx, f.ID, "partial", err)
		} else if !errors.Is(err, errManagedUsageCommitUnknown) {
			discardPlan := failedResultManagedUsagePlan(usagePlan)
			if persistErr := s.persistFailedWithSettlement(
				ctx,
				f.ID,
				usageHandle,
				discardPlan,
				providers,
				f.SHA256,
			); persistErr != nil {
				s.terminalizeRejectedIndexResult(
					ctx,
					f.ID,
					"partial",
					persistErr,
				)
				s.log.Error(
					"indexer.managed_usage_failure_persist_failed",
					"file_id", f.ID,
					"err", persistErr,
				)
			}
		}
		s.log.Error("indexer.persist_failed", "file_id", f.ID, "err", err)
		if _, settleErr := s.settleManagedUsageForAttempt(ctx, usageHandle); settleErr != nil {
			s.log.Error("indexer.managed_usage_settle_failed", "file_id", f.ID, "err", settleErr)
		}
		return
	}
	if _, settleErr := s.settleManagedUsageForAttempt(ctx, usageHandle); settleErr != nil {
		s.log.Error("indexer.managed_usage_settle_failed", "file_id", f.ID, "err", settleErr)
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

// persist is the model-free/test entry point. Managed indexing uses
// persistWithSettlement so the file result and its closed settlement intent
// commit atomically.
func (s *Service) persist(ctx context.Context, fileID uuid.UUID, resp *workerpb.ProcessResponse) error {
	return s.persistWithSettlement(
		ctx,
		fileID,
		resp,
		nil,
		managedUsageSettlementPlan{},
		providerRoute{},
		"",
	)
}

// persistWithSettlement writes the Worker output and, when applicable, one
// durable row per managed stage in the same PostgreSQL transaction. If the
// source file was deleted while the Worker was in flight, the exact stage
// outcomes are committed as detached rows instead; no file output remains to
// persist, but the provider calls still require durable accounting.
// Model-produced descriptions/tags become pending annotations; only explicit
// user decisions may change the backwards-compatible files.summary/files.tags
// projections.
func (s *Service) persistWithSettlement(
	ctx context.Context,
	fileID uuid.UUID,
	resp *workerpb.ProcessResponse,
	usageHandle *managedusage.Handle,
	usagePlan managedUsageSettlementPlan,
	route providerRoute,
	contentSHA256 string,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck — committed below on success

	if validationErr := lockAndValidateAIProfileResult(
		ctx,
		tx,
		route,
		resp,
	); validationErr != nil {
		return s.commitRejectedManagedUsageSettlement(
			ctx,
			tx,
			usageHandle,
			usagePlan,
			route,
			contentSHA256,
			validationErr,
		)
	}
	if err := lockManagedUsageResultReservations(
		ctx,
		tx,
		route.WorkspaceID,
		usageHandle,
	); err != nil {
		return err
	}

	enrichment := parseWorkerEnrichment(resp)
	status := "done"
	if resp.Status == workerpb.ProcessStatus_STATUS_PARTIAL || enrichment.Partial {
		status = "partial"
	}

	tag, err := tx.Exec(ctx,
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
	if tag.RowsAffected() != 1 {
		if tag.RowsAffected() != 0 {
			return errors.New("update files affected an unexpected number of rows")
		}
		if usageHandle == nil || !usageHandle.HasManagedStages() {
			return errors.New("update files: file not found")
		}
		if err := enqueueManagedUsageSettlement(
			ctx,
			tx,
			nil,
			contentSHA256,
			route,
			usageHandle,
			usagePlan,
			false,
		); err != nil {
			return err
		}
		if err := s.runAIProfileResultCommitHook(ctx, route); err != nil {
			return err
		}
		if err := s.runManagedUsageResultCommitHook(ctx, usageHandle); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("%w: %v", errManagedUsageCommitUnknown, err)
		}
		return nil
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

	if err := enqueueManagedUsageSettlement(
		ctx,
		tx,
		&fileID,
		contentSHA256,
		route,
		usageHandle,
		usagePlan,
		true,
	); err != nil {
		return err
	}

	if err := s.runAIProfileResultCommitHook(ctx, route); err != nil {
		return err
	}
	if err := s.runManagedUsageResultCommitHook(ctx, usageHandle); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		// PostgreSQL COMMIT errors are not proof of rollback: the server may
		// have committed while the acknowledgement was lost. The caller must
		// not apply an opposite direct settlement in this state; a retry will
		// inspect the durable outbox first.
		return fmt.Errorf("%w: %v", errManagedUsageCommitUnknown, err)
	}
	return nil
}

// persistFailedWithSettlement stores a failed attempt and its closed
// accounting outcomes atomically without touching the last usable embeddings
// or enrichment. replayable=false means a later delivery may retry only after
// these rows have settled and been removed.
func (s *Service) persistFailedWithSettlement(
	ctx context.Context,
	fileID uuid.UUID,
	usageHandle *managedusage.Handle,
	usagePlan managedUsageSettlementPlan,
	route providerRoute,
	contentSHA256 string,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin failed-result tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- committed below on success

	if validationErr := lockAndValidateAIProfileResult(
		ctx,
		tx,
		route,
		nil,
	); validationErr != nil {
		return s.commitRejectedManagedUsageSettlement(
			ctx,
			tx,
			usageHandle,
			usagePlan,
			route,
			contentSHA256,
			validationErr,
		)
	}
	if err := lockManagedUsageResultReservations(
		ctx,
		tx,
		route.WorkspaceID,
		usageHandle,
	); err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE files
		   SET index_status = 'failed',
		       updated_at = now()
		 WHERE id = $1
	`, fileID)
	if err != nil {
		return fmt.Errorf("store failed index status: %w", err)
	}
	if tag.RowsAffected() != 1 {
		if tag.RowsAffected() != 0 {
			return errors.New("store failed index status affected an unexpected number of rows")
		}
		if usageHandle == nil || !usageHandle.HasManagedStages() {
			return errors.New("store failed index status: file not found")
		}
		if err := enqueueManagedUsageSettlement(
			ctx,
			tx,
			nil,
			contentSHA256,
			route,
			usageHandle,
			usagePlan,
			false,
		); err != nil {
			return err
		}
		if err := s.runAIProfileResultCommitHook(ctx, route); err != nil {
			return err
		}
		if err := s.runManagedUsageResultCommitHook(ctx, usageHandle); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("%w: %v", errManagedUsageCommitUnknown, err)
		}
		return nil
	}
	if err := enqueueManagedUsageSettlement(
		ctx,
		tx,
		&fileID,
		contentSHA256,
		route,
		usageHandle,
		usagePlan,
		false,
	); err != nil {
		return err
	}
	if err := s.runAIProfileResultCommitHook(ctx, route); err != nil {
		return err
	}
	if err := s.runManagedUsageResultCommitHook(ctx, usageHandle); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: %v", errManagedUsageCommitUnknown, err)
	}
	return nil
}

// lockAndValidateAIProfileResult is the first database action in every
// workspace-routed result transaction. Select takes the same workspace-row
// lock across its corpus gate, provider probe, and snapshot upsert. Once this
// transaction owns the lock, a fresh READ COMMITTED statement must either
// still have no profile for a legacy route or match the complete immutable
// profile route. Comparing only the text provider would miss V1→V2 pipeline,
// MIME, visual, or model-stage changes that reuse the same embedding model.
func lockAndValidateAIProfileResult(
	ctx context.Context,
	tx pgx.Tx,
	route providerRoute,
	resp *workerpb.ProcessResponse,
) error {
	// Model-free internal callers use an empty route. Every production legacy
	// route resolves WorkspaceID before Worker dispatch and therefore enters
	// the same database boundary as an explicit profile route.
	if route.WorkspaceID == uuid.Nil && route.AIProfile == nil {
		return nil
	}
	if route.WorkspaceID == uuid.Nil {
		return errors.New("workspace AI profile result workspace is invalid")
	}
	if _, err := workspacelock.ForAIProfileCoordination(
		ctx,
		tx,
		route.WorkspaceID,
	); err != nil {
		return fmt.Errorf("lock workspace AI profile result: %w", err)
	}
	active, err := aiprofile.SelectionForResultTx(
		ctx,
		tx,
		route.WorkspaceID,
	)
	if errors.Is(err, aiprofile.ErrNotFound) {
		if route.AIProfile == nil {
			return validateLegacyEmbeddingResult(route, resp)
		}
		return errAIProfileResultStale
	}
	if err != nil {
		return fmt.Errorf("load workspace AI profile result snapshot: %w", err)
	}
	if route.AIProfile == nil {
		// A first profile selection committed while this legacy Worker call was
		// in flight. Its output belongs to the pre-profile pipeline and must not
		// become the active profile's corpus.
		return errAIProfileResultStale
	}
	if !providerRouteMatchesSelection(route, active) {
		return errAIProfileResultStale
	}
	return validateAIProfileEmbeddingResult(route, resp)
}

// validateAIProfileEmbeddingResult binds every returned vector kind to the
// immutable profile snapshot after the result transaction owns the workspace
// lock. PostgreSQL vector dimensions alone cannot prove model identity, and
// embeddings_visual does not persist a provider column.
func validateAIProfileEmbeddingResult(
	route providerRoute,
	resp *workerpb.ProcessResponse,
) error {
	if route.AIProfile == nil || resp == nil {
		return nil
	}
	for kind, embedding := range resp.Embeddings {
		var expected workerclient.ProviderStage
		switch kind {
		case "text":
			expected = route.AIProfile.Embedding
		case "visual":
			expected = route.AIProfile.VisualEmbedding
		case "face":
			// The two published V1 snapshots used file-enrichment-v1, whose
			// ImageProcessor may emit the fixed local InsightFace model. Face
			// was not a selectable profile Stage, so preserve only that exact
			// historical contract; V2 and future profiles must declare a new
			// reviewed contract before accepting face vectors.
			if !allowsLegacyProfileFace(route.AIProfile) {
				return fmt.Errorf(
					"%w: undeclared face embedding",
					errAIProfileResultContract,
				)
			}
			expected = workerclient.ProviderStage{
				Enabled:    true,
				Provider:   legacyFaceProvider,
				Dimensions: faceEmbeddingSchemaDim,
			}
		default:
			return fmt.Errorf(
				"%w: unknown embedding kind %q",
				errAIProfileResultContract,
				kind,
			)
		}
		if !expected.Enabled {
			return fmt.Errorf(
				"%w: disabled embedding kind %q",
				errAIProfileResultContract,
				kind,
			)
		}
		if embedding == nil ||
			embedding.Provider != expected.Provider ||
			int(embedding.Dim) != expected.Dimensions {
			return fmt.Errorf(
				"%w: %s provider or set dimensions",
				errAIProfileResultContract,
				kind,
			)
		}
		for index, row := range embedding.Rows {
			if row == nil || len(row.Values) != expected.Dimensions {
				return fmt.Errorf(
					"%w: %s row %d dimensions",
					errAIProfileResultContract,
					kind,
					index,
				)
			}
		}
	}
	return nil
}

func validateLegacyEmbeddingResult(
	route providerRoute,
	resp *workerpb.ProcessResponse,
) error {
	if resp == nil {
		return nil
	}
	text, exists := resp.Embeddings["text"]
	if !exists {
		return nil
	}
	if text == nil ||
		strings.TrimSpace(text.Provider) == "" ||
		(route.EmbeddingProvider != "" &&
			text.Provider != route.EmbeddingProvider) ||
		int(text.Dim) != textEmbeddingSchemaDim {
		return fmt.Errorf(
			"%w: legacy text provider or set dimensions",
			errAIProfileResultContract,
		)
	}
	for index, row := range text.Rows {
		if row == nil || len(row.Values) != textEmbeddingSchemaDim {
			return fmt.Errorf(
				"%w: legacy text row %d dimensions",
				errAIProfileResultContract,
				index,
			)
		}
	}
	return nil
}

// commitRejectedManagedUsageSettlement preserves accounting for provider work
// whose business output lost a profile-selection race or failed a
// deterministic embedding contract check. The caller still owns the workspace
// lock acquired by lockAndValidateAIProfileResult. Continue in the global
// workspace→entitlement→usage order, commit only detached closed settlement
// intent, and return the rejection after COMMIT so the caller can terminalize
// the file without ever publishing rejected output.
func (s *Service) commitRejectedManagedUsageSettlement(
	ctx context.Context,
	tx pgx.Tx,
	handle *managedusage.Handle,
	plan managedUsageSettlementPlan,
	route providerRoute,
	contentSHA256 string,
	rejection error,
) error {
	if !resultBusinessOutputRejected(rejection) ||
		handle == nil ||
		!handle.HasManagedStages() {
		return rejection
	}
	if err := lockManagedUsageResultReservations(
		ctx,
		tx,
		route.WorkspaceID,
		handle,
	); err != nil {
		return err
	}
	if err := enqueueManagedUsageSettlement(
		ctx,
		tx,
		nil,
		contentSHA256,
		route,
		handle,
		plan,
		false,
	); err != nil {
		return err
	}
	if err := s.runAIProfileResultCommitHook(ctx, route); err != nil {
		return err
	}
	if err := s.runManagedUsageResultCommitHook(ctx, handle); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: %v", errManagedUsageCommitUnknown, err)
	}
	return rejection
}

func allowsLegacyProfileFace(profile *workerclient.AIProfileOptions) bool {
	if profile == nil || profile.PipelineRevision != "file-enrichment-v1" {
		return false
	}
	return profile.ID == aiprofile.LocalFastV1 ||
		profile.ID == aiprofile.IdealabQualityV1
}

func providerRouteMatchesSelection(
	route providerRoute,
	active *aiprofile.Selection,
) bool {
	expected, err := routeFromAIProfile(active)
	if err != nil ||
		route.AIProfile == nil ||
		expected.AIProfile == nil {
		return false
	}
	return route.WorkspaceID == expected.WorkspaceID &&
		route.ProfileID == expected.ProfileID &&
		route.ProfileRevision == expected.ProfileRevision &&
		route.PipelineRevision == expected.PipelineRevision &&
		route.DataEgress == expected.DataEgress &&
		route.EmbeddingProvider == expected.EmbeddingProvider &&
		route.VisualEmbeddingProvider == expected.VisualEmbeddingProvider &&
		route.LLMProvider == expected.LLMProvider &&
		route.VLMProvider == expected.VLMProvider &&
		route.ASRProvider == expected.ASRProvider &&
		*route.AIProfile == *expected.AIProfile &&
		slices.Equal(route.AllowedMIMETypes, expected.AllowedMIMETypes)
}

// lockManagedUsageResultReservations serializes a result/outbox commit with
// period rollover, stale reconciliation, and post-commit settlement. The
// shared order is always workspace_entitlements first and then usage rows in
// UUID order. Holding both locks until COMMIT closes the gap where another
// transaction could classify a reservation before its closed outbox outcome
// became visible.
func lockManagedUsageResultReservations(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
	handle *managedusage.Handle,
) error {
	if handle == nil || !handle.HasManagedStages() {
		return nil
	}
	if workspaceID == uuid.Nil {
		return errors.New("managed usage result workspace is invalid")
	}

	reservations := handle.Reservations()
	if len(reservations) == 0 {
		return errors.New("managed usage result has no reservations")
	}
	usageIDs := make([]uuid.UUID, 0, len(reservations))
	seen := make(map[uuid.UUID]struct{}, len(reservations))
	for _, reservation := range reservations {
		if reservation.Replayed || reservation.UsageID == uuid.Nil {
			return errors.New("managed usage result reservation is invalid")
		}
		if _, duplicate := seen[reservation.UsageID]; duplicate {
			return errors.New("managed usage result reservation is duplicated")
		}
		seen[reservation.UsageID] = struct{}{}
		usageIDs = append(usageIDs, reservation.UsageID)
	}

	var lockedWorkspaceID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT workspace_id
		  FROM workspace_entitlements
		 WHERE workspace_id = $1
		 FOR UPDATE
	`, workspaceID).Scan(&lockedWorkspaceID); err != nil {
		return fmt.Errorf("lock managed usage result entitlement: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT id, workspace_id, status
		  FROM managed_embedding_usage
		 WHERE id = ANY($1::uuid[])
		 ORDER BY id
		 FOR UPDATE
	`, usageIDs)
	if err != nil {
		return fmt.Errorf("lock managed usage result reservations: %w", err)
	}
	defer rows.Close()

	locked := 0
	for rows.Next() {
		var usageID, usageWorkspaceID uuid.UUID
		var status string
		if err := rows.Scan(&usageID, &usageWorkspaceID, &status); err != nil {
			return fmt.Errorf("scan managed usage result reservation: %w", err)
		}
		if usageWorkspaceID != lockedWorkspaceID {
			return errors.New("managed usage result reservation workspace is invalid")
		}
		if status != entitlement.StatusReserved {
			return errManagedUsageReservationTerminal
		}
		locked++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate managed usage result reservations: %w", err)
	}
	if locked != len(usageIDs) {
		return errors.New("managed usage result reservation is missing")
	}
	return nil
}

// terminalizeRejectedIndexResult is deliberately limited to lock-time proof
// that the result transaction published no business output because either the
// active profile changed, the signed vector output violated its deterministic
// contract, or a competing entitlement transaction closed a reservation. A
// rejected managed result may already have committed detached accounting
// intent for provider work. A COMMIT error is never routed here: its result
// status remains untouched until durable state can be inspected.
func (s *Service) terminalizeRejectedIndexResult(
	ctx context.Context,
	fileID uuid.UUID,
	status string,
	cause error,
) {
	if !resultRejectedBeforeCommit(cause) {
		return
	}
	if err := s.setStatus(ctx, fileID, status); err != nil {
		s.log.Error(
			"indexer.managed_usage_terminal_status_failed",
			"file_id", fileID,
			"err", err,
		)
	}
}

func resultRejectedBeforeCommit(err error) bool {
	return errors.Is(err, errManagedUsageReservationTerminal) ||
		resultBusinessOutputRejected(err)
}

func resultBusinessOutputRejected(err error) bool {
	return errors.Is(err, errAIProfileResultStale) ||
		errors.Is(err, errAIProfileResultContract)
}

func (s *Service) runAIProfileResultCommitHook(
	ctx context.Context,
	route providerRoute,
) error {
	if s == nil ||
		s.aiProfileResultCommitHook == nil ||
		route.WorkspaceID == uuid.Nil {
		return nil
	}
	if err := s.aiProfileResultCommitHook(ctx); err != nil {
		return fmt.Errorf("pause workspace AI profile result commit: %w", err)
	}
	return nil
}

func (s *Service) runManagedUsageResultCommitHook(
	ctx context.Context,
	handle *managedusage.Handle,
) error {
	if s == nil ||
		s.managedUsageResultCommitHook == nil ||
		handle == nil ||
		!handle.HasManagedStages() {
		return nil
	}
	if err := s.managedUsageResultCommitHook(ctx); err != nil {
		return fmt.Errorf("pause managed usage result commit: %w", err)
	}
	return nil
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
	if err := s.pool.QueryRow(ctx, `
		SELECT id
		  FROM workspaces
		 WHERE resource_owner_user_id = $1
	`, userID).Scan(&route.WorkspaceID); err != nil {
		return providerRoute{}, fmt.Errorf("resolve legacy provider workspace: %w", err)
	}
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
	if selection.VisualEmbedding.Enabled &&
		(selection.VisualEmbedding.Provider == "" ||
			selection.VisualEmbedding.Dimensions != visualEmbeddingSchemaDim) {
		return providerRoute{}, fmt.Errorf("workspace AI profile has an invalid visual embedding stage")
	}
	profile := &workerclient.AIProfileOptions{
		Contract:         aiProfileContract,
		ID:               selection.ProfileID,
		Revision:         selection.ProfileRevision,
		PipelineRevision: selection.PipelineRevision,
		DataEgress:       selection.DataEgress,
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
		PipelineRevision:        selection.PipelineRevision,
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
		WorkspaceID:      route.WorkspaceID,
		FileID:           f.ID,
		ContentSHA256:    f.SHA256,
		ProfileID:        route.ProfileID,
		ProfileRevision:  route.ProfileRevision,
		PipelineRevision: route.PipelineRevision,
		Stages:           stages,
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

const managedUsageReceiptContract = "mem.managed-stage-receipt/v1"

type managedUsageSettlementPlan struct {
	finalize      []managedusage.Stage
	release       []managedusage.Stage
	indeterminate []managedusage.Stage
}

type managedUsageSettlementRow struct {
	UsageID    uuid.UUID
	Stage      managedusage.Stage
	Outcome    managedusage.Outcome
	Settled    bool
	Replayable bool
}

func managedUsagePlanFromResponse(
	handle *managedusage.Handle,
	resp *workerpb.ProcessResponse,
) (managedUsageSettlementPlan, error) {
	var plan managedUsageSettlementPlan
	if handle == nil || !handle.HasManagedStages() {
		return plan, nil
	}
	if resp == nil || len(resp.MetadataJson) == 0 {
		return plan, errors.New("managed usage receipt is missing")
	}

	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(resp.MetadataJson, &metadata); err != nil {
		return plan, errors.New("managed usage receipt metadata is invalid")
	}
	raw, ok := metadata["managed_usage"]
	if !ok {
		return plan, errors.New("managed usage receipt is missing")
	}
	var receipt struct {
		Contract string            `json:"contract"`
		Stages   map[string]string `json:"stages"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil ||
		receipt.Contract != managedUsageReceiptContract ||
		receipt.Stages == nil {
		return plan, errors.New("managed usage receipt is invalid")
	}

	reservations := handle.Reservations()
	if len(receipt.Stages) != len(reservations) {
		return plan, errors.New("managed usage receipt stage set is invalid")
	}
	for _, reservation := range reservations {
		if reservation.Replayed {
			return plan, errors.New("managed usage receipt cannot settle a replay")
		}
		outcome, exists := receipt.Stages[string(reservation.Stage)]
		if !exists {
			return plan, errors.New("managed usage receipt stage set is invalid")
		}
		switch outcome {
		case "succeeded":
			plan.finalize = append(plan.finalize, reservation.Stage)
		case "not_invoked":
			plan.release = append(plan.release, reservation.Stage)
		case "indeterminate":
			plan.indeterminate = append(plan.indeterminate, reservation.Stage)
		default:
			return managedUsageSettlementPlan{}, errors.New("managed usage receipt outcome is invalid")
		}
	}
	return plan, nil
}

func applyManagedUsagePlan(
	ctx context.Context,
	handle *managedusage.Handle,
	plan managedUsageSettlementPlan,
) error {
	if handle == nil {
		return nil
	}
	settleCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		managedUsageSettlementTimeout,
	)
	defer cancel()

	var errs []error
	if err := handle.ReleaseStagesNotInvoked(settleCtx, plan.release); err != nil {
		errs = append(errs, err)
	}
	if err := handle.FinalizeStages(settleCtx, plan.finalize); err != nil {
		errs = append(errs, err)
	}
	if err := handle.MarkStagesIndeterminate(settleCtx, plan.indeterminate); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (p managedUsageSettlementPlan) hasIndeterminate() bool {
	return len(p.indeterminate) > 0
}

func indeterminateManagedUsagePlan(
	handle *managedusage.Handle,
) managedUsageSettlementPlan {
	var plan managedUsageSettlementPlan
	if handle == nil {
		return plan
	}
	for _, reservation := range handle.Reservations() {
		if !reservation.Replayed {
			plan.indeterminate = append(plan.indeterminate, reservation.Stage)
		}
	}
	return plan
}

// failedResultManagedUsagePlan preserves proof that a provider was not
// invoked, but treats every attempted stage conservatively when its file
// result cannot be committed as usable output.
func failedResultManagedUsagePlan(
	plan managedUsageSettlementPlan,
) managedUsageSettlementPlan {
	return managedUsageSettlementPlan{
		release: append([]managedusage.Stage(nil), plan.release...),
		indeterminate: append(
			append([]managedusage.Stage(nil), plan.finalize...),
			plan.indeterminate...,
		),
	}
}

func (p managedUsageSettlementPlan) outcomes() (map[managedusage.Stage]managedusage.Outcome, error) {
	outcomes := make(map[managedusage.Stage]managedusage.Outcome)
	add := func(stages []managedusage.Stage, outcome managedusage.Outcome) error {
		for _, stage := range stages {
			if _, duplicate := outcomes[stage]; duplicate {
				return errors.New("managed usage settlement plan contains a duplicate stage")
			}
			outcomes[stage] = outcome
		}
		return nil
	}
	if err := add(p.finalize, managedusage.OutcomeSucceeded); err != nil {
		return nil, err
	}
	if err := add(p.release, managedusage.OutcomeNotInvoked); err != nil {
		return nil, err
	}
	if err := add(p.indeterminate, managedusage.OutcomeIndeterminate); err != nil {
		return nil, err
	}
	return outcomes, nil
}

func enqueueManagedUsageSettlement(
	ctx context.Context,
	tx pgx.Tx,
	fileID *uuid.UUID,
	contentSHA256 string,
	route providerRoute,
	handle *managedusage.Handle,
	plan managedUsageSettlementPlan,
	replayable bool,
) error {
	if handle == nil || !handle.HasManagedStages() {
		outcomes, err := plan.outcomes()
		if err != nil {
			return err
		}
		if len(outcomes) != 0 {
			return errors.New("managed usage settlement plan has no reservation handle")
		}
		return nil
	}
	if (fileID != nil && *fileID == uuid.Nil) ||
		len(contentSHA256) != 64 ||
		route.ProfileID == "" ||
		route.ProfileRevision == "" ||
		route.PipelineRevision == "" {
		return errors.New("managed usage settlement identity is invalid")
	}

	outcomes, err := plan.outcomes()
	if err != nil {
		return err
	}
	reservations := handle.Reservations()
	if len(reservations) == 0 || len(reservations) != len(outcomes) {
		return errors.New("managed usage settlement stage set is invalid")
	}
	var nullableFileID any
	var nullableContentSHA256 any
	if fileID != nil {
		nullableFileID = *fileID
		nullableContentSHA256 = contentSHA256
	}
	for _, reservation := range reservations {
		if reservation.Replayed {
			return errors.New("managed usage replay cannot create a settlement row")
		}
		outcome, ok := outcomes[reservation.Stage]
		if !ok {
			return errors.New("managed usage settlement stage set is invalid")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO managed_ai_stage_settlement_outbox (
			    usage_id,
			    file_id,
			    content_sha256,
			    profile_id,
			    profile_revision,
			    pipeline_revision,
			    stage,
			    outcome,
			    replayable
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`,
			reservation.UsageID,
			nullableFileID,
			nullableContentSHA256,
			route.ProfileID,
			route.ProfileRevision,
			route.PipelineRevision,
			string(reservation.Stage),
			string(outcome),
			replayable,
		); err != nil {
			return fmt.Errorf("enqueue managed usage settlement: %w", err)
		}
	}
	return nil
}

func (s *Service) resumeManagedUsageSettlement(
	ctx context.Context,
	f *file.File,
	route providerRoute,
) (bool, error) {
	if route.AIProfile == nil ||
		route.DataEgress != aiprofile.DataEgressManagedIdealab {
		return false, nil
	}
	expected := managedStageSet(managedStagesForMIME(route, f.MIME))
	if len(expected) == 0 {
		return false, nil
	}

	rows, err := s.loadManagedUsageSettlementRows(ctx, f.ID, f.SHA256, route)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	if len(rows) != len(expected) {
		return true, errors.New("durable managed usage settlement stage set is incomplete")
	}
	seen := make(map[managedusage.Stage]struct{}, len(rows))
	replayable := rows[0].Replayable
	for _, row := range rows {
		if _, ok := expected[row.Stage]; !ok {
			return true, errors.New("durable managed usage settlement has an unexpected stage")
		}
		if _, duplicate := seen[row.Stage]; duplicate {
			return true, errors.New("durable managed usage settlement has a duplicate stage")
		}
		if row.Replayable != replayable {
			return true, errors.New("durable managed usage settlement replay policy is inconsistent")
		}
		seen[row.Stage] = struct{}{}
	}
	if _, err = s.settleManagedUsageRows(ctx, rows); err != nil {
		return true, err
	}
	if replayable {
		return true, nil
	}
	if err := s.deleteRetryableManagedUsageSettlementRows(
		ctx,
		f.ID,
		f.SHA256,
		route,
	); err != nil {
		return true, err
	}
	// Failed-attempt rows are only a durable accounting hand-off. Once every
	// closed outcome is settled, remove them so a released stage can derive a
	// fresh reservation and the queue delivery may safely retry.
	return false, nil
}

func (s *Service) deleteRetryableManagedUsageSettlementRows(
	ctx context.Context,
	fileID uuid.UUID,
	contentSHA256 string,
	route providerRoute,
) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM managed_ai_stage_settlement_outbox
		 WHERE file_id = $1
		   AND content_sha256 = $2
		   AND profile_id = $3
		   AND profile_revision = $4
		   AND pipeline_revision = $5
		   AND replayable = false
		   AND settled_at IS NOT NULL
	`,
		fileID,
		contentSHA256,
		route.ProfileID,
		route.ProfileRevision,
		route.PipelineRevision,
	)
	if err != nil {
		return fmt.Errorf("remove settled failed-attempt outcomes: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("settled failed-attempt outcomes disappeared")
	}
	return nil
}

func managedStageSet(stages []managedusage.StageSpec) map[managedusage.Stage]struct{} {
	result := make(map[managedusage.Stage]struct{}, len(stages))
	for _, stage := range stages {
		// The current managed catalog namespace is an explicit egress/billing
		// boundary. Fixed local stages in a future managed profile do not get
		// entitlement reservations or outbox rows.
		if aiprofile.IsManagedCatalogProvider(stage.ProviderSpec) &&
			!isLocalProfileProvider(stage.ProviderSpec) {
			result[stage.Stage] = struct{}{}
		}
	}
	return result
}

func isLocalProfileProvider(spec string) bool {
	vendor, _, ok := strings.Cut(spec, ":")
	if !ok {
		return false
	}
	switch vendor {
	case "ollama", "clip", "faster-whisper", "whisper":
		return true
	default:
		return false
	}
}

func (s *Service) loadManagedUsageSettlementRows(
	ctx context.Context,
	fileID uuid.UUID,
	contentSHA256 string,
	route providerRoute,
) ([]managedUsageSettlementRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT usage_id, stage, outcome, settled_at IS NOT NULL, replayable
		  FROM managed_ai_stage_settlement_outbox
		 WHERE file_id = $1
		   AND content_sha256 = $2
		   AND profile_id = $3
		   AND profile_revision = $4
		   AND pipeline_revision = $5
		 ORDER BY stage, usage_id
	`,
		fileID,
		contentSHA256,
		route.ProfileID,
		route.ProfileRevision,
		route.PipelineRevision,
	)
	if err != nil {
		return nil, fmt.Errorf("load managed usage settlement: %w", err)
	}
	defer rows.Close()

	result := make([]managedUsageSettlementRow, 0, 3)
	for rows.Next() {
		var row managedUsageSettlementRow
		if err := rows.Scan(
			&row.UsageID,
			&row.Stage,
			&row.Outcome,
			&row.Settled,
			&row.Replayable,
		); err != nil {
			return nil, fmt.Errorf("scan managed usage settlement: %w", err)
		}
		if !validManagedUsageOutcome(row.Outcome) {
			return nil, errors.New("managed usage settlement outcome is invalid")
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed usage settlements: %w", err)
	}
	return result, nil
}

func validManagedUsageOutcome(outcome managedusage.Outcome) bool {
	switch outcome {
	case managedusage.OutcomeSucceeded,
		managedusage.OutcomeNotInvoked,
		managedusage.OutcomeIndeterminate:
		return true
	default:
		return false
	}
}

func (s *Service) settleManagedUsageForAttempt(
	ctx context.Context,
	handle *managedusage.Handle,
) (int, error) {
	if handle == nil {
		return 0, nil
	}
	reservations := handle.Reservations()
	if len(reservations) == 0 {
		return 0, nil
	}
	usageIDs := make([]uuid.UUID, 0, len(reservations))
	for _, reservation := range reservations {
		if reservation.UsageID != uuid.Nil {
			usageIDs = append(usageIDs, reservation.UsageID)
		}
	}
	if len(usageIDs) == 0 {
		return 0, nil
	}

	queryRows, err := s.pool.Query(ctx, `
		SELECT usage_id, stage, outcome, false, replayable
		  FROM managed_ai_stage_settlement_outbox
		 WHERE settled_at IS NULL
		   AND usage_id = ANY($1::uuid[])
		 ORDER BY created_at, usage_id
	`, usageIDs)
	if err != nil {
		return 0, fmt.Errorf("load attempt managed usage settlements: %w", err)
	}
	defer queryRows.Close()

	rows := make([]managedUsageSettlementRow, 0, len(usageIDs))
	for queryRows.Next() {
		var row managedUsageSettlementRow
		if err := queryRows.Scan(
			&row.UsageID,
			&row.Stage,
			&row.Outcome,
			&row.Settled,
			&row.Replayable,
		); err != nil {
			return 0, fmt.Errorf("scan attempt managed usage settlement: %w", err)
		}
		if !validManagedUsageOutcome(row.Outcome) {
			return 0, errors.New("attempt managed usage settlement outcome is invalid")
		}
		rows = append(rows, row)
	}
	if err := queryRows.Err(); err != nil {
		return 0, fmt.Errorf("iterate attempt managed usage settlements: %w", err)
	}
	return s.settleManagedUsageRows(ctx, rows)
}

// ReconcileManagedUsageSettlements resumes post-commit accounting before the
// entitlement TTL reconciler can classify a crash-orphaned reservation as
// indeterminate. It is safe to call concurrently: ledger transitions and the
// settled_at update are both idempotent.
func (s *Service) ReconcileManagedUsageSettlements(
	ctx context.Context,
	limit int,
) (int, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.loadPendingManagedUsageSettlementRows(ctx, uuid.Nil, limit)
	if err != nil {
		return 0, err
	}
	return s.settleManagedUsageRows(ctx, rows)
}

func (s *Service) loadPendingManagedUsageSettlementRows(
	ctx context.Context,
	fileID uuid.UUID,
	limit int,
) ([]managedUsageSettlementRow, error) {
	query := `
		SELECT usage_id, stage, outcome, false, replayable
		  FROM managed_ai_stage_settlement_outbox
		 WHERE settled_at IS NULL`
	args := []any{}
	if fileID != uuid.Nil {
		query += ` AND file_id = $1`
		args = append(args, fileID)
	}
	query += ` ORDER BY created_at, usage_id`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load pending managed usage settlements: %w", err)
	}
	defer rows.Close()

	result := make([]managedUsageSettlementRow, 0)
	for rows.Next() {
		var row managedUsageSettlementRow
		if err := rows.Scan(
			&row.UsageID,
			&row.Stage,
			&row.Outcome,
			&row.Settled,
			&row.Replayable,
		); err != nil {
			return nil, fmt.Errorf("scan pending managed usage settlement: %w", err)
		}
		if !validManagedUsageOutcome(row.Outcome) {
			return nil, errors.New("pending managed usage settlement outcome is invalid")
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending managed usage settlements: %w", err)
	}
	return result, nil
}

func (s *Service) settleManagedUsageRows(
	ctx context.Context,
	rows []managedUsageSettlementRow,
) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	if s.managedUsage == nil {
		return 0, managedusage.ErrEntitlementUnavailable
	}

	settled := 0
	var errs []error
	for _, row := range rows {
		if row.Settled {
			continue
		}
		settleCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			managedUsageSettlementTimeout,
		)
		if err := s.managedUsage.SettleUsage(
			settleCtx,
			row.UsageID,
			row.Outcome,
		); err != nil {
			cancel()
			errs = append(errs, fmt.Errorf("settle usage %s: %w", row.UsageID, err))
			continue
		}
		tag, err := s.pool.Exec(settleCtx, `
			UPDATE managed_ai_stage_settlement_outbox
			   SET settled_at = COALESCE(settled_at, now())
			 WHERE usage_id = $1
		`, row.UsageID)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("mark managed usage settlement complete: %w", err))
			continue
		}
		if tag.RowsAffected() != 1 {
			errs = append(errs, errors.New("managed usage settlement row disappeared"))
			continue
		}
		settled++
	}
	return settled, errors.Join(errs...)
}

func (s *Service) hasPendingManagedUsageSettlement(
	ctx context.Context,
	fileID uuid.UUID,
) (bool, error) {
	var pending bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1
		      FROM managed_ai_stage_settlement_outbox
		     WHERE file_id = $1
		       AND settled_at IS NULL
		)
	`, fileID).Scan(&pending); err != nil {
		return false, err
	}
	return pending, nil
}

func discardManagedUsagePlan(
	ctx context.Context,
	handle *managedusage.Handle,
	plan managedUsageSettlementPlan,
) error {
	if handle == nil {
		return nil
	}
	settleCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		managedUsageSettlementTimeout,
	)
	defer cancel()

	var errs []error
	if err := handle.ReleaseStagesNotInvoked(settleCtx, plan.release); err != nil {
		errs = append(errs, err)
	}
	attempted := append(
		append([]managedusage.Stage(nil), plan.finalize...),
		plan.indeterminate...,
	)
	if err := handle.MarkStagesIndeterminate(settleCtx, attempted); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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
