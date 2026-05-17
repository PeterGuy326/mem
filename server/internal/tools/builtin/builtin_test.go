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
	lastMethod string
	lastPath   string
	lastQuery  string
	lastBody   []byte
	lastCtype  string
}

func newFakeServer(body string, status int, ctype string) *fakeServer {
	fs := &fakeServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.lastMethod = r.Method
		fs.lastPath = r.URL.Path
		fs.lastQuery = r.URL.RawQuery
		fs.lastCtype = r.Header.Get("Content-Type")
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
		"mem_ask",
		"mem_folder_tree", "mem_get", "mem_info",
		"mem_list", "mem_ls", "mem_mkdir", "mem_mv", "mem_put",
		"mem_search",
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
