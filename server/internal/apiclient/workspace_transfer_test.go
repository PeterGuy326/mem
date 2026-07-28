package apiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExportWorkspaceStreamsCanonicalBundle(t *testing.T) {
	const bundle = "portable-workspace-bundle"
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
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
		if got := r.Header.Get("Accept"); got != WorkspaceBundleMediaType {
			t.Errorf("Accept = %q", got)
		}
		w.Header().Set("Content-Type", WorkspaceBundleMediaType)
		w.Header().Set(
			"Content-Disposition",
			`attachment; filename="workspace-export.membundle"`,
		)
		w.Header().Set("Content-Length", "25")
		_, _ = io.WriteString(w, bundle)
	}))
	defer server.Close()

	download, err := New(server.URL, "export-token").
		WithWorkspace("export-workspace").
		ExportWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer download.Body.Close()
	body, err := io.ReadAll(download.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != bundle {
		t.Fatalf("body = %q", body)
	}
	if download.Filename != "workspace-export.membundle" ||
		download.ContentType != WorkspaceBundleMediaType ||
		download.ContentLength != int64(len(bundle)) {
		t.Fatalf("download = %+v", download)
	}
}

func TestExportWorkspaceRejectsUnexpectedSuccessContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"not":"a bundle"}`)
	}))
	defer server.Close()

	_, err := New(server.URL, "token").ExportWorkspace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected Content-Type") {
		t.Fatalf("error = %v", err)
	}
}

func TestImportWorkspaceStreamsBundleAndDecodesResult(t *testing.T) {
	const bundle = "workspace-archive"
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.Method != http.MethodPost ||
			r.URL.Path != "/v1/workspaces/current/import" ||
			r.URL.Query().Get("mode") != WorkspaceRestoreModeFresh {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Content-Type"); got != WorkspaceBundleMediaType {
			t.Errorf("Content-Type = %q", got)
		}
		if r.ContentLength != int64(len(bundle)) {
			t.Errorf("Content-Length = %d", r.ContentLength)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != bundle {
			t.Errorf("body = %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"bundle_id":"bundle-1",
			"archive_sha256":"abcd",
			"source_workspace_id":"source-1",
			"imported_at":"2026-07-28T12:00:00Z",
			"counts":{"files":2,"memories":3,"blob_bytes":42},
			"replayed":true
		}`)
	}))
	defer server.Close()

	result, err := New(server.URL, "import-token").ImportWorkspace(
		context.Background(),
		WorkspaceRestoreModeFresh,
		int64(len(bundle)),
		strings.NewReader(bundle),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.BundleID != "bundle-1" ||
		result.SourceWorkspaceID != "source-1" ||
		result.Counts.Files != 2 ||
		result.Counts.Memories != 3 ||
		result.Counts.BlobBytes != 42 ||
		!result.Replayed {
		t.Fatalf("result = %+v", result)
	}
}

func TestImportWorkspacePreservesConflictDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{
			"error":"workspace_import_conflict",
			"hint":"target workspace conflicts",
			"conflicts":[
				{"kind":"path","resource":"files","value":"/Projects/plan.md"}
			],
			"total":201,
			"truncated":true
		}`)
	}))
	defer server.Close()

	_, err := New(server.URL, "token").ImportWorkspace(
		context.Background(),
		WorkspaceRestoreModeFresh,
		6,
		strings.NewReader("bundle"),
	)
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error = %T %v", err, err)
	}
	if apiError.StatusCode != http.StatusConflict ||
		len(apiError.Conflicts) != 1 ||
		apiError.Conflicts[0].Value != "/Projects/plan.md" ||
		apiError.ConflictTotal != 201 ||
		!apiError.ConflictsTruncated {
		t.Fatalf("API error = %+v", apiError)
	}
}

func TestImportWorkspaceRejectsUnimplementedModeBeforeRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		called = true
	}))
	defer server.Close()

	_, err := New(server.URL, "token").ImportWorkspace(
		context.Background(),
		"merge",
		6,
		strings.NewReader("bundle"),
	)
	if err == nil || !strings.Contains(err.Error(), "unsupported workspace restore mode") {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("request made for unsupported mode")
	}
}
