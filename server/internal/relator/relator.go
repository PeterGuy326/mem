// Package relator computes file ↔ file relations via embedding KNN
// and entity overlap.
//
// SPEC §F4 — four relation types are defined; Phase 1 lands three:
//   - same_topic  — text embedding cosine similarity
//   - same_event  — visual embedding cosine similarity (images only)
//   - same_person — shared `person` entities from face clustering / NER
//
// (sequel needs timeline + narrative heuristics, still future.)
//
// Relations are computed at indexing time for the *new* file only — the
// resulting (src_id, dst_id) rows are NOT mirrored to (dst_id, src_id).
// Bidirectional semantics emerge naturally as later uploads run their own
// pass against the earlier ones. This keeps re-index idempotent (we wipe
// only the new file's outgoing rows on each run). RebuildForUser exists
// for backfilling historical data after new relation types come online.
package relator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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
	if err := s.recomputePerson(ctx, fileID, userID, defaultTopK); err != nil {
		s.log.Warn("relator.person_failed", "file_id", fileID, "err", err)
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
func (s *Service) Get(
	ctx context.Context,
	userID, srcID uuid.UUID,
	filterType string,
	allowedPaths []string,
	limit int,
) ([]Hit, error) {
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
	restricted := true
	for _, p := range allowedPaths {
		if p == "/" {
			restricted = false
			break
		}
	}
	if len(allowedPaths) == 0 {
		restricted = false
	}
	if restricted {
		clauses := make([]string, 0, len(allowedPaths))
		for _, p := range allowedPaths {
			if p == "" {
				continue
			}
			args = append(args, p)
			n := len(args)
			clauses = append(clauses, fmt.Sprintf(
				"(f.path = $%d OR left(f.path, length($%d) + 1) = $%d || '/')",
				n, n, n,
			))
		}
		if len(clauses) > 0 {
			where = append(where, "("+strings.Join(clauses, " OR ")+")")
		} else {
			where = append(where, "FALSE")
		}
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

// recomputePerson finds files that share at least one `person`-typed entity
// with srcID. score = shared_count / src_person_count, giving a value in
// (0, 1] — 1 means the destination contains every person attached to the
// source. The relation is directional, so a partial destination ranks lower.
// Top-K ties are broken by shared_count and then file ID.
//
// If the src file has zero person entities we still wipe old rows (idempotent
// rebuild) but insert nothing.
func (s *Service) recomputePerson(ctx context.Context, srcID, userID uuid.UUID, topK int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`DELETE FROM file_relations WHERE src_id = $1 AND type = $2`,
		srcID, TypeSamePerson,
	); err != nil {
		return fmt.Errorf("clear: %w", err)
	}

	rows, err := tx.Query(ctx, `
		WITH src_persons AS (
		  SELECT fe.entity_id
		    FROM file_entities fe
		    JOIN entities e ON e.id = fe.entity_id
		   WHERE fe.file_id = $1
		     AND e.user_id  = $2
		     AND e.type     = 'person'
		),
		src_count AS (
		  SELECT COUNT(*)::int AS n FROM src_persons
		),
		dst_shared AS (
		  SELECT fe.file_id, COUNT(*)::int AS shared
		    FROM file_entities fe
		    JOIN files f ON f.id = fe.file_id
		   WHERE fe.entity_id IN (SELECT entity_id FROM src_persons)
		     AND fe.file_id != $1
		     AND f.user_id  = $2
		   GROUP BY fe.file_id
		)
		SELECT d.file_id,
		       (d.shared::real / GREATEST((SELECT n FROM src_count), 1))::real AS score,
		       d.shared
		  FROM dst_shared d
		 WHERE (SELECT n FROM src_count) > 0
		 ORDER BY score DESC, d.shared DESC, d.file_id
		 LIMIT $3
	`, srcID, userID, topK)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	batch := &pgx.Batch{}
	count := 0
	for rows.Next() {
		var dstID uuid.UUID
		var score float32
		var shared int
		if err := rows.Scan(&dstID, &score, &shared); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		batch.Queue(`
			INSERT INTO file_relations (src_id, dst_id, type, score, computed_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (src_id, dst_id, type)
			  DO UPDATE SET score = EXCLUDED.score, computed_at = EXCLUDED.computed_at
		`, srcID, dstID, TypeSamePerson, score)
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

// RebuildResult reports what a backfill pass touched.
type RebuildResult struct {
	Files    int `json:"files"`
	Failures int `json:"failures"`
}

// RebuildForUser recomputes relations for every file owned by userID. Used
// after a new relation type comes online (e.g. same_person) so historical
// uploads pick it up without re-ingesting the file bytes. Optionally scoped
// to a single fileID.
//
// Failures per file are counted but not fatal — the pass returns a summary
// so callers can decide whether to alert.
func (s *Service) RebuildForUser(ctx context.Context, userID uuid.UUID, onlyFileID *uuid.UUID) (RebuildResult, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if onlyFileID != nil {
		rows, err = s.pool.Query(ctx,
			`SELECT id FROM files WHERE user_id = $1 AND id = $2`,
			userID, *onlyFileID)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id FROM files WHERE user_id = $1 ORDER BY created_at ASC`,
			userID)
	}
	if err != nil {
		return RebuildResult{}, fmt.Errorf("list files: %w", err)
	}
	defer rows.Close()

	ids := make([]uuid.UUID, 0, 64)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return RebuildResult{}, fmt.Errorf("scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return RebuildResult{}, err
	}

	res := RebuildResult{Files: len(ids)}
	for _, id := range ids {
		if err := s.ComputeForFile(ctx, id); err != nil {
			s.log.Warn("relator.rebuild_file_failed", "file_id", id, "err", err)
			res.Failures++
		}
	}
	return res, nil
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
