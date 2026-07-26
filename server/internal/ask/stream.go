package ask

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// StreamEvent is one server-sent event in the streaming-ask protocol. The web
// client renders thinking/answer deltas live as they arrive.
type StreamEvent struct {
	Type    string   `json:"type"` // step | thinking | answer | sources | done | error
	Step    *Step    `json:"step,omitempty"`
	Delta   string   `json:"delta,omitempty"`
	Sources []Source `json:"sources,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// AskStream runs the RAG flow but streams the LLM output token-by-token via the
// `emit` callback, so the UI can show the answer in real time. It supports the
// project's local default (ollama:<model>) as well as OpenAI-compatible models.
func (s *Service) AskStream(ctx context.Context, req Request, emit func(StreamEvent) error) error {
	q := strings.TrimSpace(req.Question)
	if q == "" {
		return fmt.Errorf("question is empty")
	}
	spec := s.userLLMSpec(ctx, req.UserID)
	if spec == "" {
		spec = os.Getenv("MEM_DEFAULT_LLM")
	}
	// The bare-metal development stack delegates default selection to the worker
	// and therefore does not set MEM_DEFAULT_LLM on memd. Keep streaming aligned
	// with that local default instead of forcing callers to configure a provider.
	if spec == "" {
		spec = "ollama:llama3.1"
	}
	vendor, model, ok := strings.Cut(spec, ":")
	if !ok || model == "" {
		return fmt.Errorf("streaming requires a provider spec like ollama:model or openai:model, got %q", spec)
	}
	vendor = strings.ToLower(strings.TrimSpace(vendor))
	model = strings.TrimSpace(model)
	if vendor != "ollama" && vendor != "openai" {
		return fmt.Errorf("streaming is not implemented for provider %q", vendor)
	}

	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}

	// 1. Retrieve — auto route (text + visual) so images (via their caption)
	// can ground answers about photos too.
	retrieveStart := time.Now()
	hits, err := s.retrieveHits(ctx, req.UserID, q, topK)
	if err != nil {
		return fmt.Errorf("retrieve: %w", err)
	}
	if err := emit(StreamEvent{Type: "step", Step: &Step{
		Name:       "retrieve",
		Label:      "向量检索",
		Detail:     fmt.Sprintf("命中 %d 个相关片段", len(hits)),
		DurationMS: time.Since(retrieveStart).Milliseconds(),
	}}); err != nil {
		return err
	}
	if len(hits) == 0 {
		_ = emit(StreamEvent{Type: "answer", Delta: "我的网盘中没有找到与此问题相关的文件。"})
		return emit(StreamEvent{Type: "done"})
	}

	// 2. Build prompt + sources.
	systemPrompt := ragSystemPrompt()
	var ctxBlock strings.Builder
	sources := make([]Source, 0, len(hits))
	for i, h := range hits {
		fmt.Fprintf(&ctxBlock, "\n[%d] %s\n%s\n", i+1, h.Name, ragEvidence(h))
		sources = append(sources, Source{
			FileID: h.FileID, Name: h.Name, Path: h.Path, MIME: h.MIME,
			Excerpt: h.Snippet, Score: h.Score,
		})
	}
	if err := emit(StreamEvent{Type: "sources", Sources: sources}); err != nil {
		return err
	}
	userMsg := fmt.Sprintf("Snippets from my drive:\n%s\n\nQuestion: %s", ctxBlock.String(), q)

	// 3. Stream the chat completion from the selected provider.
	genStart := time.Now()
	messages := []chatMsg{{Role: "system", Content: systemPrompt}, {Role: "user", Content: userMsg}}
	onDelta := func(reasoning, content string) error {
		if reasoning != "" {
			if e := emit(StreamEvent{Type: "thinking", Delta: reasoning}); e != nil {
				return e
			}
		}
		if content != "" {
			if e := emit(StreamEvent{Type: "answer", Delta: content}); e != nil {
				return e
			}
		}
		return nil
	}
	if vendor == "ollama" {
		base := strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/")
		if base == "" {
			base = "http://localhost:11434"
		}
		err = streamOllamaChat(ctx, base, model, messages, onDelta)
	} else {
		base := strings.TrimRight(os.Getenv("OPENAI_BASE_URL"), "/")
		key := os.Getenv("OPENAI_API_KEY")
		if base == "" || key == "" {
			return fmt.Errorf("streaming unavailable: OPENAI_BASE_URL/OPENAI_API_KEY not set")
		}
		err = streamChat(ctx, base, key, model, messages, onDelta)
	}
	if err != nil {
		return fmt.Errorf("llm stream: %w", err)
	}
	_ = emit(StreamEvent{Type: "step", Step: &Step{
		Name:       "generate",
		Label:      "生成答案",
		Detail:     spec,
		DurationMS: time.Since(genStart).Milliseconds(),
	}})
	return emit(StreamEvent{Type: "done"})
}

// streamOllamaChat translates Ollama's newline-delimited JSON stream into the
// same delta callback used by OpenAI-compatible providers. Ollama exposes
// assistant text in message.content and signals completion with done: true.
func streamOllamaChat(ctx context.Context, base, model string, messages []chatMsg, onDelta func(reasoning, content string) error) error {
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   true,
		// Thinking-capable models (for example qwen3) otherwise spend several
		// seconds emitting a separate thinking field before message.content.
		// The RAG UI needs a concise grounded answer, not hidden reasoning.
		"think": false,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("ollama %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var chunk struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done bool `json:"done"`
		}
		if err := json.Unmarshal(sc.Bytes(), &chunk); err != nil {
			return fmt.Errorf("decode ollama stream: %w", err)
		}
		if chunk.Message.Content != "" {
			if err := onDelta("", chunk.Message.Content); err != nil {
				return err
			}
		}
		if chunk.Done {
			break
		}
	}
	return sc.Err()
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// streamChat POSTs an OpenAI-compatible streaming chat completion and invokes
// onDelta for each chunk with the reasoning-content and answer-content deltas.
func streamChat(ctx context.Context, base, key, model string, messages []chatMsg, onDelta func(reasoning, content string) error) error {
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   true,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("gateway %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed keep-alive / partial lines
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.ReasoningContent != "" || d.Content != "" {
			if err := onDelta(d.ReasoningContent, d.Content); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
