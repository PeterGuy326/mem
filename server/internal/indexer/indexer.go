// Package indexer turns a freshly-uploaded file into AI-indexed rows.
//
// Flow:
//  1. Upload handler (api/handlePutFile) returns a `*file.File`
//  2. api spawns `go indexer.IndexFile(file)` — fire-and-forget
//  3. indexer flips `files.index_status` to 'processing'
//  4. indexer calls workerclient.Index → ProcessResponse
//  5. indexer writes summary / caption / tags back into `files`
//  6. indexer inserts chunked embeddings into `embeddings_text`
//  7. on any failure: index_status='failed', the error is logged
//
// Persistence is done in a single transaction so failures roll back cleanly.
package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
	"github.com/PeterGuy326/mem/server/internal/workerpb"
)

// Service is the indexer. Construct with New. Safe for concurrent use.
type Service struct {
	pool   *pgxpool.Pool
	client *workerclient.Client
	log    *slog.Logger
}

// New constructs an indexer Service. If client is nil or disabled, IndexFile
// becomes a no-op that just leaves index_status='pending'.
func New(pool *pgxpool.Pool, client *workerclient.Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, client: client, log: log}
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
		`SELECT id, user_id, name, path, folder_id, size, sha256, mime, storage_key,
		        summary, caption, tags, timeline_at, index_status, created_at, updated_at
		   FROM files WHERE id = $1 AND user_id = $2`,
		fileID, userID,
	).Scan(
		&f.ID, &f.UserID, &f.Name, &f.Path, &f.FolderID, &f.Size, &f.SHA256, &f.MIME,
		&f.StorageKey, &f.Summary, &f.Caption, &f.Tags, &f.TimelineAt, &f.IndexStatus,
		&f.CreatedAt, &f.UpdatedAt,
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

	if err := s.setStatus(ctx, f.ID, "processing"); err != nil {
		s.log.Error("indexer.set_processing", "file_id", f.ID, "err", err)
		return
	}

	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	embProv, llmProv, vlmProv := s.providerOverrides(ctx, f.UserID)
	resp, err := s.client.Index(rpcCtx, workerclient.FileMeta{
		FileID:            f.ID.String(),
		UserID:            f.UserID.String(),
		Name:              f.Name,
		MIME:              f.MIME,
		SHA256:            f.SHA256,
		StorageKey:        f.StorageKey,
		EmbeddingProvider: embProv,
		LLMProvider:       llmProv,
		VLMProvider:       vlmProv,
	})
	if err != nil {
		s.log.Error("indexer.worker_failed", "file_id", f.ID, "err", err)
		_ = s.setStatus(ctx, f.ID, "failed")
		return
	}
	if resp.Status == workerpb.ProcessStatus_STATUS_FAILED {
		s.log.Error("indexer.process_failed", "file_id", f.ID, "err", resp.Error)
		_ = s.setStatus(ctx, f.ID, "failed")
		return
	}
	if resp.Status == workerpb.ProcessStatus_STATUS_SKIPPED {
		s.log.Info("indexer.skipped", "file_id", f.ID, "reason", "unsupported_mime")
		_ = s.setStatus(ctx, f.ID, "done")
		return
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
}

// persist writes the worker output into PG in one transaction.
func (s *Service) persist(ctx context.Context, fileID uuid.UUID, resp *workerpb.ProcessResponse) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck — committed below on success

	summary := nullable(resp.Summary)
	caption := nullable(resp.Caption)
	tags := resp.Tags
	if tags == nil {
		tags = []string{}
	}

	_, err = tx.Exec(ctx,
		`UPDATE files
		   SET summary = $1,
		       caption = $2,
		       tags = (CASE WHEN cardinality($3::text[]) = 0 THEN tags ELSE $3 END),
		       index_status = 'done',
		       updated_at = now()
		 WHERE id = $4`,
		summary, caption, tags, fileID,
	)
	if err != nil {
		return fmt.Errorf("update files: %w", err)
	}

	// Wipe previous embeddings for idempotency (re-index replays).
	if _, err := tx.Exec(ctx, `DELETE FROM embeddings_text WHERE file_id = $1`, fileID); err != nil {
		return fmt.Errorf("delete old text embeddings: %w", err)
	}

	if text := resp.Embeddings["text"]; text != nil && len(text.Rows) > 0 {
		batch := &pgx.Batch{}
		for _, row := range text.Rows {
			batch.Queue(
				`INSERT INTO embeddings_text (file_id, chunk_index, chunk_text, embedding)
				 VALUES ($1, $2, $3, $4::vector)`,
				fileID, row.Index, row.ChunkText, vectorLiteral(row.Values),
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

	return tx.Commit(ctx)
}

// providerOverrides reads the user's saved provider_settings rows so the
// worker can be told which model to use for THIS user — important after a
// `mem provider set embedding ...` switches dims.
//
// Done as a separate package-internal query (not via provider.Service) to
// avoid an import cycle. Missing rows fall through to worker defaults.
func (s *Service) providerOverrides(ctx context.Context, userID uuid.UUID) (emb, llm, vlm string) {
	rows, err := s.pool.Query(ctx,
		`SELECT kind, spec FROM provider_settings WHERE user_id = $1`, userID)
	if err != nil {
		s.log.Warn("indexer.provider_lookup_failed", "user_id", userID, "err", err)
		return "", "", ""
	}
	defer rows.Close()
	for rows.Next() {
		var kind, spec string
		if err := rows.Scan(&kind, &spec); err != nil {
			continue
		}
		switch kind {
		case "embedding":
			emb = spec
		case "llm":
			llm = spec
		case "vlm":
			vlm = spec
		}
	}
	return
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

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func embeddingKinds(r *workerpb.ProcessResponse) []string {
	out := make([]string, 0, len(r.Embeddings))
	for k := range r.Embeddings {
		out = append(out, k)
	}
	return out
}
