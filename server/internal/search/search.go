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

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/indexmeta"
	"github.com/PeterGuy326/mem/server/internal/modeltext"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
)

type textEmbedder interface {
	Enabled() bool
	EmbedTextWith(context.Context, string, string) ([]float32, error)
}

// profileTextEmbedder is intentionally separate from textEmbedder so existing
// lightweight test doubles remain usable for legacy routes. The production
// workerclient implements it and transmits the complete, explicit profile
// contract rather than falling through to process-wide defaults.
type profileTextEmbedder interface {
	EmbedTextWithProfile(context.Context, string, workerclient.AIProfileOptions) ([]float32, error)
}

// aiProfileResolver is the narrow selection lookup search needs. Search owns
// a resource owner ID, not an HTTP workspace object.
type aiProfileResolver interface {
	ResolveForOwner(context.Context, uuid.UUID) (*aiprofile.Selection, error)
}

type embeddingRoute struct {
	textSpec   string
	visualSpec string
	profile    *workerclient.AIProfileOptions
	dataEgress string
}

const (
	aiProfileContract        = "mem.ai-profile/v1"
	textEmbeddingSchemaDim   = 768
	visualEmbeddingSchemaDim = 512
)

// Service is the search service.
type Service struct {
	pool                     *pgxpool.Pool
	worker                   textEmbedder
	defaultEmbeddingProvider string
	profiles                 aiProfileResolver
	generations              GenerationResolver
	requireProfile           bool
	// requireManagedProfileReservation prevents in-process callers from
	// bypassing the HTTP managed-search executor after a workspace chooses a
	// paid profile. The executor adds a private context marker only after its
	// entitlement reservation succeeds; this service then remains the final
	// guard immediately before the Worker invocation.
	requireManagedProfileReservation bool
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

// SetAIProfiles makes an explicit workspace profile authoritative for search.
// Hosted deployments set requireProfile to prevent an unselected workspace
// from accidentally borrowing a process-wide managed embedding default.
func (s *Service) SetAIProfiles(resolver aiProfileResolver, requireProfile bool) {
	if s == nil {
		return
	}
	s.profiles = resolver
	s.requireProfile = requireProfile
}

// RequireManagedProfileReservation makes a managed workspace profile fail
// closed unless the request was authorized by the server's reservation
// executor. It has no effect on local profiles or visual-only CLIP queries.
// memd enables this for SaaS after wiring the entitlement-backed executor.
func (s *Service) RequireManagedProfileReservation(required bool) {
	if s == nil {
		return
	}
	s.requireManagedProfileReservation = required
}

// ErrManagedProfileReservationRequired is deliberately provider-neutral. A
// caller that reaches this error has not contacted the Worker or an upstream
// model; the HTTP layer maps it through the managed entitlement error path.
var ErrManagedProfileReservationRequired = errors.New("managed workspace AI profile reservation required")

type managedEmbeddingReservationContextKey struct{}

type managedEmbeddingReservation struct {
	providerSpec string
}

// WithManagedEmbeddingReservation marks a context after the server has
// reserved a managed query embedding. It is an in-process capability, not a
// client credential: only the HTTP entitlement executor should call it.
func WithManagedEmbeddingReservation(ctx context.Context, providerSpec string) context.Context {
	providerSpec = strings.TrimSpace(providerSpec)
	if providerSpec == "" {
		return ctx
	}
	return context.WithValue(ctx, managedEmbeddingReservationContextKey{}, managedEmbeddingReservation{
		providerSpec: providerSpec,
	})
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
	route, err := s.embeddingRouteForUser(ctx, userID)
	if err != nil {
		return "", err
	}
	return route.textSpec, nil
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
	route, err := s.embeddingRouteForUser(ctx, q.UserID)
	if err != nil {
		return nil, fmt.Errorf("resolve text embedding space: %w", err)
	}
	return s.searchTextWithRoute(ctx, q, text, route)
}

func (s *Service) searchTextWithRoute(
	ctx context.Context,
	q Query,
	text string,
	route embeddingRoute,
) ([]Hit, error) {
	// Managed entitlement establishes this value before provider invocation.
	// Treat it as an assertion over the server-resolved route, not an override
	// that can strip a selected profile's explicit dimensions/stage settings.
	if requested := strings.TrimSpace(q.EmbeddingProvider); requested != "" && requested != route.textSpec {
		return nil, fmt.Errorf("requested embedding provider does not match the workspace AI profile")
	}
	if err := s.requireManagedReservation(ctx, route); err != nil {
		return nil, err
	}
	embCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var (
		vec []float32
		err error
	)
	if route.profile != nil {
		profileWorker, ok := s.worker.(profileTextEmbedder)
		if !ok {
			return nil, fmt.Errorf("profile search requires a profile-capable worker")
		}
		vec, err = profileWorker.EmbedTextWithProfile(embCtx, text, *route.profile)
	} else {
		vec, err = s.worker.EmbedTextWith(embCtx, text, route.textSpec)
	}
	if err != nil {
		return nil, fmt.Errorf("embed query (text): %w", err)
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("empty text embedding")
	}
	if gen := s.resolveActiveGeneration(ctx, q.UserID, RouteText); gen != nil {
		return s.runTextANNGeneration(ctx, q, vec, gen)
	}
	return s.runTextANN(ctx, q, vec)
}

// requireManagedReservation is intentionally checked immediately before a
// text Worker embedding. A profile's visual route uses fixed local CLIP, so
// it has no managed-provider reservation requirement.
func (s *Service) requireManagedReservation(ctx context.Context, route embeddingRoute) error {
	if s == nil || !s.requireManagedProfileReservation || route.profile == nil ||
		route.dataEgress != aiprofile.DataEgressManagedIdealab {
		return nil
	}
	reservation, ok := ctx.Value(managedEmbeddingReservationContextKey{}).(managedEmbeddingReservation)
	if !ok || reservation.providerSpec != route.textSpec {
		return ErrManagedProfileReservationRequired
	}
	return nil
}

// searchVisual encodes the query via the CLIP text tower and ANN-matches it
// against embeddings_visual (which holds CLIP image-tower vectors).
func (s *Service) searchVisual(ctx context.Context, q Query, text string) ([]Hit, error) {
	route, err := s.embeddingRouteForUser(ctx, q.UserID)
	if err != nil {
		return nil, fmt.Errorf("resolve visual embedding space: %w", err)
	}
	return s.searchVisualWithRoute(ctx, q, text, route)
}

func (s *Service) searchVisualWithRoute(
	ctx context.Context,
	q Query,
	text string,
	route embeddingRoute,
) ([]Hit, error) {
	visualSpec, err := visualProviderForRoute(route)
	if err != nil {
		return nil, err
	}
	embCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// The Worker profile contract currently fixes its text stage at 768
	// dimensions, while CLIP's text tower is a separate 512-d visual space.
	// Passing this server-selected explicit spec is still fail-closed: it does
	// not consult a MEM_DEFAULT_* value and it is validated against the active
	// profile's visual stage above.
	vec, err := s.worker.EmbedTextWith(embCtx, text, visualSpec)
	if err != nil {
		return nil, fmt.Errorf("embed query (visual/clip): %w", err)
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("empty visual embedding")
	}
	if gen := s.resolveActiveGeneration(ctx, q.UserID, RouteVisual); gen != nil {
		return s.runVisualANNGeneration(ctx, q, vec, gen)
	}
	return s.runVisualANN(ctx, q, vec)
}

func visualProviderForRoute(route embeddingRoute) (string, error) {
	if route.visualSpec != "" {
		return route.visualSpec, nil
	}
	if route.profile != nil {
		return "", fmt.Errorf("workspace AI profile visual embedding is disabled")
	}
	// Legacy visual search preserves the historical explicit CLIP provider.
	return "clip:ViT-B-32", nil
}

// searchAuto runs text + visual in parallel, merges by file_id keeping the
// best-scoring row per file, re-sorts by score.
type autoResult struct {
	hits []Hit
	err  error
}

func (s *Service) searchAuto(ctx context.Context, q Query, text string) ([]Hit, error) {
	route, err := s.embeddingRouteForUser(ctx, q.UserID)
	if err != nil {
		return nil, fmt.Errorf("resolve auto embedding spaces: %w", err)
	}
	return s.searchAutoWithRoute(ctx, q, text, route)
}

func (s *Service) searchAutoWithRoute(
	ctx context.Context,
	q Query,
	text string,
	route embeddingRoute,
) ([]Hit, error) {
	return s.runAutoSearch(
		q,
		route,
		func() ([]Hit, error) {
			return s.searchTextWithRoute(ctx, q, text, route)
		},
		func() ([]Hit, error) {
			return s.searchVisualWithRoute(ctx, q, text, route)
		},
	)
}

func (s *Service) runAutoSearch(
	q Query,
	route embeddingRoute,
	textSearch func() ([]Hit, error),
	visualSearch func() ([]Hit, error),
) ([]Hit, error) {
	// A selected text-only profile deliberately has no visual provider. Auto
	// therefore means text-only for that workspace: do not invoke a disabled
	// stage, and preserve a successful empty text result as "no matches".
	if route.profile != nil && route.visualSpec == "" {
		return textSearch()
	}

	textCh := make(chan autoResult, 1)
	visualCh := make(chan autoResult, 1)

	go func() {
		hits, err := textSearch()
		textCh <- autoResult{hits, err}
	}()
	go func() {
		hits, err := visualSearch()
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

// embeddingRouteForUser resolves an exact, server-owned vector-space route.
// An active profile blocks all legacy settings and global default fallback.
func (s *Service) embeddingRouteForUser(ctx context.Context, userID uuid.UUID) (embeddingRoute, error) {
	if s.profiles != nil {
		selection, err := s.profiles.ResolveForOwner(ctx, userID)
		if err == nil {
			route, routeErr := searchRouteFromAIProfile(selection)
			if routeErr != nil {
				return embeddingRoute{}, routeErr
			}
			if err := s.requireProfileCorpusCompatibility(ctx, userID, route.textSpec); err != nil {
				return embeddingRoute{}, err
			}
			return route, nil
		}
		if !errors.Is(err, aiprofile.ErrNotFound) {
			return embeddingRoute{}, fmt.Errorf("resolve workspace AI profile: %w", err)
		}
	}
	if s.requireProfile {
		return embeddingRoute{}, fmt.Errorf("workspace AI profile is required before searching")
	}
	spec, err := s.legacyEmbeddingSpec(ctx, userID)
	if err != nil {
		return embeddingRoute{}, err
	}
	return embeddingRoute{textSpec: spec, visualSpec: "clip:ViT-B-32"}, nil
}

// legacyEmbeddingSpec resolves one explicit vector space. Existing corpus
// metadata takes precedence over mutable worker environment defaults.
func (s *Service) legacyEmbeddingSpec(ctx context.Context, userID uuid.UUID) (string, error) {
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

// searchRouteFromAIProfile produces an embedding-only form of the selected
// profile for a query. A search query needs neither generation nor captioning;
// disabling those stages is deliberate so a long query cannot consume a
// profile's LLM/VLM/ASR defaults by accident.
func searchRouteFromAIProfile(selection *aiprofile.Selection) (embeddingRoute, error) {
	if err := aiprofile.ValidateSelection(selection); err != nil {
		return embeddingRoute{}, fmt.Errorf("invalid workspace AI profile selection: %w", err)
	}
	if !selection.Embedding.Enabled || selection.Embedding.Provider == "" ||
		selection.Embedding.Dimensions != textEmbeddingSchemaDim {
		return embeddingRoute{}, fmt.Errorf("workspace AI profile has an invalid text embedding stage")
	}
	if selection.VisualEmbedding.Enabled &&
		(selection.VisualEmbedding.Provider == "" ||
			selection.VisualEmbedding.Dimensions != visualEmbeddingSchemaDim) {
		return embeddingRoute{}, fmt.Errorf("workspace AI profile has an invalid visual embedding stage")
	}
	profile := &workerclient.AIProfileOptions{
		Contract:         aiProfileContract,
		ID:               selection.ProfileID,
		Revision:         selection.ProfileRevision,
		PipelineRevision: selection.PipelineRevision,
		DataEgress:       selection.DataEgress,
		Embedding: workerclient.ProviderStage{
			Enabled: true, Provider: selection.Embedding.Provider, Dimensions: selection.Embedding.Dimensions,
		},
		VisualEmbedding: workerclient.ProviderStage{
			Enabled:    selection.VisualEmbedding.Enabled,
			Provider:   selection.VisualEmbedding.Provider,
			Dimensions: selection.VisualEmbedding.Dimensions,
		},
		LLM:    workerclient.ProviderStage{Enabled: false},
		VLM:    workerclient.ProviderStage{Enabled: false},
		ASR:    workerclient.ProviderStage{Enabled: false},
		Rerank: workerclient.ProviderStage{Enabled: false},
	}
	return embeddingRoute{
		textSpec:   selection.Embedding.Provider,
		visualSpec: selection.VisualEmbedding.Provider,
		profile:    profile,
		dataEgress: selection.DataEgress,
	}, nil
}

// requireProfileCorpusCompatibility defends the read path against a direct
// database mutation or a concurrent writer that would otherwise make a query
// compare a new profile vector to an old corpus. Normal profile selection
// already prevents this before it can be persisted.
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
