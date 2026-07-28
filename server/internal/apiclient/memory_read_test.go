package apiclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestListMemoriesUsesTypedStablePaginationContract(t *testing.T) {
	memoryID := uuid.NewString()
	workspaceID := uuid.NewString()
	createdAt := time.Date(2026, 7, 28, 2, 3, 4, 0, time.UTC)
	var (
		gotQuery     url.Values
		gotWorkspace string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/memories" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.Query()
		gotWorkspace = r.Header.Get("X-Workspace-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"memories":[{
				"id":"`+memoryID+`",
				"workspace_id":"`+workspaceID+`",
				"kind":"decision",
				"excerpt":"Use immutable occurrences",
				"content_length":25,
				"path":"/Projects/mem",
				"source_type":"agent",
				"source_ref":"agent://codex/task-1",
				"producer_agent":"codex",
				"content_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"lifecycle_status":"active",
				"created_at":"`+createdAt.Format(time.RFC3339Nano)+`",
				"updated_at":"`+createdAt.Format(time.RFC3339Nano)+`"
			}],
			"next_cursor":"opaque-next"
		}`)
	}))
	defer server.Close()

	page, err := New(server.URL, "token").
		WithWorkspace(workspaceID).
		ListMemories(context.Background(), MemoryListOptions{
			Scope:     "/Projects/mem α",
			Kinds:     []string{"decision", "fact"},
			Lifecycle: "archived",
			Limit:     25,
			Cursor:    "opaque-current",
		})
	if err != nil {
		t.Fatal(err)
	}
	if gotWorkspace != workspaceID ||
		gotQuery.Get("scope") != "/Projects/mem α" ||
		gotQuery.Get("lifecycle") != "archived" ||
		gotQuery.Get("limit") != "25" ||
		gotQuery.Get("cursor") != "opaque-current" {
		t.Fatalf("workspace=%q query=%v", gotWorkspace, gotQuery)
	}
	if kinds := gotQuery["kind"]; len(kinds) != 2 ||
		kinds[0] != "decision" ||
		kinds[1] != "fact" {
		t.Fatalf("kind query=%v", kinds)
	}
	if len(page.Memories) != 1 ||
		page.Memories[0].ID != memoryID ||
		page.Memories[0].Excerpt != "Use immutable occurrences" ||
		page.Memories[0].SourceRef != "agent://codex/task-1" ||
		!page.Memories[0].CreatedAt.Equal(createdAt) ||
		page.NextCursor != "opaque-next" {
		t.Fatalf("page=%+v", page)
	}
}

func TestGetMemoryUsesTypedDetailContract(t *testing.T) {
	memoryID := uuid.NewString()
	workspaceID := uuid.NewString()
	createdByUserID := uuid.NewString()
	sourceFileID := uuid.NewString()
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/memories/"+memoryID {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"`+memoryID+`",
			"workspace_id":"`+workspaceID+`",
			"created_by_user_id":"`+createdByUserID+`",
			"kind":"note",
			"content":"Visible detail",
			"attributes":{"importance":"high"},
			"path":"/Work/Project",
			"source_type":"user",
			"source_file_id":"`+sourceFileID+`",
			"source_file_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"source_locator":{"line":42},
			"producer_agent":"codex",
			"content_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"lifecycle_status":"active",
			"created_at":"2026-07-28T00:00:00Z",
			"updated_at":"2026-07-28T00:00:00Z",
			"citation":"mem://memories/`+memoryID+`",
			"provenance":{
				"workspace_id":"`+workspaceID+`",
				"created_by_user_id":"`+createdByUserID+`",
				"source_type":"user",
				"source_file_id":"`+sourceFileID+`",
				"source_file_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"source_locator":{"line":42},
				"producer_agent":"codex"
			}
		}`)
	}))
	defer server.Close()

	record, err := New(server.URL, "token").GetMemory(
		context.Background(),
		memoryID,
		MemoryGetOptions{Scope: "/Work"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("scope") != "/Work" ||
		record.ID != memoryID ||
		record.Content != "Visible detail" ||
		record.Citation != "mem://memories/"+memoryID ||
		record.Provenance.WorkspaceID != workspaceID ||
		record.Provenance.CreatedByUserID == nil ||
		*record.Provenance.CreatedByUserID != createdByUserID ||
		record.Provenance.SourceFileID == nil ||
		*record.Provenance.SourceFileID != sourceFileID ||
		record.Provenance.ProducerAgent != "codex" ||
		string(record.Provenance.SourceLocator) != `{"line":42}` {
		t.Fatalf("query=%v record=%+v", gotQuery, record)
	}
}

func TestMemoryReadClientRejectsInvalidInputBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	client := New(server.URL, "token")

	if _, err := client.ListMemories(context.Background(), MemoryListOptions{
		Scope: "relative",
	}); err == nil {
		t.Fatal("relative list scope accepted")
	}
	if _, err := client.ListMemories(context.Background(), MemoryListOptions{
		Limit: 201,
	}); err == nil {
		t.Fatal("oversized list limit accepted")
	}
	if _, err := client.ListMemories(context.Background(), MemoryListOptions{
		Kinds: []string{"unknown"},
	}); err == nil {
		t.Fatal("unknown memory kind accepted")
	}
	if _, err := client.GetMemory(
		context.Background(),
		"not-a-uuid",
		MemoryGetOptions{},
	); err == nil {
		t.Fatal("invalid memory id accepted")
	}
	if requests != 0 {
		t.Fatalf("unexpected requests=%d", requests)
	}
}
