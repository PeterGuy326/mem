package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/auth"
)

const maxAIProfileSelectBodyBytes = 4 << 10

// handleGetAIProfile returns the selected immutable snapshot (if any) and
// the public, server-side allowlist. Neither shape has a place for a base URL,
// credential, prompt, source text, or provider response.
func (s *Server) handleGetAIProfile(w http.ResponseWriter, r *http.Request) {
	if s.AIProfiles == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_profile_disabled",
			"workspace AI profiles are not configured")
		return
	}
	ws := currentWorkspace(r)
	active, err := s.AIProfiles.Get(r.Context(), ws.ID)
	if err != nil && !errors.Is(err, aiprofile.ErrNotFound) {
		writeAIProfileError(w, err)
		return
	}
	if errors.Is(err, aiprofile.ErrNotFound) {
		active = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"active":    active,
		"available": s.AIProfiles.List(),
	})
}

type aiProfileSelectRequest struct {
	ProfileID string `json:"profile_id"`
}

// handleSelectAIProfile accepts exactly one catalog identifier. Strict JSON is
// intentional: ignoring an accidental `base_url`, `model`, or `api_key` field
// would make the control-plane boundary ambiguous and invite unsafe clients.
func (s *Server) handleSelectAIProfile(w http.ResponseWriter, r *http.Request) {
	if s.AIProfiles == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_profile_disabled",
			"workspace AI profiles are not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAIProfileSelectBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request aiProfileSelectRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "bad_ai_profile_request",
			"request must contain only profile_id")
		return
	}
	if err := requireJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "bad_ai_profile_request",
			"request must contain one JSON object")
		return
	}
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	if request.ProfileID == "" || len(request.ProfileID) > 64 {
		writeError(w, http.StatusBadRequest, "bad_ai_profile_request", "profile_id is required")
		return
	}

	actor := r.Context().Value(ctxActor).(*auth.User)
	selection, err := s.AIProfiles.Select(
		r.Context(),
		currentWorkspace(r).ID,
		actor.ID,
		request.ProfileID,
	)
	if err != nil {
		writeAIProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": selection})
}

func writeAIProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, aiprofile.ErrUnknownProfile),
		errors.Is(err, aiprofile.ErrProfileDisabled):
		writeError(w, http.StatusBadRequest, "unknown_ai_profile", "profile_id is not available")
	case errors.Is(err, aiprofile.ErrEmbeddingDimensionMismatch):
		writeError(w, http.StatusUnprocessableEntity, "ai_profile_embedding_dimension_mismatch",
			"profile embedding output does not match the required 768 dimensions")
	case errors.Is(err, aiprofile.ErrProfileIndexingInFlight):
		writeError(w, http.StatusConflict, "ai_profile_indexing_in_flight",
			"wait for pending or processing files before changing the workspace AI profile")
	case errors.Is(err, aiprofile.ErrProfileCorpusIdentityUnknown),
		errors.Is(err, aiprofile.ErrProfileCorpusMismatch):
		writeError(w, http.StatusConflict, "ai_profile_generation_required",
			"the existing text index must be rebuilt in a separate index generation before changing the workspace AI profile")
	case errors.Is(err, aiprofile.ErrProbeUnavailable),
		errors.Is(err, aiprofile.ErrEmbeddingProbeFailed),
		errors.Is(err, aiprofile.ErrManagedUsageUnavailable):
		writeError(w, http.StatusServiceUnavailable, "ai_profile_probe_unavailable",
			"profile embedding capability or managed usage accounting is not currently available")
	case errors.Is(err, aiprofile.ErrStoreUnavailable):
		writeError(w, http.StatusServiceUnavailable, "ai_profile_store_unavailable",
			"workspace AI profile store is not currently available")
	case errors.Is(err, aiprofile.ErrWorkspaceRequired),
		errors.Is(err, aiprofile.ErrActorRequired):
		writeError(w, http.StatusBadRequest, "bad_ai_profile_request", "workspace profile request is invalid")
	default:
		// Do not reflect datastore or provider diagnostics. They can contain
		// user-controlled model specs or upstream gateway details.
		writeError(w, http.StatusInternalServerError, "ai_profile_unavailable",
			"workspace AI profile operation could not be completed")
	}
}
