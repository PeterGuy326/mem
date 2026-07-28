package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

func TestMemoryControlRoutesAreRegistered(t *testing.T) {
	t.Parallel()

	routes, ok := (&Server{}).Router().(chi.Routes)
	if !ok {
		t.Fatal("Server.Router did not return chi.Routes")
	}

	registered := make(map[string]bool)
	if err := chi.Walk(routes, func(
		method string,
		route string,
		_ http.Handler,
		_ ...func(http.Handler) http.Handler,
	) error {
		registered[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}

	for _, route := range []string{
		http.MethodGet + " /v1/memories",
		http.MethodGet + " /v1/memories/{id}",
		http.MethodPost + " /v1/memories/{id}/feedback",
		http.MethodPost + " /v1/memories/{id}/archive",
		http.MethodPost + " /v1/memories/{id}/restore",
		http.MethodPost + " /v1/memories/{id}/forget",
	} {
		if !registered[route] {
			t.Errorf("memory route %q is not registered", route)
		}
	}
}

func TestMemoryRouteAuthorizationPolicies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		route      string
		scopes     []string
		role       string
		wantStatus int
		wantHint   string
	}{
		{
			name:       "list accepts read",
			route:      "list",
			scopes:     []string{auth.ScopeRead},
			role:       workspace.RoleMember,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "list rejects write without read",
			route:      "list",
			scopes:     []string{auth.ScopeWrite},
			role:       workspace.RoleMember,
			wantStatus: http.StatusForbidden,
			wantHint:   "missing scope: read",
		},
		{
			name:       "get accepts read",
			route:      "get",
			scopes:     []string{auth.ScopeRead},
			role:       workspace.RoleMember,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "get rejects write without read",
			route:      "get",
			scopes:     []string{auth.ScopeWrite},
			role:       workspace.RoleMember,
			wantStatus: http.StatusForbidden,
			wantHint:   "missing scope: read",
		},
		{
			name:       "feedback accepts read and write",
			route:      "feedback",
			scopes:     []string{auth.ScopeRead, auth.ScopeWrite},
			role:       workspace.RoleMember,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "feedback rejects write without read",
			route:      "feedback",
			scopes:     []string{auth.ScopeWrite},
			role:       workspace.RoleMember,
			wantStatus: http.StatusForbidden,
			wantHint:   "missing scope: read",
		},
		{
			name:       "feedback rejects read without write",
			route:      "feedback",
			scopes:     []string{auth.ScopeRead},
			role:       workspace.RoleMember,
			wantStatus: http.StatusForbidden,
			wantHint:   "missing scope: write",
		},
		{
			name:       "archive accepts read and write",
			route:      "archive",
			scopes:     []string{auth.ScopeRead, auth.ScopeWrite},
			role:       workspace.RoleMember,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "archive rejects write without read",
			route:      "archive",
			scopes:     []string{auth.ScopeWrite},
			role:       workspace.RoleMember,
			wantStatus: http.StatusForbidden,
			wantHint:   "missing scope: read",
		},
		{
			name:       "archive rejects read without write",
			route:      "archive",
			scopes:     []string{auth.ScopeRead},
			role:       workspace.RoleMember,
			wantStatus: http.StatusForbidden,
			wantHint:   "missing scope: write",
		},
		{
			name:       "restore accepts read and write",
			route:      "restore",
			scopes:     []string{auth.ScopeRead, auth.ScopeWrite},
			role:       workspace.RoleMember,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "restore rejects write without read",
			route:      "restore",
			scopes:     []string{auth.ScopeWrite},
			role:       workspace.RoleMember,
			wantStatus: http.StatusForbidden,
			wantHint:   "missing scope: read",
		},
		{
			name:       "restore rejects read without write",
			route:      "restore",
			scopes:     []string{auth.ScopeRead},
			role:       workspace.RoleMember,
			wantStatus: http.StatusForbidden,
			wantHint:   "missing scope: write",
		},
		{
			name:       "admin scope satisfies read and write",
			route:      "feedback",
			scopes:     []string{auth.ScopeAdmin},
			role:       workspace.RoleMember,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "forget accepts delete for owner",
			route:      "forget",
			scopes:     []string{auth.ScopeDelete},
			role:       workspace.RoleOwner,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "forget accepts delete for workspace admin",
			route:      "forget",
			scopes:     []string{auth.ScopeDelete},
			role:       workspace.RoleAdmin,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "forget rejects member even with delete",
			route:      "forget",
			scopes:     []string{auth.ScopeDelete},
			role:       workspace.RoleMember,
			wantStatus: http.StatusForbidden,
			wantHint:   "workspace role does not allow delete",
		},
		{
			name:       "forget rejects owner without delete",
			route:      "forget",
			scopes:     []string{auth.ScopeRead, auth.ScopeWrite},
			role:       workspace.RoleOwner,
			wantStatus: http.StatusForbidden,
			wantHint:   "missing scope: delete",
		},
		{
			name:       "admin token cannot bypass member role",
			route:      "forget",
			scopes:     []string{auth.ScopeAdmin},
			role:       workspace.RoleMember,
			wantStatus: http.StatusForbidden,
			wantHint:   "workspace role does not allow delete",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := &Server{}
			handlerCalls := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalls++
				w.WriteHeader(http.StatusNoContent)
			})
			handler := memoryAuthorizationChainForTest(server, test.route, next)
			request := memoryAuthorizationRequest(test.route, test.scopes, test.role)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d, body = %s",
					recorder.Code,
					test.wantStatus,
					recorder.Body.String(),
				)
			}
			wantCalls := 0
			if test.wantStatus == http.StatusNoContent {
				wantCalls = 1
			}
			if handlerCalls != wantCalls {
				t.Fatalf("downstream handler calls = %d, want %d", handlerCalls, wantCalls)
			}
			if test.wantHint != "" && !strings.Contains(recorder.Body.String(), test.wantHint) {
				t.Fatalf("response body %q does not contain %q", recorder.Body.String(), test.wantHint)
			}
		})
	}
}

// memoryAuthorizationChainForTest mirrors the middleware sequences declared
// on the memory routes in Server.Router. The concrete auth and workspace
// services are PostgreSQL-backed, so this test isolates the route policies
// after authentication; TestMemoryControlRoutesAreRegistered separately walks
// the actual Router to catch method or path registration regressions.
func memoryAuthorizationChainForTest(
	server *Server,
	route string,
	next http.Handler,
) http.Handler {
	switch route {
	case "list", "get":
		return server.requireScope(auth.ScopeRead)(next)
	case "feedback", "archive", "restore":
		return server.requireScope(auth.ScopeRead)(
			server.requireScope(auth.ScopeWrite)(next),
		)
	case "forget":
		return server.requireScope(auth.ScopeDelete)(
			server.requireWorkspaceDelete(next),
		)
	default:
		panic("unknown memory route: " + route)
	}
}

func memoryAuthorizationRequest(
	route string,
	scopes []string,
	role string,
) *http.Request {
	method := http.MethodPost
	path := "/v1/memories/memory-id/" + route
	switch route {
	case "list":
		method = http.MethodGet
		path = "/v1/memories"
	case "get":
		method = http.MethodGet
		path = "/v1/memories/memory-id"
	}
	request := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(request.Context(), ctxToken, &auth.Token{Scopes: scopes})
	ctx = context.WithValue(ctx, ctxWorkspace, &workspace.Workspace{Role: role})
	return request.WithContext(ctx)
}
