package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetEntitlementSummary(t *testing.T) {
	var gotWorkspace string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotWorkspace = r.Header.Get("X-Workspace-ID")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/entitlements/current" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"deployment_mode":"saas",
			"commercial_gate":true,
			"upgrade_required":false,
			"managed_embedding":{
				"workspace_id":"00000000-0000-0000-0000-000000000001",
				"plan":"pro",
				"status":"active",
				"qualifying":true,
				"managed_embedding_unit_limit":100,
				"managed_embedding_units_reserved":2,
				"managed_embedding_units_consumed":7,
				"managed_embedding_units_remaining":91,
				"period_start":"2026-07-01T00:00:00Z",
				"reset_at":"2026-08-01T00:00:00Z"
			}
		}`))
	}))
	defer server.Close()

	summary, err := New(server.URL, "token").WithWorkspace("workspace-1").
		GetEntitlementSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotWorkspace != "workspace-1" {
		t.Fatalf("X-Workspace-ID = %q", gotWorkspace)
	}
	if summary.DeploymentMode != "saas" ||
		summary.ManagedEmbedding == nil ||
		summary.ManagedEmbedding.Remaining != 91 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestAPIErrorManagedEmbeddingKinds(t *testing.T) {
	tests := []struct {
		status int
		want   ErrorKind
	}{
		{status: http.StatusPaymentRequired, want: KindPlan},
		{status: http.StatusTooManyRequests, want: KindQuota},
		{status: http.StatusBadGateway, want: KindProvider},
		{status: http.StatusServiceUnavailable, want: KindProvider},
		{status: http.StatusGatewayTimeout, want: KindTimeout},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			got := (&APIError{StatusCode: test.status}).Kind()
			if got != test.want {
				t.Fatalf("Kind() = %v, want %v", got, test.want)
			}
		})
	}
}
