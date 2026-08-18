package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
)

// TestHandleCapabilitiesPermissionsManage pins the capability flag that lets
// the Web UI gate the permissions management surface: it mirrors the admin
// scope required by the token and durable-context allowlist endpoints and is
// independent of the caller's workspace role.
func TestHandleCapabilitiesPermissionsManage(t *testing.T) {
	cases := []struct {
		name   string
		scopes []string
		role   string
		want   bool
	}{
		{"admin scope exposes the flag", []string{auth.ScopeAdmin}, "owner", true},
		{"session-style full scope exposes the flag", auth.AllScopes, "owner", true},
		{"read-only token hides the flag", []string{auth.ScopeRead}, "owner", false},
		{"admin scope on a member role still exposes the flag",
			[]string{auth.ScopeAdmin}, "member", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{}
			request := workspaceTransferRequest(
				httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil),
				uuid.New(),
				tc.role,
				tc.scopes,
				nil,
			)
			recorder := httptest.NewRecorder()

			server.handleCapabilities(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Permissions map[string]bool `json:"permissions"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if got := response.Permissions["permissions_manage"]; got != tc.want {
				t.Fatalf("permissions_manage = %t, want %t", got, tc.want)
			}
		})
	}
}
