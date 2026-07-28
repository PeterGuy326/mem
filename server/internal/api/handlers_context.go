package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/contextpack"
	"github.com/PeterGuy326/mem/server/internal/pathx"
	"github.com/PeterGuy326/mem/server/internal/search"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

// handleContext returns bounded evidence with stable citations. It never calls
// a chat model; the external Agent owns synthesis and action decisions.
func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	tok := r.Context().Value(ctxToken).(*auth.Token)
	if s.Context == nil {
		writeError(w, http.StatusServiceUnavailable, "context_disabled",
			"context service not configured")
		return
	}
	var req struct {
		Query      string  `json:"query"`
		Scope      string  `json:"scope,omitempty"`
		Source     string  `json:"source,omitempty"`
		Type       string  `json:"type,omitempty"`
		MemoryKind string  `json:"memory_kind,omitempty"`
		Since      *string `json:"since,omitempty"`
		Until      *string `json:"until,omitempty"`
		Limit      int     `json:"limit,omitempty"`
		MaxChars   int     `json:"max_chars,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeError(w, http.StatusBadRequest, "bad_query", "query is required")
		return
	}
	if _, err := pathx.Normalize(req.Scope); err != nil {
		writeError(w, http.StatusBadRequest, "bad_scope", err.Error())
		return
	}
	source := strings.ToLower(strings.TrimSpace(req.Source))
	switch source {
	case "", contextpack.SourceAll, contextpack.SourceFile, contextpack.SourceMemory:
	default:
		writeError(w, http.StatusBadRequest, "bad_source", "source must be one of all|file|memory")
		return
	}
	if source == contextpack.SourceMemory && req.Type != "" {
		writeError(w, http.StatusBadRequest, "bad_filter",
			"type filters file evidence and cannot be used with source=memory")
		return
	}
	if source == contextpack.SourceFile && req.MemoryKind != "" {
		writeError(w, http.StatusBadRequest, "bad_filter",
			"memory_kind cannot be used with source=file")
		return
	}
	var workspaceID uuid.UUID
	if ws, ok := r.Context().Value(ctxWorkspace).(*workspace.Workspace); ok && ws != nil {
		workspaceID = ws.ID
	}
	input := contextpack.Request{
		UserID:       u.ID,
		WorkspaceID:  workspaceID,
		Query:        req.Query,
		Scope:        req.Scope,
		AllowedPaths: tok.Paths,
		Source:       source,
		Type:         req.Type,
		MemoryKind:   req.MemoryKind,
		Limit:        req.Limit,
		MaxChars:     req.MaxChars,
	}
	if req.Since != nil {
		t, err := time.Parse("2006-01-02", *req.Since)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_since", err.Error())
			return
		}
		input.Since = &t
	}
	if req.Until != nil {
		t, err := time.Parse("2006-01-02", *req.Until)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_until", err.Error())
			return
		}
		t = t.Add(24*time.Hour - time.Nanosecond)
		input.Until = &t
	}
	var managed *managedSearchExecutor
	var pack *contextpack.Pack
	var retrievalErr error
	if source != contextpack.SourceMemory && s.DeploymentMode == "saas" {
		searchQuery := search.Query{
			UserID:     u.ID,
			Route:      search.RouteAuto,
			Type:       req.Type,
			PathPrefix: input.Scope,
			Limit:      req.Limit,
		}
		requestSearcher, managedRequest, managedErr := s.managedSearcher(
			r,
			"context.query",
			req,
			searchQuery,
		)
		if managedErr != nil {
			writeManagedEmbeddingError(w, managedErr)
			return
		}
		managed = managedRequest
		pack, retrievalErr = s.Context.BuildWithSearcher(r.Context(), input, requestSearcher)
	} else {
		pack, retrievalErr = s.Context.Build(r.Context(), input)
	}
	if managed != nil {
		if summary, ok, replayed := managed.result(); ok {
			setManagedUsageHeaders(w, summary, replayed)
		}
	}
	if retrievalErr != nil {
		if s.Log != nil {
			if managed != nil {
				// Do not persist provider-controlled error bodies in logs.
				s.Log.Error("context.managed_retrieve_failed")
			} else {
				s.Log.Error("context.retrieve_failed", "err", retrievalErr)
			}
		}
		if managed != nil {
			writeManagedEmbeddingError(w, retrievalErr)
			return
		}
		writeError(w, http.StatusBadGateway, "context_unavailable", "memory retrieval is temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusOK, pack)
}
