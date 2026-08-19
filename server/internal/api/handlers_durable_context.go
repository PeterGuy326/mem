package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/durablecontext"
)

const maxDurableContextBodyBytes = 16 << 10

// writeDurableContextError maps contract errors to SPEC §8.2 envelopes. The
// storage-backed failures deliberately surface as 502 context_unavailable per
// SPEC §F5 so consumers can distinguish degradation from denial.
func (s *Server) writeDurableContextError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, durablecontext.ErrInvalidCommand):
		writeError(w, http.StatusBadRequest, "invalid_durable_context", err.Error())
	case errors.Is(err, durablecontext.ErrScopeDenied):
		writeError(w, http.StatusForbidden, "context_scope_denied",
			"no approved durable-context grant exists for this principal")
	case errors.Is(err, durablecontext.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found",
			"no such durable-context grant or memory")
	case errors.Is(err, durablecontext.ErrForgotten):
		writeError(w, http.StatusGone, "memory_forgotten",
			"the granted memory has been irreversibly forgotten")
	case errors.Is(err, durablecontext.ErrStale):
		writeError(w, http.StatusConflict, "context_stale",
			"the granted memory is archived; re-grant the current state")
	default:
		if s.Log != nil {
			s.Log.Error("durable_context.operation_failed", "err", err)
		}
		writeError(w, http.StatusBadGateway, "context_unavailable",
			"durable context is temporarily unavailable")
	}
}

// durableContextContract rejects any contract other than the pinned version
// so incompatible clients fail clearly instead of receiving unrequested
// semantics.
func (s *Server) durableContextContract(w http.ResponseWriter, contract string) bool {
	if contract == durablecontext.ContractVersion {
		return true
	}
	writeError(w, http.StatusBadRequest, "contract_unsupported",
		"contract must be exactly "+durablecontext.ContractVersion)
	return false
}

type durableContextRecallRequest struct {
	Contract   string `json:"contract"`
	Principal  string `json:"principal"`
	SessionRef string `json:"session_ref,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// handleDurableContextRecall resumes the approved, active context for one
// principal. Read scope only: the contract never writes.
func (s *Server) handleDurableContextRecall(w http.ResponseWriter, r *http.Request) {
	if s.DurableContext == nil {
		writeError(w, http.StatusServiceUnavailable, "durable_context_disabled",
			"durable context service not configured")
		return
	}
	tok := r.Context().Value(ctxToken).(*auth.Token)
	ws := currentWorkspace(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxDurableContextBodyBytes)
	var req durableContextRecallRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	if !s.durableContextContract(w, req.Contract) {
		return
	}
	result, err := s.DurableContext.Recall(r.Context(), durablecontext.RecallQuery{
		WorkspaceID:  ws.ID,
		Principal:    req.Principal,
		SessionRef:   req.SessionRef,
		AllowedPaths: tok.Paths,
		Limit:        req.Limit,
	})
	if err != nil {
		s.writeDurableContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleDurableContextGetMemory resolves one granted memory for one
// principal, preserving F5A.5: unapproved objects behave as nonexistent.
func (s *Server) handleDurableContextGetMemory(w http.ResponseWriter, r *http.Request) {
	if s.DurableContext == nil {
		writeError(w, http.StatusServiceUnavailable, "durable_context_disabled",
			"durable context service not configured")
		return
	}
	tok := r.Context().Value(ctxToken).(*auth.Token)
	ws := currentWorkspace(r)

	memoryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_memory_id", "memory id must be a UUID")
		return
	}
	query := r.URL.Query()
	if !s.durableContextContract(w, query.Get("contract")) {
		return
	}
	principal := strings.TrimSpace(query.Get("principal"))
	if principal == "" {
		writeError(w, http.StatusBadRequest, "bad_principal",
			"principal query parameter is required")
		return
	}
	hit, err := s.DurableContext.Get(r.Context(), durablecontext.GetQuery{
		WorkspaceID:  ws.ID,
		Principal:    principal,
		MemoryID:     memoryID,
		AllowedPaths: tok.Paths,
	})
	if err != nil {
		s.writeDurableContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hit)
}

type durableContextGrantRequest struct {
	Principal string `json:"principal"`
	MemoryID  string `json:"memory_id"`
}

// handleCreateDurableContextGrant approves one explicit read grant. Admin
// scope: recall allowlists are operator-owned policy, never self-granted.
func (s *Server) handleCreateDurableContextGrant(w http.ResponseWriter, r *http.Request) {
	if s.DurableContext == nil {
		writeError(w, http.StatusServiceUnavailable, "durable_context_disabled",
			"durable context service not configured")
		return
	}
	actor := r.Context().Value(ctxActor).(*auth.User)
	tok := r.Context().Value(ctxToken).(*auth.Token)
	ws := currentWorkspace(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxDurableContextBodyBytes)
	var req durableContextGrantRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	memoryID, err := uuid.Parse(strings.TrimSpace(req.MemoryID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_memory_id", "memory_id must be a UUID")
		return
	}
	actorID := actor.ID
	tokenID := tok.ID
	grant, err := s.DurableContext.Grant(r.Context(), durablecontext.GrantCommand{
		WorkspaceID:  ws.ID,
		Principal:    req.Principal,
		MemoryID:     memoryID,
		ActorUserID:  &actorID,
		ActorTokenID: &tokenID,
	})
	if err != nil {
		s.writeDurableContextError(w, err)
		return
	}
	w.Header().Set("Location", "/v1/durable-context/grants/"+grant.ID.String())
	writeJSON(w, http.StatusCreated, grant)
}

// handleListDurableContextGrants lists the workspace allowlist for audit.
// Items carry the grant fields plus a derived view status and the granted
// memory's lifecycle so owners can tell stale approvals from active ones.
func (s *Server) handleListDurableContextGrants(w http.ResponseWriter, r *http.Request) {
	if s.DurableContext == nil {
		writeError(w, http.StatusServiceUnavailable, "durable_context_disabled",
			"durable context service not configured")
		return
	}
	ws := currentWorkspace(r)
	query := r.URL.Query()
	limit := 0
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "bad_limit", "limit must be a non-negative integer")
			return
		}
		limit = parsed
	}
	grants, err := s.DurableContext.ListGrantViews(r.Context(), durablecontext.ListGrantsQuery{
		WorkspaceID: ws.ID,
		Principal:   query.Get("principal"),
		Limit:       limit,
	})
	if err != nil {
		s.writeDurableContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": grants})
}

// handleRevokeDurableContextGrant soft-revokes one grant; the audit row
// survives so a resumed session can be told why context stopped.
func (s *Server) handleRevokeDurableContextGrant(w http.ResponseWriter, r *http.Request) {
	if s.DurableContext == nil {
		writeError(w, http.StatusServiceUnavailable, "durable_context_disabled",
			"durable context service not configured")
		return
	}
	actor := r.Context().Value(ctxActor).(*auth.User)
	tok := r.Context().Value(ctxToken).(*auth.Token)
	ws := currentWorkspace(r)

	grantID, err := uuid.Parse(chi.URLParam(r, "grantID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_grant_id", "grant id must be a UUID")
		return
	}
	actorID := actor.ID
	tokenID := tok.ID
	grant, err := s.DurableContext.Revoke(r.Context(), durablecontext.RevokeCommand{
		WorkspaceID:  ws.ID,
		GrantID:      grantID,
		ActorUserID:  &actorID,
		ActorTokenID: &tokenID,
	})
	if err != nil {
		s.writeDurableContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}
