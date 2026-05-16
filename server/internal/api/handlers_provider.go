package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/provider"
)

// handleListProviders → GET /v1/providers
// Returns the user's saved provider settings (one row per kind, possibly
// empty) + a constant list of supported kinds for client UIs.
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Provider == nil {
		writeError(w, http.StatusServiceUnavailable, "provider_disabled", "provider service not configured")
		return
	}
	settings, err := s.Provider.List(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if settings == nil {
		settings = []provider.Setting{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings,
		"kinds":    provider.ValidKinds,
	})
}

// handleSetProvider → PUT /v1/providers/{kind}
// Body: { "spec": "<vendor>:<model>" }
// Side effects on embedding kind: probe dim, optional schema migration, reindex enqueue.
func (s *Server) handleSetProvider(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Provider == nil {
		writeError(w, http.StatusServiceUnavailable, "provider_disabled", "provider service not configured")
		return
	}
	kind := chi.URLParam(r, "kind")
	var body struct {
		Spec string `json:"spec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	res, err := s.Provider.Set(r.Context(), u.ID, kind, body.Spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, "set_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleTestProvider → POST /v1/providers/{kind}/test
// Body (optional): { "spec": "..." } — if omitted, tests the saved spec.
func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Provider == nil {
		writeError(w, http.StatusServiceUnavailable, "provider_disabled", "provider service not configured")
		return
	}
	kind := chi.URLParam(r, "kind")
	var body struct {
		Spec string `json:"spec,omitempty"`
	}
	// Body is optional — ignore decode errors when empty.
	_ = json.NewDecoder(r.Body).Decode(&body)

	out, err := s.Provider.Test(r.Context(), u.ID, kind, body.Spec)
	if err != nil {
		if errors.Is(err, provider.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found",
				"no provider setting for kind; PUT /v1/providers/"+kind+" with spec first")
			return
		}
		writeError(w, http.StatusBadRequest, "test_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
