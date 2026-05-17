// Package ask implements the RAG (retrieval-augmented generation) flow that
// powers `mem ask` and the MCP `mem_ask` tool.
//
// Flow (SPEC §F5):
//  1. Run a text search to find the top-K relevant chunks.
//  2. Pack the chunks into a context block + the user question.
//  3. Ask the LLM (via worker.Chat) to synthesize an answer.
//  4. Return answer + sources (file_id + chunk excerpts).
//
// Provider selection:
//   - LLM is the user's saved llm spec, or worker default if absent.
//   - Embedding for retrieval is the user's saved embedding spec (so dim
//     matches stored vectors — search package handles this for us).
//
// Why a separate package instead of bolting onto search:
// the prompt template + source-formatting logic has nothing to do with ANN,
// and keeping it isolated makes future swap-in of streaming / function-calling
// LLMs a one-package change.
package ask

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/search"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
)

// Service is the ask service.
type Service struct {
	pool   *pgxpool.Pool
	search *search.Service
	worker *workerclient.Client
	log    *slog.Logger
}

// New constructs an ask Service.
func New(pool *pgxpool.Pool, s *search.Service, w *workerclient.Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, search: s, worker: w, log: log}
}

// Source is a citation pointing back to one file + excerpt the LLM saw.
type Source struct {
	FileID  uuid.UUID `json:"file_id"`
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	MIME    string    `json:"mime"`
	Excerpt string    `json:"excerpt"`
	Score   float32   `json:"score"`
}

// Answer is the response payload.
type Answer struct {
	Answer    string    `json:"answer"`
	Sources   []Source  `json:"sources"`
	Provider  string    `json:"provider"`
	LatencyMS int64     `json:"latency_ms"`
	AskedAt   time.Time `json:"asked_at"`
}

// Request bundles all inputs.
type Request struct {
	UserID   uuid.UUID
	Question string
	Scope    string // path prefix filter, e.g. "/Photos/2012" (TODO: not yet wired into search)
	TopK     int    // number of context chunks (default 5, max 20)
}

// Ask runs the full RAG flow.
func (s *Service) Ask(ctx context.Context, req Request) (*Answer, error) {
	q := strings.TrimSpace(req.Question)
	if q == "" {
		return nil, fmt.Errorf("question is empty")
	}
	if req.UserID == uuid.Nil {
		return nil, fmt.Errorf("user_id required")
	}
	if s.search == nil || s.worker == nil || !s.worker.Enabled() {
		return nil, fmt.Errorf("ask disabled: search/worker not configured")
	}
	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}

	start := time.Now()

	// 1. Retrieve. Use the text route — visual hits don't give the LLM
	// reliable excerpts to ground its answer in.
	hits, err := s.search.Search(ctx, search.Query{
		UserID: req.UserID,
		Text:   q,
		Route:  search.RouteText,
		Limit:  topK,
	})
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}
	if len(hits) == 0 {
		return &Answer{
			Answer:    "I don't have any documents in your drive that match this question.",
			Sources:   []Source{},
			LatencyMS: time.Since(start).Milliseconds(),
			AskedAt:   start,
		}, nil
	}

	// 2. Build prompt.
	systemPrompt := "You are mem, the user's personal AI drive. Answer their " +
		"question using ONLY the snippets below. If the snippets don't " +
		"contain the answer, say so honestly. Cite sources by their [N] tag " +
		"inline (e.g. \"[1]\")."

	var ctxBlock strings.Builder
	sources := make([]Source, 0, len(hits))
	for i, h := range hits {
		fmt.Fprintf(&ctxBlock, "\n[%d] %s\n%s\n", i+1, h.Name, h.Snippet)
		sources = append(sources, Source{
			FileID:  h.FileID,
			Name:    h.Name,
			Path:    h.Path,
			MIME:    h.MIME,
			Excerpt: h.Snippet,
			Score:   h.Score,
		})
	}

	userMsg := fmt.Sprintf(
		"Snippets from my drive:\n%s\n\nQuestion: %s",
		ctxBlock.String(), q,
	)

	// 3. Resolve user's LLM provider (worker falls back to its default if empty).
	llmSpec := s.userLLMSpec(ctx, req.UserID)

	// 4. Call worker.Chat.
	chatCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	reply, err := s.worker.Chat(chatCtx, req.UserID.String(),
		[]workerclient.ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMsg},
		},
		llmSpec,
	)
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}

	return &Answer{
		Answer:    strings.TrimSpace(reply),
		Sources:   sources,
		Provider:  llmSpec,
		LatencyMS: time.Since(start).Milliseconds(),
		AskedAt:   start,
	}, nil
}

// userLLMSpec reads the user's saved llm provider. Empty string means
// "fall back to worker default".
func (s *Service) userLLMSpec(ctx context.Context, userID uuid.UUID) string {
	var spec string
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM provider_settings WHERE user_id = $1 AND kind = 'llm'`,
		userID,
	).Scan(&spec)
	if err != nil {
		return ""
	}
	return spec
}
