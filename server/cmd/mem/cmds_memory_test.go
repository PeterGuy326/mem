package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const commandLifecycleMemoryID = "9baadf78-6ad1-47a7-a719-57122f352a67"

func TestMemoriesCommandMapsFiltersAndWritesJSONToCommandOutput(t *testing.T) {
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"memories":[{
				"id":"`+commandLifecycleMemoryID+`",
				"kind":"decision",
				"excerpt":"Use PostgreSQL",
				"path":"/Projects/mem",
				"lifecycle_status":"active",
				"state_version":2
			}],
			"next_cursor":"next/+ ="
		}`)
	}))
	defer server.Close()
	setMemoryCommandTestConfig(t, server.URL)

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{
		"memories",
		"--scope", "/Projects/mem α",
		"--recursive=false",
		"--kind", "decision",
		"--kind", "fact",
		"--lifecycle", "all",
		"--pinned=false",
		"--limit", "25",
		"--cursor", "cursor/+ =",
		"--format", "json",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("scope") != "/Projects/mem α" ||
		gotQuery.Get("recursive") != "false" ||
		gotQuery.Get("lifecycle") != "all" ||
		gotQuery.Get("pinned") != "false" ||
		gotQuery.Get("limit") != "25" ||
		gotQuery.Get("cursor") != "cursor/+ =" {
		t.Fatalf("query = %#v", gotQuery)
	}
	if kinds := gotQuery["kind"]; len(kinds) != 2 ||
		kinds[0] != "decision" || kinds[1] != "fact" {
		t.Fatalf("kinds = %#v", kinds)
	}
	var response map[string]any
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, output.String())
	}
	if response["next_cursor"] != "next/+ =" {
		t.Fatalf("response = %#v", response)
	}
}

func TestMemoryCommandGetsScopedDetailAndEscapesText(t *testing.T) {
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet ||
			r.URL.Path != "/v1/memories/"+commandLifecycleMemoryID {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"`+commandLifecycleMemoryID+`",
			"workspace_id":"11111111-1111-4111-8111-111111111111",
			"kind":"decision",
			"content":"Use immutable checkpoints\u001b]0;spoof\u0007\nnext",
			"attributes":{"priority":"high"},
			"path":"/Projects/mem",
			"source_type":"agent",
			"source_ref":"agent://codex/task-42",
			"source_file_id":"22222222-2222-4222-8222-222222222222",
			"source_locator":{"line":"42\u001b]0;locator\u0007"},
			"producer_agent":"codex",
			"producer_session":"session-42",
			"content_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"lifecycle_status":"active",
			"state_version":3,
			"created_at":"2026-07-28T12:00:00Z",
			"updated_at":"2026-07-28T12:00:00Z",
			"citation":"mem://memories/`+commandLifecycleMemoryID+`",
			"provenance":{
				"workspace_id":"11111111-1111-4111-8111-111111111111",
				"created_by_user_id":"33333333-3333-4333-8333-333333333333",
				"source_type":"agent",
				"source_ref":"agent://codex/task-42",
				"source_file_id":"22222222-2222-4222-8222-222222222222",
				"source_locator":{"line":"42\u001b]0;locator\u0007"},
				"producer_agent":"codex",
				"producer_session":"session-42"
			}
		}`)
	}))
	defer server.Close()
	setMemoryCommandTestConfig(t, server.URL)

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{
		"memory",
		commandLifecycleMemoryID,
		"--scope", "/Projects/mem α",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("scope") != "/Projects/mem α" {
		t.Fatalf("query = %#v", gotQuery)
	}
	got := output.String()
	for _, want := range []string{
		"memory_id",
		commandLifecycleMemoryID,
		"citation",
		"mem://memories/" + commandLifecycleMemoryID,
		"workspace_id",
		"11111111-1111-4111-8111-111111111111",
		"source_file_id",
		"22222222-2222-4222-8222-222222222222",
		"producer_agent",
		"codex",
		`42\u001b]0;locator\u0007`,
		"state_version",
		"3",
		`Use immutable checkpoints\x1b]0;spoof\x07\x0anext`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\a') {
		t.Fatalf("text output contains a terminal control: %q", got)
	}
}

func TestMemoryMutationCommandsMapBodiesAndIdempotency(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		response   string
		wantAction string
		wantReason string
	}{
		{
			name: "feedback",
			args: []string{
				"feedback", commandLifecycleMemoryID,
				"--action", "pin",
				"--expected-version", "2",
				"--idempotency-key", "feedback-key",
				"--format", "json",
			},
			response:   `{"memory":{"id":"` + commandLifecycleMemoryID + `","state_version":3},"event":{},"replayed":false}`,
			wantAction: "pin",
		},
		{
			name: "archive",
			args: []string{
				"archive", commandLifecycleMemoryID,
				"--expected-version", "2",
				"--idempotency-key", "archive-key",
				"--format", "json",
			},
			response: `{"memory":{"id":"` + commandLifecycleMemoryID + `","state_version":3},"event":{},"replayed":false}`,
		},
		{
			name: "restore",
			args: []string{
				"restore", commandLifecycleMemoryID,
				"--expected-version", "2",
				"--idempotency-key", "restore-key",
				"--format", "json",
			},
			response: `{"memory":{"id":"` + commandLifecycleMemoryID + `","state_version":3},"event":{},"replayed":true}`,
		},
		{
			name: "forget",
			args: []string{
				"forget", commandLifecycleMemoryID,
				"--expected-version", "2",
				"--reason", "sensitive",
				"--idempotency-key", "forget-key",
				"--yes",
				"--format", "json",
			},
			response: `{
				"tombstone":{
					"id":"` + commandLifecycleMemoryID + `",
					"forgotten_at":"2026-07-28T12:00:00Z"
				},
				"event":{},
				"replayed":false
			}`,
			wantReason: "sensitive",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				gotHeader string
				gotBody   map[string]any
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost ||
					r.URL.Path != "/v1/memories/"+commandLifecycleMemoryID+"/"+test.name {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				gotHeader = r.Header.Get("Idempotency-Key")
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			setMemoryCommandTestConfig(t, server.URL)

			root := newRootCmd()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetArgs(test.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if gotHeader != test.name+"-key" {
				t.Fatalf("Idempotency-Key = %q", gotHeader)
			}
			if gotBody["expected_version"] != float64(2) {
				t.Fatalf("body = %#v", gotBody)
			}
			if test.wantAction != "" && gotBody["action"] != test.wantAction {
				t.Fatalf("action body = %#v", gotBody)
			}
			if test.wantReason != "" && gotBody["reason"] != test.wantReason {
				t.Fatalf("reason body = %#v", gotBody)
			}
			var response map[string]any
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatalf("output is not JSON: %v\n%s", err, output.String())
			}
		})
	}
}

