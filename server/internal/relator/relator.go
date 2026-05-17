// Package relator computes file ↔ file relations via embedding KNN.
//
// SPEC §F4 — four relation types are defined; Phase 1 lands two:
//   * same_topic — derived from text embedding cosine similarity
//   * same_event — derived from visual embedding cosine similarity
//
// (same_person comes online with Phase G face clustering; sequel needs
// timeline + entity overlap heuristics, also future.)
//
// Relations are computed at indexing time for the *new* file only — the
// resulting (src_id, dst_id) rows are NOT mirrored to (dst_id, src_id).
// Bidirectional semantics emerge naturally as later uploads run their own
// KNN against the earlier ones. This keeps re-index idempotent (we wipe
// only the new file's outgoing rows on each run).
package relator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Relation types — keep strings stable; CHECK constraints and CLI flags depend.
const (
	TypeSameTopic  = "same_topic"
	TypeSameEvent  = "same_event"
	TypeSamePerson = "same_person" // Phase G
	TypeSequel     = "sequel"      // future
)

// Phase 1 default: how many neighbors to keep per (src, type).
const defaultTopK = 10

// Service is the relator.
type Service struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// New constructs a relator Service.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, log: log}
}

// ComputeForFile is called by the indexer once a file's embeddings are
// persisted. It refreshes file_relations rows for this file as src.
//
// Failures are non-fatal: we log + return error, but indexer treats them
// as soft (the file is still "done" even if relations were skipped).
func (s *Service) ComputeForFile(ctx context.Context, fileID uuid.UUID) error {
	userID, mime, err := s.fileMeta(ctx, fileID)
	if err != nil {
		return fmt.Errorf("load file meta: %w", err)
	}
	if err := s.recomputeText(ctx, fileID, userID, defaultTopK); err != nil {
		s.log.Warn("relator.text_failed", "file_id", fileID, "err", err)
	}
	// Visual route is only meaningful for image-like files (and only when
	// they have an embeddings_visual row).
	if isImageMIME(mime) {
		if err := s.recomputeVisual(ctx, fileID, userID, defaultTopK); err != nil {
			s.log.Warn("relator.visual_failed", "file_id", fileID, "err", err)
		}
	}
	return nil
}

// Hit is one related file returned by Get.
type Hit struct {
	FileID  uuid.UUID `json:"file_id"`
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	MIME    string    `json:"mime"`
	Type    string    `json:"type"`
	Score   float32   `json:"score"`
	Summary *string   `json:"summary,omitempty"`
}

// Get returns the top related files for srcID. filterType narrows by relation
// type when non-empty.
func (s *Service) Get(ctx context.Context, userID, srcID uuid.UUID, filterType string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = defaultTopK
	}
	if limit > 100 {
		limit = 100
	}
	args := []any{srcID, userID}
	where := []string{"r.src_id = $1", "f.user_id = $2", "f.id != $1"}
	if filterType != "" {
		args = append(args, filterType)
		where = append(where, fmt.Sprintf("r.type = $%d", len(args)))
	}
	args = append(args, limit)
	limitIdx := len(args)

	sql := `
		SELECT f.id, f.name, f.path, f.mime, r.type, r.score, f.summary
		  FROM file_relations r
		  JOIN files f ON f.id = r.dst_id
		 WHERE ` + joinAnd(where) + `
		 ORDER BY r.score DESC
		 LIMIT $` + fmt.Sprintf("%d", limitIdx)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	out := make([]Hit, 0, limit)
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.FileID, &h.Name, &h.Path, &h.MIME, &h.Type, &h.Score, &h.Summary); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// --- internals ---

