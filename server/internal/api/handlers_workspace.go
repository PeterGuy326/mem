package api

import (
	"net/http"

	"github.com/PeterGuy326/mem/server/internal/auth"
)

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxActor).(*auth.User)
	items, err := s.Workspace.List(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "workspace_list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": items})
}

func (s *Server) handleCurrentWorkspace(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentWorkspace(r))
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	ws := currentWorkspace(r)
	t := r.Context().Value(ctxToken).(*auth.Token)
	writeJSON(w, http.StatusOK, map[string]any{
		"deployment_mode":   s.DeploymentMode,
		"registration_mode": s.RegistrationMode,
		"workspace":         ws,
		"permissions": map[string]bool{
			"read":            auth.HasScope(t, auth.ScopeRead),
			"search":          auth.HasScope(t, auth.ScopeSearch),
			"write":           auth.HasScope(t, auth.ScopeWrite),
			"delete":          auth.HasScope(t, auth.ScopeDelete) && ws.Role != "member",
			"provider_read":   auth.HasScope(t, auth.ScopeRead),
			"provider_modify": auth.HasScope(t, auth.ScopeAdmin) && ws.Role != "member",
		},
	})
}
