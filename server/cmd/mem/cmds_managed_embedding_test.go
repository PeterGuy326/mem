package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
)

func TestSearchAndContextCommandsAttachManagedRequestKey(t *testing.T) {
	var requests []struct {
		path string
		key  string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, struct {
			path string
			key  string
		}{path: r.URL.Path, key: r.Header.Get("Idempotency-Key")})
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/search":
			_, _ = io.WriteString(w, `{"results":[],"_meta":{}}`)
		case "/v1/context":
			_, _ = io.WriteString(w, `{
				"query":"decision",
				"scope":"/",
				"source":"memory",
				"evidence":[],
				"total_chars":0,
				"partial":false,
				"retrieved_at":"2026-07-28T00:00:00Z"
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("MEM_CONFIG", filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("MEM_SERVER", server.URL)
	t.Setenv("MEM_TOKEN", "token-1")
	t.Setenv("MEM_WORKSPACE", "workspace-1")

	searchRoot := newRootCmd()
	searchRoot.SetOut(&bytes.Buffer{})
	searchRoot.SetArgs([]string{
		"search", "decision",
		"--idempotency-key", "cli-search-request-1",
	})
	if err := searchRoot.Execute(); err != nil {
		t.Fatal(err)
	}

	contextRoot := newRootCmd()
	contextRoot.SetOut(&bytes.Buffer{})
	contextRoot.SetArgs([]string{
		"context", "decision",
		"--source", "memory",
		"--idempotency-key", "cli-context-request-1",
	})
	if err := contextRoot.Execute(); err != nil {
		t.Fatal(err)
	}

	if len(requests) != 2 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].path != "/v1/search" || requests[0].key != "cli-search-request-1" {
		t.Fatalf("search request = %#v", requests[0])
	}
	if requests[1].path != "/v1/context" || requests[1].key != "cli-context-request-1" {
		t.Fatalf("context request = %#v", requests[1])
	}
}

func TestManagedRequestKeyGeneratesAndBoundsKeys(t *testing.T) {
	generated, err := managedRequestKey("search", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) == 0 || len(generated) > 200 {
		t.Fatalf("generated key = %q", generated)
	}
	if _, err := managedRequestKey("search", string(make([]byte, 201))); err == nil {
		t.Fatal("expected oversized key error")
	}
}

func TestManagedAPIErrorExitKinds(t *testing.T) {
	tests := []struct {
		status int
		code   int
	}{
		{status: http.StatusPaymentRequired, code: 4},
		{status: http.StatusTooManyRequests, code: 4},
		{status: http.StatusBadGateway, code: 5},
		{status: http.StatusGatewayTimeout, code: 5},
	}
	for _, test := range tests {
		got := fromAPIError(&apiclient.APIError{
			StatusCode: test.status,
			Message:    http.StatusText(test.status),
		})
		var cliErr *cliError
		if !errors.As(got, &cliErr) {
			t.Fatalf("status %d: error = %T", test.status, got)
		}
		if cliErr.code != test.code {
			t.Fatalf("status %d: exit code = %d, want %d", test.status, cliErr.code, test.code)
		}
	}
}