func TestForgetAndInvalidFormatFailBeforeNetwork(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	setMemoryCommandTestConfig(t, server.URL)

	tests := [][]string{
		{
			"forget", commandLifecycleMemoryID,
			"--expected-version", "2",
			"--idempotency-key", "forget-no-confirm",
		},
		{
			"feedback", commandLifecycleMemoryID,
			"--action", "useful",
			"--expected-version", "2",
			"--idempotency-key", "feedback-invalid-format",
			"--format", "yaml",
		},
	}
	for _, args := range tests {
		root := newRootCmd()
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Fatalf("expected local validation error for %v", args)
		}
	}
	if requestCount.Load() != 0 {
		t.Fatalf("unsafe validation made %d request(s)", requestCount.Load())
	}
}

func TestContextUsesCommandOutputAndRejectsInvalidFormatLocally(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.URL.Path != "/v1/context" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"query":"database",
			"scope":"/",
			"source":"memory",
			"evidence":[{
				"evidence_id":"memory-1",
				"source_kind":"memory",
				"source_id":"`+commandLifecycleMemoryID+`",
				"citation":"mem://memories/`+commandLifecycleMemoryID+`",
				"memory_kind":"decision",
				"path":"/Projects/mem",
				"excerpt":"Use PostgreSQL",
				"score":1,
				"route":"lexical"
			}],
			"total_chars":14,
			"partial":false,
			"retrieved_at":"2026-07-28T12:00:00Z"
		}`)
	}))
	defer server.Close()
	setMemoryCommandTestConfig(t, server.URL)

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"context", "database", "--source", "memory"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "mem://memories/") ||
		!strings.Contains(output.String(), "Use PostgreSQL") {
		t.Fatalf("context output = %q", output.String())
	}

	root = newRootCmd()
	root.SetArgs([]string{"context", "database", "--format", "yaml"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected invalid format error")
	}
	if requestCount.Load() != 1 {
		t.Fatalf("invalid format made a request; count = %d", requestCount.Load())
	}
}

func TestMemoryTextOutputEscapesTerminalControlSequences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"memories":[{
				"id":"`+commandLifecycleMemoryID+`",
				"kind":"note",
				"excerpt":"safe\u001b]52;c;QUJD\u0007\nFAKE",
				"path":"/Shared/x\u001b[2J",
				"lifecycle_status":"active",
				"state_version":1
			}]
		}`)
	}))
	defer server.Close()
	setMemoryCommandTestConfig(t, server.URL)

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"memories"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\a') {
		t.Fatalf("text output contains a terminal control: %q", got)
	}
	if !strings.Contains(got, `\x1b]52;c;QUJD\x07`) ||
		!strings.Contains(got, `/Shared/x\x1b[2J`) {
		t.Fatalf("text output did not visibly escape controls: %q", got)
	}
	if strings.Contains(got, "\nFAKE") {
		t.Fatalf("excerpt injected a new output line: %q", got)
	}
}

func TestContextTextOutputEscapesTerminalControlSequences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"query":"database",
			"scope":"/",
			"source":"memory",
			"evidence":[{
				"evidence_id":"memory-1",
				"source_kind":"memory",
				"source_id":"`+commandLifecycleMemoryID+`",
				"citation":"mem://memories/`+commandLifecycleMemoryID+`\u001b[2J",
				"memory_kind":"decision",
				"path":"/Projects/mem",
				"excerpt":"trusted\u001b]0;spoof\u0007",
				"score":1,
				"route":"lexical"
			}],
			"total_chars":14,
			"partial":false,
			"retrieved_at":"2026-07-28T12:00:00Z"
		}`)
	}))
	defer server.Close()
	setMemoryCommandTestConfig(t, server.URL)

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"context", "database", "--source", "memory"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\a') {
		t.Fatalf("context output contains a terminal control: %q", got)
	}
	if !strings.Contains(got, `\x1b[2J`) ||
		!strings.Contains(got, `trusted\x1b]0;spoof\x07`) {
		t.Fatalf("context output did not visibly escape controls: %q", got)
	}
}

func setMemoryCommandTestConfig(t *testing.T, serverURL string) {
	t.Helper()
	oldServer := cliServerOverride
	oldWorkspace := cliWorkspaceOverride
	cliServerOverride = ""
	cliWorkspaceOverride = ""
	t.Cleanup(func() {
		cliServerOverride = oldServer
		cliWorkspaceOverride = oldWorkspace
	})
	t.Setenv("MEM_CONFIG", filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("MEM_SERVER", serverURL)
	t.Setenv("MEM_TOKEN", "memory-command-token")
	t.Setenv("MEM_WORKSPACE", "memory-command-workspace")
}
