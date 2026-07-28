// Package provider manages per-user indexing-model settings: text embedding
// and VLM choices used to derive searchable observations. The visual vector
// space stays fixed to CLIP until versioned index generations exist.
//
// SPEC §F8 — vendor adapter is in the Python worker; this package stores the
// user choice and exposes:
//
//   - List / Get / Set settings
//   - Test (probe the worker → confirm it works and record output dim)
//   - Validate embedding dim against the fixed schema and reject unsafe live switches
//
// Defaults: when no row exists, falls back to the worker process defaults.
package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/indexmeta"
	"github.com/PeterGuy326/mem/server/internal/queue"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
)

// Kind enumerates the provider categories.
const (
	KindEmbedding       = "embedding"
	KindVisualEmbedding = "visual_embedding"
	KindVLM             = "vlm"
)

// ValidKinds is the canonical ordered list.
var ValidKinds = []string{KindEmbedding, KindVLM}

// Setting is one row of provider_settings.
type Setting struct {
	UserID    uuid.UUID `json:"user_id"`
	Kind      string    `json:"kind"`
	Spec      string    `json:"spec"`          // "<vendor>:<model>"
	Dim       *int      `json:"dim,omitempty"` // embedding output dim (NULL for VLM)
	UpdatedAt time.Time `json:"updated_at"`
}

// Service is the provider settings service.
type Service struct {
	pool   *pgxpool.Pool
	worker probeWorker
	queue  *queue.Client // for tenant reindex enqueue after embedding provider changes
	log    *slog.Logger
}

// probeWorker is the subset of workerclient.Client used by provider probes.
// Keeping this small also lets the probe behavior be unit-tested without a
// live gRPC worker or real model credentials.
type probeWorker interface {
	Enabled() bool
	EmbedTextWith(context.Context, string, string) ([]float32, error)
	ProbeVLM(context.Context, string) (string, error)
}

// New constructs a provider Service.
func New(pool *pgxpool.Pool, worker *workerclient.Client, q *queue.Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, worker: worker, queue: q, log: log}
}

// Service sentinel errors.
var (
	ErrNotFound             = errors.New("provider setting not found")
	ErrEmbeddingDimConflict = errors.New("embedding dimension cannot be changed online")
	ErrEmbeddingGeneration  = errors.New("embedding model switch requires a staged index generation")
)

