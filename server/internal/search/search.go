// Package search implements natural-language file retrieval over the
// embeddings stored in PostgreSQL/pgvector.
//
// Routes (SPEC F3.3):
//   - Text route: embed query via worker → cosine-distance ANN over
//     embeddings_text → dedupe to one row per file → join files for metadata.
//   - Visual route: embed query via the CLIP text tower → ANN over
//     embeddings_visual (CLIP image-tower vectors written at index time).
//   - Auto route (default): run text + visual in parallel, merge by file_id
//     keeping the best-scoring row per file, re-sort by score.
//
// Filters supported: user_id (required), mime prefix, since/until on
// timeline_at (or created_at fallback), limit.
package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/indexmeta"
	"github.com/PeterGuy326/mem/server/internal/modeltext"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
)

type textEmbedder interface {
	Enabled() bool
	EmbedTextWith(context.Context, string, string) ([]float32, error)
}

// Service is the search service.
type Service struct {
	pool                     *pgxpool.Pool
	worker                   textEmbedder
	defaultEmbeddingProvider string
}

// New constructs a search Service.
func New(
	pool *pgxpool.Pool,
	worker *workerclient.Client,
	defaultEmbeddingProvider ...string,
) *Service {
	s := &Service{pool: pool, worker: worker}
	if len(defaultEmbeddingProvider) > 0 {
		s.defaultEmbeddingProvider = strings.TrimSpace(defaultEmbeddingProvider[0])
	}
	return s
}

