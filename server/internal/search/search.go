// Package search implements natural-language file retrieval over the
// embeddings stored in PostgreSQL/pgvector.
//
// Phase C scope (W3 partial):
//   - Text route: embed query via worker → cosine-distance ANN over
//     embeddings_text → dedupe to one row per file → join files for metadata.
//   - Visual route + multi-route rerank are NOT wired yet (SPEC F3.3).
//
// Filters supported: user_id (required), mime prefix, since/until on
// timeline_at (or created_at fallback), limit.
package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/workerclient"
)

// Service is the search service.
type Service struct {
	pool   *pgxpool.Pool
	worker *workerclient.Client
}

// New constructs a search Service.
func New(pool *pgxpool.Pool, worker *workerclient.Client) *Service {
	return &Service{pool: pool, worker: worker}
}

// Hit is one search result.
type Hit struct {
	FileID     uuid.UUID  `json:"file_id"`
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	MIME       string     `json:"mime"`
	Score      float32    `json:"score"`   // 1 - cosine_distance, in [-1, 1]; ~1 is great
	Snippet    string     `json:"snippet"` // best matching chunk
	Summary    *string    `json:"summary,omitempty"`
	TimelineAt *time.Time `json:"timeline_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Query is the request shape.
type Query struct {
	UserID uuid.UUID
	Text   string
	Type   string // mime prefix filter, e.g. "image" => "image/%"
	Since  *time.Time
	Until  *time.Time
	Limit  int
}

// Search runs the text route end-to-end.
func (s *Service) Search(ctx context.Context, q Query) ([]Hit, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, fmt.Errorf("query is empty")
	}
	if q.UserID == uuid.Nil {
		return nil, fmt.Errorf("user_id required")
	}
	if q.Limit <= 0 {
		q.Limit = 10
	}
	if q.Limit > 100 {
		q.Limit = 100
	}

	if s.worker == nil || !s.worker.Enabled() {
		return nil, fmt.Errorf("search disabled: worker not configured")
	}

	embCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	vec, err := s.worker.EmbedText(embCtx, text)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("embed query: empty vector")
	}

	// Build SQL with optional filters.
	args := []any{vectorLiteral(vec), q.UserID}
	where := []string{"f.user_id = $2"}

	if q.Type != "" {
		args = append(args, q.Type+"/%")
		where = append(where, fmt.Sprintf("f.mime LIKE $%d", len(args)))
	}
	if q.Since != nil {
		args = append(args, *q.Since)
		where = append(where, fmt.Sprintf("COALESCE(f.timeline_at, f.created_at) >= $%d", len(args)))
	}
	if q.Until != nil {
		args = append(args, *q.Until)
		where = append(where, fmt.Sprintf("COALESCE(f.timeline_at, f.created_at) <= $%d", len(args)))
	}
	args = append(args, q.Limit)
	limitIdx := len(args)

	// DISTINCT ON keeps one row per file: the best-scoring chunk.
	// Outer ORDER BY re-sorts by score after dedup.
	sql := fmt.Sprintf(`
		SELECT file_id, name, path, mime, score, snippet, summary, timeline_at, created_at
		FROM (
		  SELECT DISTINCT ON (f.id)
		    f.id          AS file_id,
		    f.name        AS name,
		    f.path        AS path,
		    f.mime        AS mime,
		    1 - (e.embedding <=> $1::vector) AS score,
		    e.chunk_text  AS snippet,
		    f.summary     AS summary,
		    f.timeline_at AS timeline_at,
		    f.created_at  AS created_at
		  FROM embeddings_text e
		  JOIN files f ON f.id = e.file_id
		  WHERE %s
		  ORDER BY f.id, e.embedding <=> $1::vector ASC
		) hits
		ORDER BY score DESC
		LIMIT $%d
	`, strings.Join(where, " AND "), limitIdx)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	out := make([]Hit, 0, q.Limit)
	for rows.Next() {
		var h Hit
		if err := rows.Scan(
			&h.FileID, &h.Name, &h.Path, &h.MIME,
			&h.Score, &h.Snippet, &h.Summary, &h.TimelineAt, &h.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan hit: %w", err)
		}
		// Truncate long snippets for readability.
		if len(h.Snippet) > 240 {
			h.Snippet = h.Snippet[:237] + "..."
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// vectorLiteral matches indexer.vectorLiteral — kept private here to avoid an
// import cycle and to keep search's wire format owned.
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
		fmt.Fprintf(&b, "%g", v)
	}
	b.WriteByte(']')
	return b.String()
}