// List returns all settings for the user (one row per kind, possibly empty).
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Setting, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, kind, spec, dim, updated_at
		   FROM provider_settings
		  WHERE user_id = $1 AND kind IN ('embedding', 'vlm')
		  ORDER BY kind`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	var out []Setting
	for rows.Next() {
		var x Setting
		if err := rows.Scan(&x.UserID, &x.Kind, &x.Spec, &x.Dim, &x.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Get returns one setting (kind ∈ ValidKinds), or ErrNotFound.
func (s *Service) Get(ctx context.Context, userID uuid.UUID, kind string) (*Setting, error) {
	if !validKind(kind) {
		return nil, fmt.Errorf("invalid kind %q", kind)
	}
	var x Setting
	err := s.pool.QueryRow(ctx,
		`SELECT user_id, kind, spec, dim, updated_at
		   FROM provider_settings WHERE user_id = $1 AND kind = $2`,
		userID, kind,
	).Scan(&x.UserID, &x.Kind, &x.Spec, &x.Dim, &x.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &x, nil
}

// SetResult keeps the existing response envelope while provider switching is
// intentionally conservative. Reindex fields stay false until versioned index
// generations can rebuild and activate a corpus atomically.
type SetResult struct {
	Setting         Setting `json:"setting"`
	ReindexQueued   bool    `json:"reindex_queued"`
	ReindexFiles    int     `json:"reindex_files,omitempty"`
	ReindexRequired bool    `json:"reindex_required,omitempty"`
	PreviousDim     *int    `json:"previous_dim,omitempty"`
	DimMigrationOK  bool    `json:"dim_migration_ok"`
}

// Set persists a new provider spec. For text embedding providers it probes the
// output dim and rejects dimensions incompatible with the fixed table schema.
// Once a corpus has embeddings, changing vector space is refused until a
// versioned generation can be rebuilt and activated atomically.
//
// The dim probe happens BEFORE writing the row, so a broken provider spec or
// incompatible dimension fails without changing settings or shared data.
func (s *Service) Set(ctx context.Context, userID uuid.UUID, kind, spec string) (*SetResult, error) {
	if !validKind(kind) {
		return nil, fmt.Errorf("invalid kind %q", kind)
	}
	if spec == "" {
		return nil, fmt.Errorf("spec is empty")
	}

	res := &SetResult{}
	var newDim *int

	if kind == KindEmbedding {
		unlockSwitch := indexmeta.LockProviderSwitch(userID)
		defer unlockSwitch()

		prev, getErr := s.Get(ctx, userID, kind)
		if getErr != nil && !errors.Is(getErr, ErrNotFound) {
			return nil, getErr
		}
		if prev == nil || prev.Spec != spec {
			inFlight, err := s.hasIndexingInFlight(ctx, userID)
			if err != nil {
				return nil, fmt.Errorf("check in-flight indexing: %w", err)
			}
			if inFlight {
				return nil, fmt.Errorf(
					"%w: wait for pending/processing files before changing the embedding provider",
					ErrEmbeddingGeneration,
				)
			}
		}
		corpusProvider, hasCorpus, err := indexmeta.TextProvider(ctx, s.pool, userID)
		if err != nil {
			if errors.Is(err, indexmeta.ErrUnknownProvider) ||
				errors.Is(err, indexmeta.ErrMixedProviders) {
				res.ReindexRequired = true
			} else {
				return nil, fmt.Errorf("%w: %v", ErrEmbeddingGeneration, err)
			}
		} else if hasCorpus && corpusProvider != spec {
			return nil, ErrEmbeddingGeneration
		}

		// Probe the worker with the override active to learn the dim.
		dim, err := s.probeEmbeddingDim(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", spec, err)
		}
		newDim = &dim

		var prevDim *int
		if prev != nil {
			prevDim = prev.Dim
		}
		res.PreviousDim = prevDim

		schemaDim, err := s.embeddingSchemaDim(ctx)
		if err != nil {
			return nil, fmt.Errorf("read embedding schema dim: %w", err)
		}
		if err := checkEmbeddingDimension(dim, schemaDim); err != nil {
			return nil, err
		}
		res.DimMigrationOK = true
	}

	upd := time.Now().UTC()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO provider_settings (user_id, kind, spec, dim, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (user_id, kind) DO UPDATE
		   SET spec = EXCLUDED.spec, dim = EXCLUDED.dim, updated_at = EXCLUDED.updated_at`,
		userID, kind, spec, newDim, upd,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert: %w", err)
	}
	res.Setting = Setting{UserID: userID, Kind: kind, Spec: spec, Dim: newDim, UpdatedAt: upd}

	return res, nil
}

