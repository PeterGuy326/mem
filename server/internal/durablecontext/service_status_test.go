package durablecontext

import (
	"testing"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/memory"
)

// TestDeriveGrantStatus pins the allowlist view-state mapping independently of
// PostgreSQL: revocation always wins, otherwise the granted memory lifecycle
// decides whether the approval still resumes context.
func TestDeriveGrantStatus(t *testing.T) {
	cases := []struct {
		name         string
		revoked      bool
		memoryStatus string
		want         string
	}{
		{"active grant on active memory", false, memory.StatusActive, GrantStatusActive},
		{"revoked grant on active memory", true, memory.StatusActive, GrantStatusRevoked},
		{"revoked grant wins over archived memory", true, memory.StatusArchived, GrantStatusRevoked},
		{"revoked grant wins over forgotten memory", true, memory.StatusForgotten, GrantStatusRevoked},
		{"archived memory reads as superseded", false, memory.StatusArchived, GrantStatusSuperseded},
		{"forgotten memory reads as forgotten", false, memory.StatusForgotten, GrantStatusForgotten},
		{"unknown lifecycle reads as active", false, "unexpected", GrantStatusActive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveGrantStatus(tc.revoked, tc.memoryStatus); got != tc.want {
				t.Fatalf("deriveGrantStatus(%t, %q) = %q, want %q",
					tc.revoked, tc.memoryStatus, got, tc.want)
			}
		})
	}
}

// TestListGrantViewsUnconfigured guards the nil-service contract shared with
// the rest of the service surface.
func TestListGrantViewsUnconfigured(t *testing.T) {
	var service *Service
	query := ListGrantsQuery{WorkspaceID: uuid.New()}
	if _, err := service.ListGrantViews(t.Context(), query); err == nil {
		t.Fatal("expected an error from an unconfigured service")
	}
}