// ErrReplayReferenceUnavailable means an idempotent result can no longer be
// reconstructed under the caller's current workspace/path authorization. It
// is intentionally indistinguishable between deletion and lost access.
var ErrReplayReferenceUnavailable = errors.New("managed embedding replay reference unavailable")

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
	EvidenceID    string     `json:"evidence_id"`
	FileID        uuid.UUID  `json:"file_id"`
	Name          string     `json:"name"`
	Path          string     `json:"path"`
	MIME          string     `json:"mime"`
	ContentSHA256 string     `json:"content_sha256"`
	ChunkIndex    int        `json:"chunk_index"` // -1 for whole-file visual evidence
	Score         float32    `json:"score"`       // 1 - cosine_distance, in [-1, 1]
	Snippet       string     `json:"snippet"`     // best matching chunk (text route) or caption (visual)
	Source        string     `json:"source"`      // "text" | "visual" — which route produced this hit
	Summary       *string    `json:"summary,omitempty"`
	TimelineAt    *time.Time `json:"timeline_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// Query is the request shape.
type Query struct {
	UserID uuid.UUID
	Text   string
	Route  string // "" defaults to RouteAuto
	// EmbeddingProvider pins a server-resolved text provider for this request.
	// Hosted entitlement classification and provider invocation must use the
	// same exact spec, even if settings change concurrently.
	EmbeddingProvider string
	// RequireText makes auto retrieval fail closed when its primary text route
	// fails. Agent context packs enable this so a provider/index fault cannot
	// be misreported as "no memory". The visual route remains optional because
	// supported text-only deployments do not install CLIP.
	RequireText  bool
	Type         string // mime prefix filter, e.g. "image" => "image/%"
	PathPrefix   string // optional requested virtual-folder scope
	AllowedPaths []string
	Since        *time.Time
	Until        *time.Time
	Limit        int
	SnippetChars int // default 240; context packs request a larger evidence window
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
	if q.SnippetChars <= 0 {
		q.SnippetChars = 240
	}
	if q.SnippetChars > 16_000 {
		q.SnippetChars = 16_000
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

// EmbeddingSpec resolves the exact provider that Search will use for the text
// route. Hosted policy uses this server-side value; clients cannot self-report
// whether a call is managed.
func (s *Service) EmbeddingSpec(ctx context.Context, userID uuid.UUID) (string, error) {
	return s.userEmbeddingSpec(ctx, userID)
}

// ReplayReferences converts search hits into the normalized, content-free
// identifiers persisted by the entitlement service.
func ReplayReferences(hits []Hit) ([]entitlement.ReplayReference, error) {
	refs := make([]entitlement.ReplayReference, 0, len(hits))
	for _, hit := range hits {
		var evidenceID uuid.UUID
		var err error
		switch hit.Source {
		case RouteText:
			evidenceID, err = uuid.Parse(hit.EvidenceID)
		case RouteVisual:
			evidenceID = hit.FileID
		default:
			err = fmt.Errorf("unsupported replay source %q", hit.Source)
		}
		if err != nil || evidenceID == uuid.Nil || hit.FileID == uuid.Nil {
			return nil, fmt.Errorf("build replay reference: %w", ErrReplayReferenceUnavailable)
		}
		refs = append(refs, entitlement.ReplayReference{
			Source:     hit.Source,
			EvidenceID: evidenceID,
			FileID:     hit.FileID,
			Score:      hit.Score,
		})
	}
	return refs, nil
}

// Replay reconstructs a successful search from safe identifiers without
// invoking an embedding provider. Missing/deleted/out-of-scope references use
// one stable error and never fall back to a fresh provider call.
func (s *Service) Replay(
	ctx context.Context,
	q Query,
	refs []entitlement.ReplayReference,
) ([]Hit, error) {
	if q.UserID == uuid.Nil {
		return nil, fmt.Errorf("user_id required")
	}
	if q.SnippetChars <= 0 {
		q.SnippetChars = 240
	}
	if q.SnippetChars > 16_000 {
		q.SnippetChars = 16_000
	}
	out := make([]Hit, 0, len(refs))
	for _, ref := range refs {
		var (
			hit Hit
			err error
		)
		switch ref.Source {
		case RouteText:
			hit, err = s.rehydrateText(ctx, q, ref)
		case RouteVisual:
			hit, err = s.rehydrateVisual(ctx, q, ref)
		default:
			err = ErrReplayReferenceUnavailable
		}
		if err != nil {
			return nil, ErrReplayReferenceUnavailable
		}
		out = append(out, hit)
	}
	return out, nil
}

func (s *Service) rehydrateText(
	ctx context.Context,
	q Query,
	ref entitlement.ReplayReference,
) (Hit, error) {
	args := []any{ref.EvidenceID, q.UserID}
	where := []string{"e.id = $1", "f.user_id = $2"}
	args, where = appendPathFilters(args, where, q.PathPrefix, q.AllowedPaths)
	args, where = appendMIMEFilter(args, where, q.Type)
	args, where = appendTimeFilters(args, where, q.Since, q.Until)
	sql := fmt.Sprintf(`
		SELECT e.id::text, f.id, f.name, f.path, f.mime, f.sha256,
		       e.chunk_index, e.chunk_text, f.summary, f.timeline_at, f.created_at
		  FROM embeddings_text e
		  JOIN files f ON f.id = e.file_id
		 WHERE %s
	`, strings.Join(where, " AND "))
	var hit Hit
	err := s.pool.QueryRow(ctx, sql, args...).Scan(
		&hit.EvidenceID,
		&hit.FileID,
		&hit.Name,
		&hit.Path,
		&hit.MIME,
		&hit.ContentSHA256,
		&hit.ChunkIndex,
		&hit.Snippet,
		&hit.Summary,
		&hit.TimelineAt,
		&hit.CreatedAt,
	)
	if err != nil || hit.FileID != ref.FileID {
		return Hit{}, ErrReplayReferenceUnavailable
	}
	hit.Score = ref.Score
	hit.Source = RouteText
	hit.Snippet = truncateRunes(hit.Snippet, q.SnippetChars)
	return hit, nil
}

func (s *Service) rehydrateVisual(
	ctx context.Context,
	q Query,
	ref entitlement.ReplayReference,
) (Hit, error) {
	args := []any{ref.FileID, q.UserID}
	where := []string{"f.id = $1", "f.user_id = $2"}
	args, where = appendPathFilters(args, where, q.PathPrefix, q.AllowedPaths)
	args, where = appendMIMEFilter(args, where, q.Type)
	args, where = appendTimeFilters(args, where, q.Since, q.Until)
	sql := fmt.Sprintf(`
		SELECT 'visual:' || f.id::text, f.id, f.name, f.path, f.mime, f.sha256,
		       -1, COALESCE(f.caption, ''), f.summary, f.timeline_at, f.created_at
		  FROM embeddings_visual e
		  JOIN files f ON f.id = e.file_id
		 WHERE %s
	`, strings.Join(where, " AND "))
	var hit Hit
	err := s.pool.QueryRow(ctx, sql, args...).Scan(
		&hit.EvidenceID,
		&hit.FileID,
		&hit.Name,
		&hit.Path,
		&hit.MIME,
		&hit.ContentSHA256,
		&hit.ChunkIndex,
		&hit.Snippet,
		&hit.Summary,
		&hit.TimelineAt,
		&hit.CreatedAt,
	)
	if err != nil || ref.EvidenceID != ref.FileID {
		return Hit{}, ErrReplayReferenceUnavailable
	}
	hit.Score = ref.Score
	hit.Source = RouteVisual
	hit.Snippet = truncateRunes(hit.Snippet, q.SnippetChars)
	return hit, nil
}

func appendTimeFilters(
	args []any,
	where []string,
	since, until *time.Time,
) ([]any, []string) {
	if since != nil {
		args = append(args, *since)
		where = append(
			where,
			fmt.Sprintf("COALESCE(f.timeline_at, f.created_at) >= $%d", len(args)),
		)
	}
	if until != nil {
		args = append(args, *until)
		where = append(
			where,
			fmt.Sprintf("COALESCE(f.timeline_at, f.created_at) <= $%d", len(args)),
		)
	}
	return args, where
}

// searchText runs the original text-embedding ANN route.
func (s *Service) searchText(ctx context.Context, q Query, text string) ([]Hit, error) {
	embSpec := strings.TrimSpace(q.EmbeddingProvider)
	if embSpec == "" {
		var err error
		embSpec, err = s.userEmbeddingSpec(ctx, q.UserID)
		if err != nil {
			return nil, fmt.Errorf("resolve text embedding space: %w", err)
		}
	}
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
type autoResult struct {
	hits []Hit
	err  error
}

func (s *Service) searchAuto(ctx context.Context, q Query, text string) ([]Hit, error) {
	textCh := make(chan autoResult, 1)
	visualCh := make(chan autoResult, 1)

	go func() {
		hits, err := s.searchText(ctx, q, text)
		textCh <- autoResult{hits, err}
	}()
	go func() {
		hits, err := s.searchVisual(ctx, q, text)
		visualCh <- autoResult{hits, err}
	}()
	tr := <-textCh
	vr := <-visualCh
	return s.mergeAutoResults(q, tr, vr)
}

func (s *Service) mergeAutoResults(q Query, tr, vr autoResult) ([]Hit, error) {
	if tr.err != nil && vr.err != nil {
		return nil, fmt.Errorf("both routes failed: text=%v; visual=%v", tr.err, vr.err)
	}
	if q.RequireText && tr.err != nil {
		return nil, fmt.Errorf("incomplete auto retrieval: text route failed: %w", tr.err)
	}
	// Interactive search may tolerate one failed route when the other route
	// produced evidence. Never turn a failed route plus an empty fallback into
	// a successful empty result, because that is indistinguishable from "no
	// memory" to callers.
	if tr.err != nil && len(vr.hits) == 0 {
		return nil, fmt.Errorf("text route failed and visual route returned no evidence: %w", tr.err)
	}
	if vr.err != nil && len(tr.hits) == 0 {
		return nil, fmt.Errorf("visual route failed and text route returned no evidence: %w", vr.err)
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
	args, where = appendPathFilters(args, where, q.PathPrefix, q.AllowedPaths)
	args, where = appendMIMEFilter(args, where, q.Type)
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
		SELECT evidence_id, file_id, name, path, mime, content_sha256,
		       chunk_index, score, snippet, summary, timeline_at, created_at
		FROM (
		  SELECT DISTINCT ON (f.id)
		    e.id::text    AS evidence_id,
		    f.id          AS file_id,
		    f.name        AS name,
		    f.path        AS path,
		    f.mime        AS mime,
		    f.sha256      AS content_sha256,
		    e.chunk_index AS chunk_index,
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

	return s.scanHits(ctx, sql, args, RouteText, q.SnippetChars)
}

// runVisualANN issues the visual-route SQL.
func (s *Service) runVisualANN(ctx context.Context, q Query, vec []float32) ([]Hit, error) {
	args := []any{vectorLiteral(vec), q.UserID}
	where := []string{"f.user_id = $2"}
	args, where = appendPathFilters(args, where, q.PathPrefix, q.AllowedPaths)
	args, where = appendMIMEFilter(args, where, q.Type)
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
		SELECT 'visual:' || f.id::text AS evidence_id,
		       f.id, f.name, f.path, f.mime, f.sha256, -1 AS chunk_index,
		       (1 - (e.embedding <=> $1::vector))::real AS score,
		       COALESCE(f.caption, '') AS snippet,
		       f.summary, f.timeline_at, f.created_at
		  FROM embeddings_visual e
		  JOIN files f ON f.id = e.file_id
		 WHERE %s
		 ORDER BY e.embedding <=> $1::vector ASC
	 LIMIT $%d
	`, strings.Join(where, " AND "), limitIdx)

	return s.scanHits(ctx, sql, args, RouteVisual, q.SnippetChars)
}

