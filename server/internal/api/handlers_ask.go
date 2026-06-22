package api

import (
	"encoding/json"
	"net/http"

	"github.com/PeterGuy326/mem/server/internal/ask"
	"github.com/PeterGuy326/mem/server/internal/auth"
)

// handleAsk → POST /v1/ask
// Body: { "question": "...", "scope": "/Photos/2012", "top_k": 5 }
// Returns: { "answer": "...", "sources": [...], "provider": "...", "latency_ms": N }
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Ask == nil {
		writeError(w, http.StatusServiceUnavailable, "ask_disabled",
			"ask service not configured (MEM_WORKER_GRPC missing?)")
		return
	}
	var req struct {
		Question string `json:"question"`
		Scope    string `json:"scope,omitempty"`
		TopK     int    `json:"top_k,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	ans, err := s.Ask.Ask(r.Context(), ask.Request{
		UserID:   u.ID,
		Question: req.Question,
		Scope:    req.Scope,
		TopK:     req.TopK,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "ask_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ans)
}

// handleAskStream → POST /v1/ask/stream
// Streams the RAG answer as Server-Sent Events: one `data: {StreamEvent}` line
// per chunk (step / thinking / answer / sources / done / error), so the UI can
// render the model's reasoning and answer token-by-token.
func (s *Server) handleAskStream(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Ask == nil {
		writeError(w, http.StatusServiceUnavailable, "ask_disabled", "ask service not configured")
		return
	}
	var req struct {
		Question string `json:"question"`
		Scope    string `json:"scope,omitempty"`
		TopK     int    `json:"top_k,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_flush", "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	emit := func(ev ask.StreamEvent) error {
		b, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte("data: ")); err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if err := s.Ask.AskStream(r.Context(), ask.Request{
		UserID:   u.ID,
		Question: req.Question,
		Scope:    req.Scope,
		TopK:     req.TopK,
	}, emit); err != nil {
		_ = emit(ask.StreamEvent{Type: "error", Error: err.Error()})
	}
}
