package api

import (
	"net/http"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/PeterGuy326/mem/server/internal/workspacetransfer"
)

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxActor).(*auth.User)
	items, err := s.Workspace.List(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "workspace_list_failed", err.Error())
		return
	}
	tok := r.Context().Value(ctxToken).(*auth.Token)
	if tok.WorkspaceID != nil {
		bound := items[:0]
		for _, item := range items {
			if item.ID == *tok.WorkspaceID {
				bound = append(bound, item)
				break
			}
		}
		items = bound
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": items})
}

func (s *Server) handleCurrentWorkspace(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentWorkspace(r))
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	ws := currentWorkspace(r)
	t := r.Context().Value(ctxToken).(*auth.Token)
	transferEnabled := s.WorkspaceTransfer != nil
	transferBaseAllowed := transferEnabled &&
		auth.HasScope(t, auth.ScopeAdmin) &&
		!tokenHasPathRestrictions(r) &&
		(ws.Role == "owner" || ws.Role == "admin")
	exportAllowed := transferBaseAllowed && auth.HasScope(t, auth.ScopeRead)
	importAllowed := transferBaseAllowed && auth.HasScope(t, auth.ScopeWrite)
	handoffVersions := []int{}
	if s.Handoff != nil {
		handoffVersions = append(handoffVersions, 1)
	}
	workspaceRestoreModes := []string{}
	workspaceBundleSchemaVersions := []int{}
	if transferEnabled {
		workspaceRestoreModes = append(
			workspaceRestoreModes,
			workspacetransfer.RestoreModeFresh,
		)
		workspaceBundleSchemaVersions = append(
			workspaceBundleSchemaVersions,
			workspacebundle.SchemaVersionV1,
			workspacebundle.CurrentSchemaVersion,
		)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deployment_mode":                  s.DeploymentMode,
		"registration_mode":                s.RegistrationMode,
		"workspace":                        ws,
		"workspace_restore_modes":          workspaceRestoreModes,
		"workspace_bundle_schema_versions": workspaceBundleSchemaVersions,
		"features": map[string]bool{
			"context":          s.Context != nil,
			"handoff":          s.Handoff != nil,
			"memory":           s.Memory != nil,
			"ask":              false,
			"workspace_export": transferEnabled,
			"workspace_import": transferEnabled,
		},
		"handoff_schema_versions": handoffVersions,
		"permissions": map[string]bool{
			"read":             auth.HasScope(t, auth.ScopeRead),
			"search":           auth.HasScope(t, auth.ScopeSearch),
			"write":            auth.HasScope(t, auth.ScopeWrite),
			"delete":           auth.HasScope(t, auth.ScopeDelete) && ws.Role != "member",
			"provider_read":    auth.HasScope(t, auth.ScopeRead),
			"provider_modify":  auth.HasScope(t, auth.ScopeAdmin) && ws.Role != "member",
			"workspace_export": exportAllowed,
			"workspace_import": importAllowed,
		},
	})
}
