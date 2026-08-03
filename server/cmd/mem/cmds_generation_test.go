package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGenerationCommandsAreReadOnlyAndRegistered(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{
		{"generation", "list"},
		{"generation", "status"},
		{"generation", "events"},
	} {
		command, remaining, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %q: %v", path, err)
		}
		if len(remaining) != 0 || command.CommandPath() != "mem "+strings.Join(path, " ") {
			t.Fatalf("path %q resolved to %q with remaining %q", path, command.CommandPath(), remaining)
		}
	}
	command, _, err := root.Find([]string{"generation"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"create", "activate", "rollback", "discard", "cancel", "resume"} {
		for _, child := range command.Commands() {
			if child.Name() == forbidden {
				t.Fatalf("generation command unexpectedly exposes %q", forbidden)
			}
		}
	}
}

func TestGenerationListAndStatusExposeSafeIdentity(t *testing.T) {
	buildID := uuid.New()
	generationID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer generation-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "generation-workspace" {
			t.Errorf("X-Workspace-ID = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case workspaceIndexGenerationsPath:
			if r.URL.Query().Get("limit") != "25" {
				t.Errorf("limit = %q", r.URL.Query().Get("limit"))
			}
			_, _ = io.WriteString(w, `{"items":[{"id":"`+buildID.String()+`","profile_id":"local-fast-v2","profile_revision":"2026-07-30.1","pipeline_revision":"file-enrichment-v2","state":"building","required_targets":3,"succeeded_targets":1,"skipped_targets":1,"failed_targets":0,"allowed_mime_types":["text/*"],"quality_gate":{"mode":"all_targets"},"workspace_id":"`+uuid.NewString()+`","created_at":"2026-08-02T00:00:00Z","updated_at":"2026-08-02T00:00:00Z","generations":[]}],"execution_wired":false}`)
		case workspaceIndexGenerationsPath + "/" + buildID.String():
			_, _ = io.WriteString(w, `{"generation":{"id":"`+buildID.String()+`","workspace_id":"`+uuid.NewString()+`","profile_id":"local-fast-v2","profile_revision":"2026-07-30.1","pipeline_revision":"file-enrichment-v2","allowed_mime_types":["text/*"],"state":"ready","quality_gate":{"mode":"all_targets"},"required_targets":2,"succeeded_targets":2,"skipped_targets":0,"failed_targets":0,"created_at":"2026-08-02T00:00:00Z","updated_at":"2026-08-02T00:00:00Z","generations":[{"id":"`+generationID.String()+`","build_id":"`+buildID.String()+`","workspace_id":"`+uuid.NewString()+`","route_kind":"text","provider":"ollama","model_revision":"qwen3-embedding:0.6b","output_dimension":768,"pipeline_revision":"file-enrichment-v2","profile_id":"local-fast-v2","profile_revision":"2026-07-30.1","state":"ready","created_at":"2026-08-02T00:00:00Z","updated_at":"2026-08-02T00:00:00Z"}]},"execution_wired":false}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "generation-token", "generation-workspace")

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"generation", "list", "--limit", "25"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, buildID.String()) ||
		!strings.Contains(got, "2/3") || !strings.Contains(got, "local-fast-v2") {
		t.Fatalf("list output = %q", got)
	}

	root = newRootCmd()
	output.Reset()
	root.SetOut(&output)
	root.SetArgs([]string{"generation", "status", buildID.String()})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "ollama:qwen3-embedding:0.6b") ||
		!strings.Contains(got, "768 dimensions") || !strings.Contains(got, "file-enrichment-v2") {
		t.Fatalf("status output = %q", got)
	}
}

func TestGenerationStatusRejectsInvalidIDBeforeRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid generation ID made an HTTP request")
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	root := newRootCmd()
	root.SetArgs([]string{"generation", "status", "not-a-uuid"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "must be a UUID") {
		t.Fatalf("error = %v", err)
	}
}
