package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
)

func TestWorkspaceExportWritesAtomically(t *testing.T) {
	const bundle = "portable-workspace-bundle"
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		requestCount.Add(1)
		if r.Method != http.MethodGet ||
			r.URL.Path != "/v1/workspaces/current/export" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer export-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "export-workspace" {
			t.Errorf("X-Workspace-ID = %q", got)
		}
		w.Header().Set("Content-Type", apiclient.WorkspaceBundleMediaType)
		w.Header().Set("Content-Length", "25")
		_, _ = io.WriteString(w, bundle)
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "export-token", "export-workspace")

	output := filepath.Join(t.TempDir(), "workspace.membundle")
	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{
		"workspace", "export",
		"--output", output,
		"--format", "json",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("request count = %d", requestCount.Load())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != bundle {
		t.Fatalf("file = %q", data)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %#o", info.Mode().Perm())
	}
	var result struct {
		Output      string `json:"output"`
		Bytes       int64  `json:"bytes"`
		ContentType string `json:"content_type"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if result.Output != output ||
		result.Bytes != int64(len(bundle)) ||
		result.ContentType != apiclient.WorkspaceBundleMediaType {
		t.Fatalf("result = %+v", result)
	}
	leftovers, err := filepath.Glob(
		filepath.Join(filepath.Dir(output), "."+filepath.Base(output)+".*.tmp"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
}

func TestWorkspaceExportDoesNotOverwriteWithoutForce(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", apiclient.WorkspaceBundleMediaType)
		_, _ = io.WriteString(w, "replacement")
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	output := filepath.Join(t.TempDir(), "existing.membundle")
	if err := os.WriteFile(output, []byte("keep-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	root.SetArgs([]string{"workspace", "export", "--output", output})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %v", err)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("request count = %d, want 0", requestCount.Load())
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "keep-me" {
		t.Fatalf("existing file changed: %q", data)
	}
}

func TestWorkspaceExportForceReplacesExistingFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", apiclient.WorkspaceBundleMediaType)
		w.Header().Set("Content-Length", "11")
		_, _ = io.WriteString(w, "replacement")
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	output := filepath.Join(t.TempDir(), "existing.membundle")
	if err := os.WriteFile(output, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	root.SetArgs([]string{
		"workspace", "export",
		"--output", output,
		"--force",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "replacement" {
		t.Fatalf("file = %q", data)
	}
}

func TestWorkspaceExportRemovesPartialDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", apiclient.WorkspaceBundleMediaType)
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "partial")
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	output := filepath.Join(t.TempDir(), "partial.membundle")
	root := newRootCmd()
	root.SetArgs([]string{"workspace", "export", "--output", output})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected partial download error")
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("partial output exists: %v", statErr)
	}
	leftovers, globErr := filepath.Glob(
		filepath.Join(filepath.Dir(output), "."+filepath.Base(output)+".*.tmp"),
	)
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
}

func TestWorkspaceImportRequiresYesWithoutTTY(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		requestCount.Add(1)
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	input := writeWorkspaceTestBundle(t, "bundle")
	root := newRootCmd()
	root.SetIn(strings.NewReader("yes\n"))
	root.SetArgs([]string{"workspace", "import", "--input", input})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes is required") {
		t.Fatalf("error = %v", err)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("request count = %d", requestCount.Load())
	}
}

func TestWorkspaceImportUploadsFreshBundleAndPrintsCounts(t *testing.T) {
	const bundle = "workspace-archive"
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.Method != http.MethodPost ||
			r.URL.Path != "/v1/workspaces/current/import" ||
			r.URL.Query().Get("mode") != apiclient.WorkspaceRestoreModeFresh {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Content-Type"); got != apiclient.WorkspaceBundleMediaType {
			t.Errorf("Content-Type = %q", got)
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != bundle {
			t.Errorf("body = %q", data)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"bundle_id":"bundle-1",
			"archive_sha256":"abcd",
			"source_workspace_id":"source-1",
			"imported_at":"2026-07-28T12:00:00Z",
			"counts":{
				"folders":1,
				"files":2,
				"memories":3,
				"memory_events":4,
				"tasks":5,
				"checkpoints":6,
				"checkpoint_refs":7,
				"checkpoint_payloads":8,
				"blobs":9,
				"blob_bytes":42
			},
			"replayed":true
		}`)
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	input := writeWorkspaceTestBundle(t, bundle)
	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{
		"workspace", "import",
		"--input", input,
		"--mode", "fresh",
		"--yes",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"bundle_id",
		"bundle-1",
		"replayed",
		"true",
		"files",
		"2",
		"memories",
		"3",
		"blob_bytes",
		"42",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestWorkspaceImportConflictJSONIncludesResources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{
			"error":"workspace_import_conflict",
			"hint":"target conflicts",
			"conflicts":[
				{"kind":"path","resource":"files","value":"/Projects/plan.md"}
			],
			"total":201,
			"truncated":true
		}`)
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	input := writeWorkspaceTestBundle(t, "bundle")
	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{
		"workspace", "import",
		"--input", input,
		"--yes",
		"--format", "json",
	})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected conflict error")
	}
	var output struct {
		Error     string                              `json:"error"`
		Conflicts []apiclient.WorkspaceImportConflict `json:"conflicts"`
		Total     int                                 `json:"total"`
		Truncated bool                                `json:"truncated"`
	}
	if decodeErr := json.Unmarshal(stdout.Bytes(), &output); decodeErr != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", decodeErr, stdout.String())
	}
	if output.Error != "workspace_import_conflict" ||
		len(output.Conflicts) != 1 ||
		output.Conflicts[0].Value != "/Projects/plan.md" ||
		output.Total != 201 ||
		!output.Truncated {
		t.Fatalf("output = %+v", output)
	}
}

func TestWorkspaceImportConflictTextIncludesResources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{
			"error":"workspace_import_conflict",
			"hint":"target conflicts",
			"conflicts":[
				{"kind":"path","resource":"files","value":"/Projects/plan.md"}
			],
			"total":201,
			"truncated":true
		}`)
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	input := writeWorkspaceTestBundle(t, "bundle")
	root := newRootCmd()
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"workspace", "import",
		"--input", input,
		"--yes",
	})
	if err := root.Execute(); err == nil {
		t.Fatal("expected conflict error")
	}
	for _, want := range []string{
		"path",
		"files",
		"/Projects/plan.md",
		"more conflicts omitted",
		"201",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestWorkspaceImportIndeterminateCommitRequiresExactBundleRetry(t *testing.T) {
	const recoveryHint = "uploaded objects were preserved; retry the exact same bundle"
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{
			"error":"workspace_import_commit_indeterminate",
			"hint":"uploaded objects were preserved; retry the exact same bundle"
		}`)
	}))
	defer server.Close()
	setWorkspaceTestConfig(t, server.URL, "token", "workspace")

	input := writeWorkspaceTestBundle(t, "bundle")
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			root := newRootCmd()
			var stdout bytes.Buffer
			root.SetOut(&stdout)
			root.SetArgs([]string{
				"workspace", "import",
				"--input", input,
				"--yes",
				"--format", format,
			})
			err := root.Execute()
			var cliErr *cliError
			if !errors.As(err, &cliErr) {
				t.Fatalf("error = %T %v, want *cliError", err, err)
			}
			if cliErr.code != 5 ||
				!strings.Contains(cliErr.msg, "workspace_import_commit_indeterminate") ||
				cliErr.hint != recoveryHint {
				t.Fatalf("CLI error = %+v", cliErr)
			}
			if !root.SilenceUsage {
				t.Fatal("indeterminate recovery printed command usage")
			}
			if format == "text" {
				if stdout.Len() != 0 {
					t.Fatalf("text stdout = %q", stdout.String())
				}
				return
			}
			var output struct {
				Error string `json:"error"`
				Hint  string `json:"hint"`
			}
			if decodeErr := json.Unmarshal(stdout.Bytes(), &output); decodeErr != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", decodeErr, stdout.String())
			}
			if output.Error != "workspace_import_commit_indeterminate" ||
				output.Hint != recoveryHint {
				t.Fatalf("JSON error = %+v", output)
			}
		})
	}
}

func setWorkspaceTestConfig(t *testing.T, server, token, workspace string) {
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
	t.Setenv("MEM_TOKEN", token)
	t.Setenv("MEM_WORKSPACE", workspace)
}

func writeWorkspaceTestBundle(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workspace.membundle")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
