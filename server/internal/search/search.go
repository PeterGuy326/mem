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
	"sort"
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

// Route picks which embedding space to search against.
//
//	"text"   -> ANN over embeddings_text (Ollama / OpenAI text embedder)
//	"visual" -> ANN over embeddings_visual via CLIP text encoder
//	"auto"   -> both routes in parallel, merged + deduped by file_id (default)
const (
	RouteText   = "text"
	RouteVisual = "visual"
	RouteAuto   = "auto"
)

// Hit is one search result.
type Hit struct {
	FileID     uuid.UUID  `json:"file_id"`
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	MIME       string     `json:"mime"`
	Score      float32    `json:"score"`           // 1 - cosine_distance, in [-1, 1]
	Snippet    string     `json:"snippet"`         // best matching chunk (text route) or caption (visual)
	Source     string     `json:"source"`          // "text" | "visual" — which route produced this hit
	Summary    *string    `json:"summary,omitempty"`
	TimelineAt *time.Time `json:"timeline_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Query is the request shape.
type Query struct {
	UserID uuid.UUID
	Text   string
	Route  string // "" defaults to RouteAuto
	Type   string // mime prefix filter, e.g. "image" => "image/%"
	Since  *time.Time
	Until  *time.Time
	Limit  int
}

// Search dispatches by route. Returns hits sorted by score descending,
// truncated to q.Limit.
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

	switch q.Route {
	case RouteText:
		return s.searchText(ctx, q, text)
	case RouteVisual:
		return s.searchVisual(ctx, q, text)
	case "", RouteAuto:
		return s.searchAuto(ctx, q, text)
	default:
		return nil, fmt.Errorf("unknown route %q (expected text|visual|auto)", q.Route)
	}
}

// searchText runs the original text-embedding ANN route.
func (s *Service) searchText(ctx context.Context, q Query, text string) ([]Hit, error) {
	embSpec := s.userEmbeddingSpec(ctx, q.UserID)
	embCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	vec, err := s.worker.EmbedTextWith(embCtx, text, embSpec)
	if err != nil {
		return nil, fmt.Errorf("embed query (text): %w", err)
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("empty text embedding")
	}
	return s.runTextANN(ctx, q, vec)
}

// searchVisual encodes the query via the CLIP text tower and ANN-matches it
// against embeddings_visual (which holds CLIP image-tower vectors).
func (s *Service) searchVisual(ctx context.Context, q Query, text string) ([]Hit, error) {
	// Visual embedding provider: SPEC §9.4 default is "clip:ViT-B-32".
	// We force CLIP for queries here — using e.g. nomic-embed-text for a
	// "visual" search would land in a different latent space than the
	// indexed images, producing meaningless results.
	const clipSpec = "clip:ViT-B-32"
	embCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	vec, err := s.worker.EmbedTextWith(embCtx, text, clipSpec)
	if err != nil {
		return nil, fmt.Errorf("embed query (visual/clip): %w", err)
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("empty visual embedding")
	}
	return s.runVisualANN(ctx, q, vec)
}

// searchAuto runs text + visual in parallel, merges by file_id keeping the
// best-scoring row per file, re-sorts by score.
func (s *Service) searchAuto(ctx context.Context, q Query, text string) ([]Hit, error) {
	type result struct {
		hits []Hit
		err  error
	}
	textCh := make(chan result, 1)
	visualCh := make(chan result, 1)

	go func() {
		hits, err := s.searchText(ctx, q, text)
		textCh <- result{hits, err}
	}()
	go func() {
		hits, err := s.searchVisual(ctx, q, text)
		visualCh <- result{hits, err}
	}()
	tr := <-textCh
	vr := <-visualCh

	// Tolerate one-route failures (e.g. no visual embeddings exist yet).
	if tr.err != nil && vr.err != nil {
		return nil, fmt.Errorf("both routes failed: text=%v; visual=%v", tr.err, vr.err)
	}

	// Merge by file_id, keep max score.
	best := map[uuid.UUID]Hit{}
	for _, h := range tr.hits {
		if cur, ok := best[h.FileID]; !ok || h.Score > cur.Score {
			best[h.FileID] = h
		}
	}
	for _, h := range vr.hits {
		if cur, ok := best[h.FileID]; !ok || h.Score > cur.Score {
			best[h.FileID] = h
		}
	}
	out := make([]Hit, 0, len(best))
	for _, h := range best {
		out = append(out, h)
	}
	// Sort by score desc; stable across runs even when scores tie.
	sortHitsByScoreDesc(out)
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// runTextANN issues the text-route SQL and scans results.
func (s *Service) runTextANN(ctx context.Context, q Query, vec []float32) ([]Hit, error) {
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

	return s.scanHits(ctx, sql, args, RouteText)
}

// runVisualANN issues the visual-route SQL.
func (s *Service) runVisualANN(ctx context.Context, q Query, vec []float32) ([]Hit, error) {
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

	// One row per file in embeddings_visual — no DISTINCT ON needed.
	// The "snippet" for visual hits is the file caption (if any).
	sql := fmt.Sprintf(`
		SELECT f.id, f.name, f.path, f.mime,
		       (1 - (e.embedding <=> $1::vector))::real AS score,
		       COALESCE(f.caption, '') AS snippet,
		       f.summary, f.timeline_at, f.created_at
		  FROM embeddings_visual e
		  JOIN files f ON f.id = e.file_id
		 WHERE %s
		 ORDER BY e.embedding <=> $1::vector ASC
		 LIMIT $%d
	`, strings.Join(where, " AND "), limitIdx)

	return s.scanHits(ctx, sql, args, RouteVisual)
}

// scanHits is the common cursor → []Hit loop. Tags every hit with its source route.
func (s *Service) scanHits(ctx context.Context, sql string, args []any, route string) ([]Hit, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search query (%s): %w", route, err)
	}
	defer rows.Close()

	out := make([]Hit, 0, 16)
	for rows.Next() {
		var h Hit
		if err := rows.Scan(
			&h.FileID, &h.Name, &h.Path, &h.MIME,
			&h.Score, &h.Snippet, &h.Summary, &h.TimelineAt, &h.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan hit: %w", err)
		}
		if len(h.Snippet) > 240 {
			h.Snippet = h.Snippet[:237] + "..."
		}
		h.Source = route
		out = append(out, h)
	}
	return out, rows.Err()
}

// sortHitsByScoreDesc — small enough corpus that sort.Slice is fine.
func sortHitsByScoreDesc(hits []Hit) {
	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].Score > hits[j].Score
	})
}

// userEmbeddingSpec reads the user's saved embedding provider. Empty string
// means "fall back to worker default" (which then matches the original
// indexing run).
func (s *Service) userEmbeddingSpec(ctx context.Context, userID uuid.UUID) string {
	var spec string
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM provider_settings WHERE user_id = $1 AND kind = 'embedding'`,
		userID,
	).Scan(&spec)
	if err != nil {
		return ""
	}
	return spec
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