func (s *Service) fileMeta(ctx context.Context, id uuid.UUID) (userID uuid.UUID, mime string, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT user_id, mime FROM files WHERE id = $1`, id,
	).Scan(&userID, &mime)
	return
}

// recomputeText finds the top-K text-embedding nearest neighbors for srcID
// (within the same user) and rewrites file_relations rows of type same_topic.
//
// Strategy: take the first chunk of src as the seed; ANN against ALL chunks
// of OTHER files, DISTINCT ON dst file (best chunk wins).
func (s *Service) recomputeText(ctx context.Context, srcID, userID uuid.UUID, topK int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`DELETE FROM file_relations WHERE src_id = $1 AND type = $2`,
		srcID, TypeSameTopic,
	); err != nil {
		return fmt.Errorf("clear: %w", err)
	}

	rows, err := tx.Query(ctx, `
		WITH seed AS (
		  SELECT embedding FROM embeddings_text
		   WHERE file_id = $1 AND chunk_index = 0
		   LIMIT 1
		)
		SELECT DISTINCT ON (e.file_id)
		       e.file_id,
		       (1 - (e.embedding <=> (SELECT embedding FROM seed)))::real AS score
		  FROM embeddings_text e
		  JOIN files f ON f.id = e.file_id
		 WHERE f.user_id = $2
		   AND e.file_id != $1
		   AND (SELECT embedding FROM seed) IS NOT NULL
		 ORDER BY e.file_id, e.embedding <=> (SELECT embedding FROM seed) ASC
		 LIMIT $3
	`, srcID, userID, topK)
	if err != nil {
		return fmt.Errorf("knn: %w", err)
	}
	defer rows.Close()

	batch := &pgx.Batch{}
	count := 0
	for rows.Next() {
		var dstID uuid.UUID
		var score float32
		if err := rows.Scan(&dstID, &score); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		batch.Queue(`
			INSERT INTO file_relations (src_id, dst_id, type, score, computed_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (src_id, dst_id, type)
			  DO UPDATE SET score = EXCLUDED.score, computed_at = EXCLUDED.computed_at
		`, srcID, dstID, TypeSameTopic, score)
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count > 0 {
		br := tx.SendBatch(ctx, batch)
		for i := 0; i < count; i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return fmt.Errorf("insert: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) recomputeVisual(ctx context.Context, srcID, userID uuid.UUID, topK int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`DELETE FROM file_relations WHERE src_id = $1 AND type = $2`,
		srcID, TypeSameEvent,
	); err != nil {
		return err
	}

	rows, err := tx.Query(ctx, `
		WITH seed AS (
		  SELECT embedding FROM embeddings_visual WHERE file_id = $1 LIMIT 1
		)
		SELECT e.file_id,
		       (1 - (e.embedding <=> (SELECT embedding FROM seed)))::real AS score
		  FROM embeddings_visual e
		  JOIN files f ON f.id = e.file_id
		 WHERE f.user_id = $2
		   AND e.file_id != $1
		   AND (SELECT embedding FROM seed) IS NOT NULL
		 ORDER BY e.embedding <=> (SELECT embedding FROM seed) ASC
		 LIMIT $3
	`, srcID, userID, topK)
	if err != nil {
		return err
	}
	defer rows.Close()

	batch := &pgx.Batch{}
	count := 0
	for rows.Next() {
		var dstID uuid.UUID
		var score float32
		if err := rows.Scan(&dstID, &score); err != nil {
			return err
		}
		batch.Queue(`
			INSERT INTO file_relations (src_id, dst_id, type, score, computed_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (src_id, dst_id, type)
			  DO UPDATE SET score = EXCLUDED.score, computed_at = EXCLUDED.computed_at
		`, srcID, dstID, TypeSameEvent, score)
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count > 0 {
		br := tx.SendBatch(ctx, batch)
		for i := 0; i < count; i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func isImageMIME(mime string) bool {
	return len(mime) >= 6 && mime[:6] == "image/"
}

func joinAnd(xs []string) string {
	out := ""
	for i, s := range xs {
		if i > 0 {
			out += " AND "
		}
		out += s
	}
	return out
}
