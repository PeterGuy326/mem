package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestUploadMultipartWithSourceMetadata(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		got = r.FormValue("source_metadata")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"file":{"id":"file-1"}}`))
	}))
	defer srv.Close()

	accuracy := 12.5
	source := &FileSourceMetadata{
		CapturedAt: "2026-07-29T08:00:00+08:00",
		Location: &FileSourceLocation{
			Lat:       31.2304,
			Lon:       121.4737,
			AccuracyM: &accuracy,
			Label:     "Shanghai",
		},
		SourceKind: "mobile",
		SourceName: "camera sync",
	}
	var out map[string]any
	err := New(srv.URL, "token").UploadMultipartWithSourceMetadata(
		context.Background(),
		"photo.jpg",
		"image/jpeg",
		"/Photos",
		strings.NewReader("image"),
		nil,
		source,
		&out,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"captured_at":"2026-07-29T08:00:00+08:00"`,
		`"source_kind":"mobile"`,
		`"lat":31.2304`,
		`"lon":121.4737`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("source_metadata %q missing %q", got, want)
		}
	}
}

func TestUploadStreamWithSourceMetadata(t *testing.T) {
	var (
		got      string
		queryHas bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(sourceMetadataHeader)
		_, queryHas = r.URL.Query()["source_metadata"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"file":{"id":"file-1"}}`))
	}))
	defer srv.Close()

	var out map[string]any
	err := New(srv.URL, "token").UploadStreamWithSourceMetadata(
		context.Background(),
		"note.txt",
		"text/plain",
		"/Notes",
		4,
		nil,
		strings.NewReader("note"),
		&FileSourceMetadata{SourceKind: "cli"},
		&out,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"source_kind":"cli"}` {
		t.Fatalf("source_metadata = %q", got)
	}
	if queryHas {
		t.Fatal("source_metadata leaked into the request URL")
	}
}
