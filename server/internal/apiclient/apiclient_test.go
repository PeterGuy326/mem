package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientAttachesWorkspaceHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Workspace-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var out map[string]any
	err := New(srv.URL, "token").WithWorkspace("workspace-123").
		DoJSON(context.Background(), http.MethodGet, "/v1/test", nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	if got != "workspace-123" {
		t.Fatalf("X-Workspace-ID = %q", got)
	}
}

func TestDoJSONWithHeadersAttachesIdempotencyKey(t *testing.T) {
	var (
		gotKey       string
		gotAuth      string
		gotWorkspace string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		gotAuth = r.Header.Get("Authorization")
		gotWorkspace = r.Header.Get("X-Workspace-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var out map[string]any
	err := New(srv.URL, "token").WithWorkspace("workspace-123").DoJSONWithHeaders(
		context.Background(),
		http.MethodPost,
		"/v1/memories",
		map[string]any{"content": "remember this"},
		&out,
		map[string]string{"Idempotency-Key": "retry-key-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "retry-key-1" {
		t.Fatalf("Idempotency-Key = %q", gotKey)
	}
	if gotAuth != "Bearer token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotWorkspace != "workspace-123" {
		t.Fatalf("X-Workspace-ID = %q", gotWorkspace)
	}
}
