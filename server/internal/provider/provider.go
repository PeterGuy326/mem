// Package provider manages per-user model provider settings: which embedding /
// LLM / VLM vendor + model to use for indexing and search.
//
// SPEC §F8 — vendor adapter is in the Python worker; this package stores the
// user choice and exposes:
//
//   - List / Get / Set settings
//   - Test (probe the worker → confirm it works and record output dim)
//   - On embedding-dim change: schedule a full reindex via the queue
//
// Defaults: when no row exists, falls back to the worker process defaults
// (MEM_DEFAULT_EMBEDDING / _LLM / _VLM).
package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/queue"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
)

// Kind enumerates the provider categories.
const (
	KindEmbedding = "embedding"
	KindLLM       = "llm"
	KindVLM       = "vlm"
)

// ValidKinds is the canonical ordered list.
var ValidKinds = []string{KindEmbedding, KindLLM, KindVLM}

// Setting is one row of provider_settings.
type Setting struct {
	UserID    uuid.UUID `json:"user_id"`
	Kind      string    `json:"kind"`
	Spec      string    `json:"spec"`             // "<vendor>:<model>"
	Dim       *int      `json:"dim,omitempty"`    // embedding output dim (NULL for LLM/VLM)
	UpdatedAt time.Time `json:"updated_at"`
}

// Service is the provider settings service.
type Service struct {
	pool   *pgxpool.Pool
	worker *workerclient.Client
	queue  *queue.Client // for reindex enqueue on dim change
	log    *slog.Logger
}

// New constructs a provider Service.
func New(pool *pgxpool.Pool, worker *workerclient.Client, q *queue.Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, worker: worker, queue: q, log: log}
}

// ErrNotFound is returned by Get when no row exists.
var ErrNotFound = errors.New("provider setting not found")

// List returns all settings for the user (one row per kind, possibly empty).
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Setting, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, kind, spec, dim, updated_at
		   FROM provider_settings WHERE user_id = $1 ORDER BY kind`,
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

// SetResult is returned by Set; it surfaces whether a dim change triggered a
// reindex so callers (CLI / Web) can show progress.
type SetResult struct {
	Setting        Setting `json:"setting"`
	ReindexQueued  bool    `json:"reindex_queued"`
	ReindexFiles   int     `json:"reindex_files,omitempty"`
	PreviousDim    *int    `json:"previous_dim,omitempty"`
	DimMigrationOK bool    `json:"dim_migration_ok"`
}

// Set persists a new provider spec. For embedding providers it also probes
// the worker for output dim, runs the schema migration if dim changed, and
// enqueues a full reindex.
//
// The dim probe happens BEFORE writing the row, so a broken provider spec
// fails fast and we don't leave inconsistent state behind.
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
		// Probe the worker with the override active to learn the dim.
		dim, err := s.probeEmbeddingDim(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", spec, err)
		}
		newDim = &dim

		prev, _ := s.Get(ctx, userID, kind)
		var prevDim *int
		if prev != nil {
			prevDim = prev.Dim
		}
		res.PreviousDim = prevDim

		// If dim changed, run schema migration first — then upsert + enqueue
		// reindex. We do this before writing the row so a failed migration
		// leaves the old setting effective.
		needsMigration := prevDim == nil || (newDim != nil && *prevDim != *newDim)
		if needsMigration {
			if err := s.migrateEmbeddingDim(ctx, dim); err != nil {
				return nil, fmt.Errorf("migrate dim %d: %w", dim, err)
			}
			res.DimMigrationOK = true
		}
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

	if kind == KindEmbedding && res.DimMigrationOK {
		count, err := s.queueReindexAll(ctx, userID)
		if err != nil {
			// Best effort: log but don't fail the Set. User can `mem reindex` manually.
			s.log.Error("queue.reindex_all_failed", "err", err)
		} else {
			res.ReindexQueued = true
			res.ReindexFiles = count
		}
	}
	return res, nil
}

// Test probes the worker with the current (or supplied) spec and returns a
// short verification payload — used by the CLI/Web for "does my config
// actually work" buttons.
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
	if kind == KindEmbedding {
		dim, err := s.probeEmbeddingDim(ctx, spec)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": kind, "spec": spec, "dim": dim, "ok": true}, nil
	}
	// For llm / vlm we don't have a probe wired yet (W4). Return a soft OK.
	return map[string]any{"kind": kind, "spec": spec, "ok": true,
		"note": "non-embedding probe not implemented yet; will exercise on next use"}, nil
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
		return 0, err
	}
	if len(vec) == 0 {
		return 0, fmt.Errorf("worker returned empty vector")
	}
	return len(vec), nil
}

// migrateEmbeddingDim ALTERs embeddings_text.embedding to the new dim. Existing
// rows are dropped (they would be wrong-shape).
func (s *Service) migrateEmbeddingDim(ctx context.Context, dim int) error {
	if dim <= 0 || dim > 16000 {
		return fmt.Errorf("refusing dim=%d (out of sane range)", dim)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 1. Wipe old embeddings — wrong shape after re-type.
	if _, err := tx.Exec(ctx, `TRUNCATE embeddings_text`); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	// 2. Re-type the column. pgvector allows ALTER TYPE when table is empty.
	if _, err := tx.Exec(ctx,
		fmt.Sprintf(`ALTER TABLE embeddings_text ALTER COLUMN embedding TYPE vector(%d)`, dim),
	); err != nil {
		return fmt.Errorf("alter: %w", err)
	}
	// 3. Mark all files as pending so callers see reindex progress.
	if _, err := tx.Exec(ctx, `UPDATE files SET index_status = 'pending'`); err != nil {
		return fmt.Errorf("reset status: %w", err)
	}
	return tx.Commit(ctx)
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
