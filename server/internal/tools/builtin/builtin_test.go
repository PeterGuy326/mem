package builtin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
)

// fakeServer returns a test server that records the last request and
// responds with the supplied body. Lets us assert that each tool maps
// arguments → HTTP request correctly without standing up a real memd.
type fakeServer struct {
	*httptest.Server
	lastMethod  string
	lastPath    string
	lastQuery   string
	lastBody    []byte
	lastCtype   string
	lastHeaders http.Header
}

func newFakeServer(body string, status int, ctype string) *fakeServer {
	fs := &fakeServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.lastMethod = r.Method
		fs.lastPath = r.URL.Path
		fs.lastQuery = r.URL.RawQuery
		fs.lastCtype = r.Header.Get("Content-Type")
		fs.lastHeaders = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		fs.lastBody = b
		if ctype != "" {
			w.Header().Set("Content-Type", ctype)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	return fs
}

func TestRegisterAll_RegistersExpectedToolNames(t *testing.T) {
	fs := newFakeServer(`{}`, 200, "application/json")
	defer fs.Close()
	reg := tools.New()
	c := apiclient.New(fs.URL, "tok")
	if err := RegisterAll(reg, c); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	want := []string{
		"mem_archive", "mem_checkpoint", "mem_checkpoint_get",
		"mem_checkpoint_list", "mem_context", "mem_face", "mem_feedback",
		"mem_folder_tree", "mem_forget", "mem_get", "mem_info", "mem_list",
		"mem_ls", "mem_memory_get", "mem_memory_list", "mem_mkdir", "mem_mv",
		"mem_put", "mem_related", "mem_remember", "mem_restore", "mem_resume",
		"mem_search", "mem_task_list",
	}
	got := reg.List()
	if len(got) != len(want) {
		t.Fatalf("count: got %d want %d", len(got), len(want))
	}
	for i, n := range want {
		if got[i].Name != n {
			t.Fatalf("at %d: got %s want %s", i, got[i].Name, n)
		}
		if got[i].Description == "" {
			t.Fatalf("%s: empty description", n)
		}
	}
}

func TestMemRemember_PostsNestedBodyAndIdempotencyHeader(t *testing.T) {
	fs := newFakeServer(`{"memory":{"id":"mem-1"},"replayed":false}`, http.StatusCreated, "application/json")
	defer fs.Close()
	reg := tools.New()
	if err := registerRemember(reg, apiclient.New(fs.URL, "tok")); err != nil {
		t.Fatal(err)
	}

	out, err := reg.Call(context.Background(), "mem_remember", map[string]any{
		"content":         "Use PostgreSQL for lexical recall",
		"kind":            "decision",
		"path":            "/Projects/mem",
		"idempotency_key": "task-42-db-decision",
		"event_at":        "2026-07-28T12:34:56Z",
		"source_ref":      "agent://codex/task-42",
		"source_file_id":  "file-1",
		"source_locator":  map[string]any{"kind": "paragraph", "index": float64(3)},
		"agent_id":        "codex",
		"session_id":      "session-7",
		"task_id":         "task-42",
		"attributes":      map[string]any{"confidence": "confirmed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fs.lastMethod != http.MethodPost || fs.lastPath != "/v1/memories" {
		t.Fatalf("unexpected request: %s %s", fs.lastMethod, fs.lastPath)
	}
	if got := fs.lastHeaders.Get("Idempotency-Key"); got != "task-42-db-decision" {
		t.Fatalf("Idempotency-Key = %q", got)
	}

	var body map[string]any
	if err := json.Unmarshal(fs.lastBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["kind"] != "decision" ||
		body["content"] != "Use PostgreSQL for lexical recall" ||
		body["path"] != "/Projects/mem" ||
		body["event_at"] != "2026-07-28T12:34:56Z" {
		t.Fatalf("top-level body = %#v", body)
	}
	if _, exists := body["idempotency_key"]; exists {
		t.Fatalf("idempotency key must be a header, body = %#v", body)
	}
	source, ok := body["source"].(map[string]any)
	if !ok {
		t.Fatalf("source = %#v", body["source"])
	}
	if source["type"] != "agent" ||
		source["ref"] != "agent://codex/task-42" ||
		source["file_id"] != "file-1" {
		t.Fatalf("source = %#v", source)
	}
	locator, ok := source["locator"].(map[string]any)
	if !ok || locator["kind"] != "paragraph" || locator["index"] != float64(3) {
		t.Fatalf("source.locator = %#v", source["locator"])
	}
	producer, ok := body["producer"].(map[string]any)
	if !ok ||
		producer["agent_id"] != "codex" ||
		producer["session_id"] != "session-7" ||
		producer["task_id"] != "task-42" {
		t.Fatalf("producer = %#v", body["producer"])
	}
	attributes, ok := body["attributes"].(map[string]any)
	if !ok || attributes["confidence"] != "confirmed" {
		t.Fatalf("attributes = %#v", body["attributes"])
	}
	response, ok := out.(map[string]any)
	if !ok || response["replayed"] != false {
		t.Fatalf("response = %#v", out)
	}
}

func TestMemContext_PostsEvidenceRequest(t *testing.T) {
	fs := newFakeServer(`{"query":"project decision","scope":"/Work","evidence":[]}`, 200, "application/json")
	defer fs.Close()
	reg := tools.New()
	if err := registerContext(reg, apiclient.New(fs.URL, "tok")); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Call(context.Background(), "mem_context", map[string]any{
		"query":       "project decision",
		"scope":       "/Work",
		"source":      "memory",
		"memory_kind": "decision",
		"limit":       float64(6),
		"max_chars":   float64(9000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fs.lastMethod != http.MethodPost || fs.lastPath != "/v1/context" {
		t.Fatalf("unexpected request: %s %s", fs.lastMethod, fs.lastPath)
	}
	var body map[string]any
	if err := json.Unmarshal(fs.lastBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["query"] != "project decision" || body["scope"] != "/Work" {
		t.Fatalf("body = %#v", body)
	}
	if body["source"] != "memory" || body["memory_kind"] != "decision" {
		t.Fatalf("memory filters missing from body = %#v", body)
	}
}

func TestMemMkdir_PostsJSONWithPath(t *testing.T) {
	fs := newFakeServer(`{"id":"abc","path":"/Photos/2012"}`, 201, "application/json")
	defer fs.Close()
	reg := tools.New()
	if err := registerMkdir(reg, apiclient.New(fs.URL, "tok")); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Call(context.Background(), "mem_mkdir", map[string]any{"path": "/Photos/2012"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if fs.lastMethod != "POST" || fs.lastPath != "/v1/folders" {
		t.Fatalf("unexpected request: %s %s", fs.lastMethod, fs.lastPath)
	}
	var body map[string]any
	_ = json.Unmarshal(fs.lastBody, &body)
	if body["path"] != "/Photos/2012" {
		t.Fatalf("body.path = %v", body["path"])
	}
	m, _ := out.(map[string]any)
	if m["id"] != "abc" {
		t.Fatalf("response not propagated: %v", out)
	}
}

func TestMemLs_DefaultsParentToRoot(t *testing.T) {
	fs := newFakeServer(`{"parent":"/","folders":[],"files":[]}`, 200, "application/json")
	defer fs.Close()
	reg := tools.New()
	_ = registerLs(reg, apiclient.New(fs.URL, "tok"))
	if _, err := reg.Call(context.Background(), "mem_ls", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fs.lastQuery, "parent=%2F") {
		t.Fatalf("expected parent=/ in query, got %q", fs.lastQuery)
	}
}

func TestMemList_BuildsFiltersIntoQuery(t *testing.T) {
	fs := newFakeServer(`{"files":[]}`, 200, "application/json")
	defer fs.Close()
	reg := tools.New()
	_ = registerList(reg, apiclient.New(fs.URL, "tok"))
	_, err := reg.Call(context.Background(), "mem_list", map[string]any{
		"tag":   "important",
		"type":  "image",
		"limit": float64(20),
	})
	if err != nil {
		t.Fatal(err)
	}
	q := fs.lastQuery
	for _, sub := range []string{"tag=important", "type=image", "limit=20"} {
		if !strings.Contains(q, sub) {
			t.Fatalf("query %q missing %q", q, sub)
		}
	}
}

func TestMemPut_UploadsMultipartContent(t *testing.T) {
	fs := newFakeServer(`{"file":{"id":"f1","name":"hi.txt"},"deduped":false}`, 201, "application/json")
	defer fs.Close()
	reg := tools.New()
	_ = registerPut(reg, apiclient.New(fs.URL, "tok"))
	out, err := reg.Call(context.Background(), "mem_put", map[string]any{
		"name":    "hi.txt",
		"content": "hello",
		"path":    "/Notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fs.lastMethod != "POST" || fs.lastPath != "/v1/files" {
		t.Fatalf("unexpected request: %s %s", fs.lastMethod, fs.lastPath)
	}
	if !strings.HasPrefix(fs.lastCtype, "multipart/form-data") {
		t.Fatalf("content-type: %s", fs.lastCtype)
	}
	if !strings.Contains(string(fs.lastBody), "hello") {
		t.Fatalf("body missing payload")
	}
	if !strings.Contains(string(fs.lastBody), "/Notes") {
		t.Fatalf("body missing folder hint")
	}
	m, _ := out.(map[string]any)
	if m["deduped"] != false {
		t.Fatalf("response not propagated: %v", out)
	}
}

func TestMemMv_RequiresPathOrName(t *testing.T) {
	fs := newFakeServer(`{}`, 200, "application/json")
	defer fs.Close()
	reg := tools.New()
	_ = registerMv(reg, apiclient.New(fs.URL, "tok"))
	_, err := reg.Call(context.Background(), "mem_mv", map[string]any{"file_id": "x"})
	if err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("want 'at least one of path/name', got %v", err)
	}
}

func TestMemGet_DecodesTextAndBase64(t *testing.T) {
	// Text path
	fs1 := newFakeServer("hello", 200, "text/plain")
	defer fs1.Close()
	reg := tools.New()
	_ = registerGet(reg, apiclient.New(fs1.URL, "tok"))
	out, err := reg.Call(context.Background(), "mem_get", map[string]any{"file_id": "id1"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["encoding"] != "utf8" || m["content"] != "hello" {
		t.Fatalf("text path: %v", m)
	}

	// Binary path
	fs2 := newFakeServer("\x00\x01\x02", 200, "application/octet-stream")
	defer fs2.Close()
	reg2 := tools.New()
	_ = registerGet(reg2, apiclient.New(fs2.URL, "tok"))
	out2, err := reg2.Call(context.Background(), "mem_get", map[string]any{"file_id": "id2"})
	if err != nil {
		t.Fatal(err)
	}
	m2 := out2.(map[string]any)
	if m2["encoding"] != "base64" {
		t.Fatalf("binary path: encoding=%v", m2["encoding"])
	}
}

func TestMemPut_RejectsInvalidBase64(t *testing.T) {
	fs := newFakeServer(`{}`, 200, "application/json")
	defer fs.Close()
	reg := tools.New()
	_ = registerPut(reg, apiclient.New(fs.URL, "tok"))
	_, err := reg.Call(context.Background(), "mem_put", map[string]any{
		"name":     "x.bin",
		"content":  "not-base64!@#$",
		"encoding": "base64",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid base64") {
		t.Fatalf("want invalid-base64 error, got %v", err)
	}
}
