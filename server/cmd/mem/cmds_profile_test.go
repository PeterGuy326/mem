package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProfileCommandsAreRegistered(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{
		{"profile", "list"},
		{"profile", "status"},
		{"profile", "select"},
	} {
		command, remaining, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %q: %v", path, err)
		}
		if len(remaining) != 0 || command.CommandPath() != "mem "+strings.Join(path, " ") {
			t.Fatalf("path %q resolved to %q with remaining %q", path, command.CommandPath(), remaining)
		}
	}
}

func TestProfileListAndStatusUseWorkspaceAIProfileEndpoint(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != workspaceAIProfilePath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer profile-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "profile-workspace" {
			t.Errorf("X-Workspace-ID = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) != 0 {
			t.Errorf("GET body = %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"active":{"id":"local-fast-v1","revision":"2026-07-29"},
			"available":[
				{"id":"local-fast-v1","revision":"2026-07-29"},
				{"id":"idealab-quality-v1","revision":"2026-06-08"}
			]
		}`)
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "profile-token", "profile-workspace")

	listRoot := newRootCmd()
	var listOutput bytes.Buffer
	listRoot.SetOut(&listOutput)
	listRoot.SetArgs([]string{"profile", "list", "--format", "json"})
	if err := listRoot.Execute(); err != nil {
		t.Fatal(err)
	}
	var listResponse workspaceAIProfileResponse
	if err := json.Unmarshal(listOutput.Bytes(), &listResponse); err != nil {
		t.Fatalf("list output is not JSON: %v\n%s", err, listOutput.String())
	}
	if got := workspaceAIProfileField(listResponse.Active, "id"); got != "local-fast-v1" {
		t.Fatalf("active ID = %q", got)
	}
	if len(listResponse.Available) != 2 ||
		workspaceAIProfileField(listResponse.Available[1], "id") != "idealab-quality-v1" {
		t.Fatalf("available = %#v", listResponse.Available)
	}

	statusRoot := newRootCmd()
	var statusOutput bytes.Buffer
	statusRoot.SetOut(&statusOutput)
	statusRoot.SetArgs([]string{"profile", "status"})
	if err := statusRoot.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := statusOutput.String(); !strings.Contains(got, "active") ||
		!strings.Contains(got, "local-fast-v1") ||
		!strings.Contains(got, "2026-07-29") {
		t.Fatalf("status output = %q", got)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("request count = %d, want 2", requestCount.Load())
	}
}

func TestProfileSelectSendsOnlyProfileID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != workspaceAIProfilePath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer select-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "select-workspace" {
			t.Errorf("X-Workspace-ID = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(body), `{"profile_id":"idealab-quality-v1"}`; got != want {
			t.Errorf("request body = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"active":{
				"id":"idealab-quality-v1",
				"revision":"qwen3.7-max-2026-06-08",
				"public_status":"ready"
			}
		}`)
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "select-token", "select-workspace")

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{
		"profile", "select", "idealab-quality-v1", "--format", "json",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var response workspaceAIProfileSelectResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("select output is not JSON: %v\n%s", err, output.String())
	}
	if got := workspaceAIProfileField(response.Active, "id"); got != "idealab-quality-v1" {
		t.Fatalf("active ID = %q", got)
	}
	if got := workspaceAIProfileField(response.Active, "revision"); got != "qwen3.7-max-2026-06-08" {
		t.Fatalf("revision = %q", got)
	}
}

func TestProfileSelectHasNoManagedProviderInputFlags(t *testing.T) {
	root := newRootCmd()
	command, remaining, err := root.Find([]string{"profile", "select"})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining = %q", remaining)
	}
	for _, flag := range []string{
		"api-key",
		"base-url",
		"credential",
		"key",
		"model",
		"provider",
	} {
		if command.Flags().Lookup(flag) != nil {
			t.Fatalf("profile select unexpectedly exposes --%s", flag)
		}
	}

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount.Add(1)
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	root = newRootCmd()
	root.SetArgs([]string{
		"profile", "select", "local-fast-v1", "--model", "untrusted-model",
	})
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("error = %v", err)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("forbidden flag made %d requests", requestCount.Load())
	}
}

func TestProfileSelectRejectsEmptyProfileIDBeforeRequest(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount.Add(1)
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	root := newRootCmd()
	root.SetArgs([]string{"profile", "select", "   "})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "profile ID must not be empty") {
		t.Fatalf("error = %v", err)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("empty profile ID made %d requests", requestCount.Load())
	}
}
