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
	"sort"
	"strings"
	"time"
	"unicode"

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

// Step records one stage of the RAG pipeline with its real wall-clock cost,
// so the UI can show an execution trace (not just the model's thinking).
type Step struct {
	Name       string `json:"name"`        // machine id: "retrieve" | "generate"
	Label      string `json:"label"`       // human label
	Detail     string `json:"detail"`      // e.g. "命中 5 个片段" / model spec
	DurationMS int64  `json:"duration_ms"` // wall-clock for this stage
}

// Answer is the response payload.
type Answer struct {
	Answer    string    `json:"answer"`
	Sources   []Source  `json:"sources"`
	Steps     []Step    `json:"steps"`
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

// ragSystemPrompt keeps unary and streaming Ask answers consistent. The
// explicit-evidence rule is important for a personal drive: plausible
// inferences are not acceptable substitutes for facts in a source file.
func ragSystemPrompt() string {
	return "你是 mem，用户个人网盘的 AI 助手。请严格遵守以下规则：" +
		"1. 除非用户明确要求其他语言，否则只用简体中文回答。" +
		"2. 只能使用下方资料片段明确写出的事实，不得根据文件名、常识或上下文猜测。" +
		"3. 每个事实句都必须在句末标注对应来源编号，例如“图片中有一只猫 [1]。”；" +
		"禁止省略 [N] 引用，禁止引用未支持该事实的来源。" +
		"4. 如果资料不能明确回答，只回答“资料中没有明确说明。”" +
		"5. 标为“图像描述”的内容可以作为图片内容证据；标为“视觉检索候选”的内容只能证明" +
		"可能存在相关图片，必须回答“已找到可能相关图片 [N]”，不能确认未描述的品种、人物、地点或事件。"
}

// ragEvidence turns a visual ANN hit into an honest, useful RAG snippet. CLIP
// can establish semantic similarity even when no VLM caption is available;
// leaving its snippet empty previously made Ask discard all image evidence.
func ragEvidence(h search.Hit) string {
	snippet := strings.TrimSpace(h.Snippet)
	if h.Source != search.RouteVisual {
		return snippet
	}
	if snippet != "" {
		return "图像描述：" + snippet
	}
	return fmt.Sprintf(
		"视觉检索候选：图片文件 %q 与问题的文字描述语义相似（相似度 %.2f）。"+
			"可以回答已找到可能相关图片；不能据此确认未被图片描述明确说明的具体属性。",
		h.Name, h.Score,
	)
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
	steps := make([]Step, 0, 2)

	// 1. Retrieve. Use the auto route (text + visual) so the model can also
	// answer about images: an image's VLM caption rides along as the snippet,
	// e.g. "do I have a dog photo?" surfaces golden_retriever.jpg.
	retrieveStart := time.Now()
	hits, err := s.retrieveHits(ctx, req.UserID, q, topK)
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}
	steps = append(steps, Step{
		Name:       "retrieve",
		Label:      "向量检索",
		Detail:     fmt.Sprintf("命中 %d 个相关片段", len(hits)),
		DurationMS: time.Since(retrieveStart).Milliseconds(),
	})
	if len(hits) == 0 {
		return &Answer{
			Answer:    "我的网盘中没有找到与此问题相关的文件。",
			Sources:   []Source{},
			Steps:     steps,
			LatencyMS: time.Since(start).Milliseconds(),
			AskedAt:   start,
		}, nil
	}

	// 2. Build prompt.
	systemPrompt := ragSystemPrompt()

	var ctxBlock strings.Builder
	sources := make([]Source, 0, len(hits))
	for i, h := range hits {
		fmt.Fprintf(&ctxBlock, "\n[%d] %s\n%s\n", i+1, h.Name, ragEvidence(h))
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
	genStart := time.Now()
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
	genLabel := llmSpec
	if genLabel == "" {
		genLabel = "worker 默认模型"
	}
	steps = append(steps, Step{
		Name:       "generate",
		Label:      "生成答案",
		Detail:     genLabel,
		DurationMS: time.Since(genStart).Milliseconds(),
	})

	return &Answer{
		Answer:    strings.TrimSpace(reply),
		Sources:   sources,
		Steps:     steps,
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

// retrieveHits keeps image questions inside the image collection. Mixing raw
// text-embedding and CLIP scores previously let unrelated markdown files push
// the actual photo out of the prompt. For images we fetch a wider CLIP
// candidate set, then use their VLM captions as a lexical reranker; this is
// particularly helpful because ViT-B/32 is much weaker on Chinese prompts.
func (s *Service) retrieveHits(ctx context.Context, userID uuid.UUID, question string, limit int) ([]search.Hit, error) {
	q := search.Query{
		UserID: userID,
		Text:   question,
		Route:  search.RouteAuto,
		Limit:  limit,
	}
	if !hasVisualIntent(question) {
		return s.search.Search(ctx, q)
	}

	q.Route = search.RouteVisual
	q.Type = "image"
	q.Limit = max(20, limit*4)
	hits, err := s.search.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(hits, func(i, j int) bool {
		left := hits[i].Score + captionLexicalScore(question, hits[i].Snippet)
		right := hits[j].Score + captionLexicalScore(question, hits[j].Snippet)
		if left == right {
			return hits[i].Score > hits[j].Score
		}
		return left > right
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func hasVisualIntent(question string) bool {
	q := strings.ToLower(question)
	for _, marker := range []string{"图片", "照片", "图像", "看图", "image", "photo", "picture"} {
		if strings.Contains(q, marker) {
			return true
		}
	}
	return false
}

// captionLexicalScore rewards concrete words shared by the user's question and
// a generated image caption. It deliberately ignores common UI/question words
// so terms such as "金毛犬" or "虎斑猫" dominate over "图片" and "相关".
func captionLexicalScore(question, caption string) float32 {
	q := searchableRunes(question)
	c := string(searchableRunes(caption))
	if len(q) == 0 || len(c) == 0 {
		return 0
	}
	stop := map[string]struct{}{
		"图片": {}, "照片": {}, "图像": {}, "相关": {}, "场景": {},
		"什么": {}, "内容": {}, "回答": {}, "依据": {}, "是否": {},
		"我的": {}, "我有": {}, "请问": {}, "里面": {},
	}
	seen := make(map[string]struct{})
	var points int
	for n := 2; n <= 4; n++ {
		for i := 0; i+n <= len(q); i++ {
			term := string(q[i : i+n])
			if _, ignored := stop[term]; ignored {
				continue
			}
			if _, duplicate := seen[term]; duplicate {
				continue
			}
			seen[term] = struct{}{}
			if strings.Contains(c, term) {
				points += n * n
			}
		}
	}
	// Preserve useful one-character object names such as 猫 and 狗.
	for _, r := range q {
		if strings.ContainsRune("的是有和与在中里吗呢我你他它图像片照相", r) {
			continue
		}
		if strings.ContainsRune(c, r) {
			points++
		}
	}
	score := float32(points) * 0.03
	if score > 1 {
		return 1
	}
	return score
}

func searchableRunes(text string) []rune {
	out := make([]rune, 0, len(text))
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, r)
		}
	}
	return out
}
