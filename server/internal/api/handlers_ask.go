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