// scanHits is the common cursor → []Hit loop. Tags every hit with its source route.
func (s *Service) scanHits(ctx context.Context, sql string, args []any, route string, snippetChars int) ([]Hit, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search query (%s): %w", route, err)
	}
	defer rows.Close()

	out := make([]Hit, 0, 16)
	for rows.Next() {
		var h Hit
		if err := rows.Scan(
			&h.EvidenceID, &h.FileID, &h.Name, &h.Path, &h.MIME,
			&h.ContentSHA256, &h.ChunkIndex,
			&h.Score, &h.Snippet, &h.Summary, &h.TimelineAt, &h.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan hit: %w", err)
		}
		sanitizeDerivedDisplayText(&h, route)
		h.Snippet = truncateRunes(h.Snippet, snippetChars)
		h.Source = route
		out = append(out, h)
	}
	return out, rows.Err()
}

func sanitizeDerivedDisplayText(hit *Hit, route string) {
	if hit == nil {
		return
	}
	if hit.Summary != nil {
		if summary, ok := modeltext.NormalizePlain(*hit.Summary, 2000); ok {
			hit.Summary = &summary
		} else {
			hit.Summary = nil
		}
	}
	// Text snippets are source-document evidence and may legitimately contain
	// structured syntax. Visual snippets are model-produced captions.
	if route == RouteVisual {
		if caption, ok := modeltext.NormalizePlain(hit.Snippet, 2000); ok {
			hit.Snippet = caption
		} else {
			hit.Snippet = ""
		}
	}
}