func (s *Service) hasIndexingInFlight(ctx context.Context, userID uuid.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM files
			 WHERE user_id = $1
			   AND index_status IN ('pending', 'processing')
		)`, userID).Scan(&exists)
	return exists, err
}

// Test probes the worker with the current (or supplied) spec and returns a
// short verification payload — used by the CLI/Web for "does my config
// actually work" buttons. A VLM probe makes one minimal real image request and
// may therefore incur a small amount of latency and provider usage.
func (s *Service) Test(ctx context.Context, userID uuid.UUID, kind, specOverride string) (any, error) {
	if !validKind(kind) {
		return nil, fmt.Errorf("invalid kind %q", kind)
	}
	spec := specOverride
	if spec == "" {
		cur, err := s.Get(ctx, userID, kind)
		if err != nil {
			return nil, err
		}
		spec = cur.Spec
	}
	switch kind {
	case KindEmbedding:
		dim, err := s.probeEmbeddingDim(ctx, spec)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": kind, "spec": spec, "dim": dim, "ok": true}, nil
	case KindVLM:
		started := time.Now()
		replyChars, err := s.probeVLMProvider(ctx, spec)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"kind":        kind,
			"spec":        spec,
			"ok":          true,
			"reply_chars": replyChars,
			"latency_ms":  time.Since(started).Milliseconds(),
		}, nil
	default:
		// A visual-embedding probe needs an image-aware worker RPC. Keep its
		// legacy behavior explicit instead of accidentally claiming that a
		// VLM probe covered it.
		return map[string]any{"kind": kind, "spec": spec, "ok": true,
			"note": "visual embedding probe not implemented yet; will exercise on next use"}, nil
	}
}

// --- internals ---

func validKind(k string) bool {
	for _, v := range ValidKinds {
		if v == k {
			return true
		}
	}
	return false
}

func checkEmbeddingDimension(providerDim, schemaDim int) error {
	if providerDim != schemaDim {
		return fmt.Errorf("%w: provider returned %d, schema requires %d; migrate offline and reindex explicitly", ErrEmbeddingDimConflict, providerDim, schemaDim)
	}
	return nil
}

// probeEmbeddingDim asks the worker to embed a 1-token sentinel using the
// supplied provider override and reports the output vector length.
func (s *Service) probeEmbeddingDim(ctx context.Context, spec string) (int, error) {
	if s.worker == nil || !s.worker.Enabled() {
		return 0, fmt.Errorf("worker not configured")
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	vec, err := s.worker.EmbedTextWith(cctx, "probe", spec)
	if err != nil {
		return 0, safeGenerativeProbeError(cctx, KindEmbedding, err)
	}
	if len(vec) == 0 {
		return 0, fmt.Errorf("worker returned empty vector")
	}
	return len(vec), nil
}

// probeVLMProvider uses a dedicated image Process probe so the selected model
// must successfully inspect an image and return a caption.
func (s *Service) probeVLMProvider(ctx context.Context, spec string) (int, error) {
	if err := validateProviderSpec(spec); err != nil {
		return 0, err
	}
	if s.worker == nil || !s.worker.Enabled() {
		return 0, fmt.Errorf("worker not configured")
	}

	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	reply, err := s.worker.ProbeVLM(cctx, spec)
	if err != nil {
		return 0, safeGenerativeProbeError(cctx, KindVLM, err)
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return 0, fmt.Errorf("%s provider probe returned an empty reply", KindVLM)
	}
	return utf8.RuneCountInString(reply), nil
}

func validateProviderSpec(spec string) error {
	vendor, model, ok := strings.Cut(spec, ":")
	if !ok || strings.TrimSpace(vendor) == "" || strings.TrimSpace(model) == "" {
		return fmt.Errorf("invalid provider spec: expected '<vendor>:<model>'")
	}
	return nil
}

func safeGenerativeProbeError(ctx context.Context, kind string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return fmt.Errorf("%s provider probe timed out", kind)
		}
		return ctxErr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s provider probe timed out", kind)
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	// The worker's provider errors may contain an upstream response body. Do
	// not reflect that body to the API caller: some gateways echo request
	// headers, including Authorization, in diagnostics.
	return fmt.Errorf("%s provider probe failed", kind)
}

// embeddingSchemaDim reads the table-level pgvector dimension without changing
// shared schema or another tenant's data.
func (s *Service) embeddingSchemaDim(ctx context.Context) (int, error) {
	var formatted string
	if err := s.pool.QueryRow(ctx, `SELECT format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		WHERE a.attrelid = 'embeddings_text'::regclass AND a.attname = 'embedding' AND NOT a.attisdropped`).Scan(&formatted); err != nil {
		return 0, err
	}
	var dim int
	if _, err := fmt.Sscanf(formatted, "vector(%d)", &dim); err != nil || dim <= 0 {
		return 0, fmt.Errorf("unexpected embedding column type %q", formatted)
	}
	return dim, nil
}

// queueReindexAll enqueues an IndexFile task for every file belonging to the
// user. Returns the count enqueued.
func (s *Service) queueReindexAll(ctx context.Context, userID uuid.UUID) (int, error) {
	if s.queue == nil || !s.queue.Enabled() {
		return 0, fmt.Errorf("queue not configured")
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM files WHERE user_id = $1`, userID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return n, err
		}
		if err := s.queue.EnqueueIndexFile(ctx, queue.IndexFilePayload{
			FileID: id, UserID: userID,
		}); err != nil {
			// Reindex is bulk + idempotent; a duplicate-task or transient
			// error on one file should NOT abort the rest. Log and continue.
			s.log.Warn("queue.reindex_enqueue_skipped", "file_id", id, "err", err)
			continue
		}
		n++
	}
	return n, rows.Err()
}
