package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestRememberCommandPostsContractAndHeader(t *testing.T) {
	var (
		gotHeader    http.Header
		gotBody      []byte
		requestCount atomic.Int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/memories" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"memory":{"id":"mem-1"},"replayed":false}`))
	}))
	defer srv.Close()

	t.Setenv("MEM_CONFIG", filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("MEM_SERVER", srv.URL)
	t.Setenv("MEM_TOKEN", "token-1")
	t.Setenv("MEM_WORKSPACE", "workspace-1")

	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{
		"remember", "Use PostgreSQL for lexical recall",
		"--kind", "decision",
		"--path", "/Projects/mem",
		"--idempotency-key", "task-42-db-decision",
		"--event-at", "2026-07-28T12:34:56Z",
		"--source-ref", "agent://codex/task-42",
		"--source-file-id", "file-1",
		"--source-locator", `{"kind":"paragraph","index":3}`,
		"--agent-id", "codex",
		"--session-id", "session-7",
		"--task-id", "task-42",
		"--attributes", `{"confidence":"confirmed"}`,
		"--format", "json",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("request count = %d", requestCount.Load())
	}
	if got := gotHeader.Get("Idempotency-Key"); got != "task-42-db-decision" {
		t.Fatalf("Idempotency-Key = %q", got)
	}
	if got := gotHeader.Get("Authorization"); got != "Bearer token-1" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := gotHeader.Get("X-Workspace-ID"); got != "workspace-1" {
		t.Fatalf("X-Workspace-ID = %q", got)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["kind"] != "decision" ||
		body["content"] != "Use PostgreSQL for lexical recall" ||
		body["path"] != "/Projects/mem" {
		t.Fatalf("body = %#v", body)
	}
	if _, exists := body["idempotency_key"]; exists {
		t.Fatalf("idempotency key must not be in body: %#v", body)
	}
	source, ok := body["source"].(map[string]any)
	if !ok || source["type"] != "agent" || source["file_id"] != "file-1" {
		t.Fatalf("source = %#v", body["source"])
	}
	producer, ok := body["producer"].(map[string]any)
	if !ok || producer["agent_id"] != "codex" || producer["task_id"] != "task-42" {
		t.Fatalf("producer = %#v", body["producer"])
	}

	var response map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if response["replayed"] != false {
		t.Fatalf("response = %#v", response)
	}
}

func TestRememberCommandRejectsInvalidFormatBeforeWriting(t *testing.T) {
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	t.Setenv("MEM_CONFIG", filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("MEM_SERVER", srv.URL)
	t.Setenv("MEM_TOKEN", "token-1")

	root := newRootCmd()
	root.SetArgs([]string{
		"remember", "do not write",
		"--kind", "observation",
		"--path", "/Tests",
		"--idempotency-key", "invalid-format-test",
		"--format", "yaml",
	})
	if err := root.Execute(); err == nil {
		t.Fatal("expected invalid format error")
	}
	if requestCount.Load() != 0 {
		t.Fatalf("invalid output format made %d write request(s)", requestCount.Load())
	}
}