func appendPathFilters(args []any, where []string, requested string, allowed []string) ([]any, []string) {
	if requested != "" && requested != "/" {
		args = append(args, requested)
		n := len(args)
		where = append(where, fmt.Sprintf(
			"(f.path = $%d OR left(f.path, length($%d) + 1) = $%d || '/')",
			n, n, n,
		))
	}
	if len(allowed) == 0 {
		return args, where
	}
	for _, p := range allowed {
		if p == "/" {
			return args, where
		}
	}
	clauses := make([]string, 0, len(allowed))
	for _, p := range allowed {
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
		// A non-empty but invalid/legacy allow-list must never broaden access.
		where = append(where, "FALSE")
	}
	return args, where
}

// appendMIMEFilter translates the product-level search categories into MIME
// predicates shared by the text and visual routes. "any" means no filter;
// "doc" is intentionally broader than the nonexistent "doc/*" MIME tree.
func appendMIMEFilter(args []any, where []string, raw string) ([]any, []string) {
	kind := strings.ToLower(strings.TrimSpace(raw))
	switch kind {
	case "", "any":
		return args, where
	case "doc", "document":
		patterns := []string{
			"text/%",
			"application/pdf",
			"application/msword",
			"application/vnd.openxmlformats-officedocument.%",
			"application/vnd.oasis.opendocument.%",
			"application/rtf",
			"application/epub+zip",
		}
		args = append(args, patterns)
		where = append(where, fmt.Sprintf("f.mime LIKE ANY($%d::text[])", len(args)))
		return args, where
	default:
		if strings.Contains(kind, "/") {
			args = append(args, kind)
			where = append(where, fmt.Sprintf("f.mime = $%d", len(args)))
			return args, where
		}
		args = append(args, kind+"/%")
		where = append(where, fmt.Sprintf("f.mime LIKE $%d", len(args)))
		return args, where
	}
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= limit {
		return s
	}
	return string(rs[:limit]) + "..."
}

// sortHitsByScoreDesc — small enough corpus that sort.Slice is fine.
func sortHitsByScoreDesc(hits []Hit) {
	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].Score > hits[j].Score
	})
}

// userEmbeddingSpec resolves one explicit vector space. Existing corpus
// metadata takes precedence over mutable worker environment defaults.
func (s *Service) userEmbeddingSpec(ctx context.Context, userID uuid.UUID) (string, error) {
	var spec string
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM provider_settings WHERE user_id = $1 AND kind = 'embedding'`,
		userID,
	).Scan(&spec)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	corpusProvider, hasCorpus, err := indexmeta.TextProvider(ctx, s.pool, userID)
	if err != nil {
		return "", err
	}
	if hasCorpus {
		if spec != "" && spec != corpusProvider {
			return "", fmt.Errorf(
				"configured provider %q differs from corpus provider %q",
				spec, corpusProvider,
			)
		}
		return corpusProvider, nil
	}
	if spec != "" {
		return spec, nil
	}
	return s.defaultEmbeddingProvider, nil
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
