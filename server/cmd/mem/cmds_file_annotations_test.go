package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	commandFileAnnotationFileID = "9baadf78-6ad1-47a7-a719-57122f352a67"
	commandFileAnnotationID     = "441bcc02-9fe2-44bb-a68b-8dd9a190fb6e"
)

func TestFileAnnotationDecisionCommandsDelegateToCanonicalEndpoint(t *testing.T) {
	tests := []struct {
		command  string
		decision string
	}{
		{command: "accept", decision: "accepted"},
		{command: "reject", decision: "rejected"},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			var requestBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut ||
					r.URL.Path != "/v1/files/"+commandFileAnnotationFileID+
						"/annotations/"+commandFileAnnotationID {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
					t.Errorf("Authorization = %q", got)
				}
				if got := r.Header.Get("X-Workspace-ID"); got != "workspace-1" {
					t.Errorf("X-Workspace-ID = %q", got)
				}
				if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{
					"annotation":{
						"id":"`+commandFileAnnotationID+`",
						"file_id":"`+commandFileAnnotationFileID+`",
						"kind":"tag",
						"value_text":"travel",
						"status":"`+test.decision+`",
						"state_version":2
					},
					"replayed":false
				}`)
			}))
			defer server.Close()
			setFileAnnotationCommandTestConfig(t, server.URL)

			root := newRootCmd()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetArgs([]string{
				"annotation", test.command,
				commandFileAnnotationFileID,
				commandFileAnnotationID,
				"--expected-version", "1",
				"--format", "json",
			})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if requestBody["decision"] != test.decision ||
				requestBody["expected_version"] != float64(1) {
				t.Fatalf("request body = %#v", requestBody)
			}
			var response struct {
				Annotation struct {
					ID           string `json:"id"`
					Status       string `json:"status"`
					StateVersion int64  `json:"state_version"`
				} `json:"annotation"`
				Replayed bool `json:"replayed"`
			}
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatalf("output is not JSON: %v\n%s", err, output.String())
			}
			if response.Annotation.ID != commandFileAnnotationID ||
				response.Annotation.Status != test.decision ||
				response.Annotation.StateVersion != 2 ||
				response.Replayed {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestFileAnnotationDecisionCommandPreservesConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(
			w,
			`{"error":"annotation_decision_conflict","hint":"reload the file"}`,
		)
	}))
	defer server.Close()
	setFileAnnotationCommandTestConfig(t, server.URL)

	root := newRootCmd()
	root.SetArgs([]string{
		"annotation", "reject",
		commandFileAnnotationFileID,
		commandFileAnnotationID,
		"--expected-version", "2",
	})
	err := root.Execute()
	var cliErr *cliError
	if !errors.As(err, &cliErr) {
		t.Fatalf("error = %T %v, want *cliError", err, err)
	}
	if cliErr.code != 1 ||
		!strings.Contains(cliErr.msg, "annotation_decision_conflict") ||
		cliErr.hint != "reload the file" {
		t.Fatalf("CLI error = %#v", cliErr)
	}
}

func TestFileAnnotationDecisionCommandsRejectInvalidInputBeforeRequest(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	setFileAnnotationCommandTestConfig(t, server.URL)

	tests := [][]string{
		{
			"annotation", "accept",
			commandFileAnnotationFileID,
			commandFileAnnotationID,
			"--expected-version", "0",
		},
		{
			"annotation", "reject",
			"not-a-uuid",
			commandFileAnnotationID,
			"--expected-version", "1",
		},
		{
			"annotation", "accept",
			commandFileAnnotationFileID,
			commandFileAnnotationID,
			"--expected-version", "1",
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
		t.Fatalf("invalid input made %d request(s)", requestCount.Load())
	}
}

func setFileAnnotationCommandTestConfig(t *testing.T, server string) {
	t.Helper()
	previousServer := cliServerOverride
	previousWorkspace := cliWorkspaceOverride
	cliServerOverride = ""
	cliWorkspaceOverride = ""
	t.Cleanup(func() {
		cliServerOverride = previousServer
		cliWorkspaceOverride = previousWorkspace
	})
	t.Setenv("MEM_CONFIG", filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("MEM_SERVER", server)
	t.Setenv("MEM_TOKEN", "token-1")
	t.Setenv("MEM_WORKSPACE", "workspace-1")
}
