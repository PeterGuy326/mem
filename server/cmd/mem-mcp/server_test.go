package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
	"github.com/PeterGuy326/mem/server/internal/tools/builtin"
)

// readResponses parses the server's stdout buffer as newline-delimited
// JSON-RPC messages.
func readResponses(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	scanner := bufio.NewScanner(buf)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("bad response %q: %v", scanner.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func newTestServer(reg *tools.Registry) (*mcpServer, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return &mcpServer{
		reg: reg,
		out: bufio.NewWriter(buf),
	}, buf
}

func TestMCP_InitializeHandshake(t *testing.T) {
	reg := tools.New()
	srv, buf := newTestServer(reg)

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	if err := srv.serve(in); err != nil {
		t.Fatal(err)
	}

	resps := readResponses(t, buf)
	if len(resps) != 1 {
		t.Fatalf("want 1 response, got %d", len(resps))
	}
	r := resps[0]
	if r["jsonrpc"] != "2.0" {
		t.Fatalf("missing jsonrpc")
	}
	result, _ := r["result"].(map[string]any)
	if result["protocolVersion"] != protocolVersion {
		t.Fatalf("protocolVersion: %v", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "mem-mcp" {
		t.Fatalf("serverInfo.name: %v", info["name"])
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("missing capabilities.tools: %v", caps)
	}
}

func TestMCP_ToolsListReflectsRegistry(t *testing.T) {
	reg := tools.New()
	_ = reg.Register(tools.Tool{
		Name:        "mem_demo",
		Description: "demo",
		InputSchema: tools.Schema{Type: "object"},
		Run:         func(context.Context, map[string]any) (any, error) { return "ok", nil },
	})
	srv, buf := newTestServer(reg)

	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	_ = srv.serve(in)

	resps := readResponses(t, buf)
	r := resps[0]["result"].(map[string]any)
	items := r["tools"].([]any)
	if len(items) != 1 {
		t.Fatalf("want 1 tool, got %d", len(items))
	}
	t0 := items[0].(map[string]any)
	if t0["name"] != "mem_demo" {
		t.Fatalf("name: %v", t0["name"])
	}
	if t0["description"] != "demo" {
		t.Fatalf("description: %v", t0["description"])
	}
	schema, ok := t0["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema not an object: %T", t0["inputSchema"])
	}
	if schema["type"] != "object" {
		t.Fatalf("schema.type: %v", schema["type"])
	}
}

func TestMCP_ToolsCallRoundTrip(t *testing.T) {
	// Spin up a fake memd that returns a fixed folder response.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"abc","path":"/X","name":"X"}`))
	}))
	defer fake.Close()

	reg := tools.New()
	if err := builtin.RegisterAll(reg, apiclient.New(fake.URL, "tok")); err != nil {
		t.Fatal(err)
	}
	srv, buf := newTestServer(reg)

	in := strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mem_mkdir","arguments":{"path":"/X"}}}` + "\n")
	if err := srv.serve(in); err != nil {
		t.Fatal(err)
	}

	resps := readResponses(t, buf)
	if len(resps) != 1 {
		t.Fatalf("want 1 response, got %d", len(resps))
	}
	r := resps[0]
	if r["error"] != nil {
		t.Fatalf("rpc error: %v", r["error"])
	}
	result := r["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatalf("isError: %v", result["isError"])
	}
	content := result["content"].([]any)
	first := content[0].(map[string]any)
	if first["type"] != "text" {
		t.Fatalf("content type: %v", first["type"])
	}
	if !strings.Contains(first["text"].(string), `"id": "abc"`) {
		t.Fatalf("content body: %v", first["text"])
	}
}

func TestMCP_ToolErrorSurfacedInContent(t *testing.T) {
	// memd returns 404
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"not_found","hint":"no such file"}`))
	}))
	defer fake.Close()

	reg := tools.New()
	_ = builtin.RegisterAll(reg, apiclient.New(fake.URL, "tok"))
	srv, buf := newTestServer(reg)

	in := strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"mem_info","arguments":{"file_id":"missing"}}}` + "\n")
	_ = srv.serve(in)

	resps := readResponses(t, buf)
	r := resps[0]
	if r["error"] != nil {
		t.Fatalf("should not be JSON-RPC error: %v", r["error"])
	}
	result := r["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("isError must be true on tool failure")
	}
	content := result["content"].([]any)
	txt := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(txt, "not_found") {
		t.Fatalf("error text missing not_found: %s", txt)
	}
}

func TestMCP_UnknownMethodReturnsRPCError(t *testing.T) {
	reg := tools.New()
	srv, buf := newTestServer(reg)
	in := strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"made/up"}` + "\n")
	_ = srv.serve(in)
	r := readResponses(t, buf)[0]
	e := r["error"].(map[string]any)
	// JSON numbers decode to float64.
	if int(e["code"].(float64)) != errMethodNotFound {
		t.Fatalf("code: %v", e["code"])
	}
}

func TestMCP_NotificationProducesNoResponse(t *testing.T) {
	reg := tools.New()
	srv, buf := newTestServer(reg)
	// Notifications have no `id` and MUST NOT produce a response.
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	_ = srv.serve(in)
	if buf.Len() != 0 {
		t.Fatalf("expected no response, got: %q", buf.String())
	}
}

func TestMCP_MalformedLineReturnsParseError(t *testing.T) {
	reg := tools.New()
	srv, buf := newTestServer(reg)
	in := strings.NewReader("not-json\n" + `{"jsonrpc":"2.0","id":7,"method":"ping"}` + "\n")
	_ = srv.serve(in)
	resps := readResponses(t, buf)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d", len(resps))
	}
	first := resps[0]
	e := first["error"].(map[string]any)
	if int(e["code"].(float64)) != errParse {
		t.Fatalf("first should be parse error, got %v", e)
	}
	if resps[1]["error"] != nil {
		t.Fatalf("ping should succeed: %v", resps[1])
	}
}

// TestMCP_WriteIsConcurrencySafe smokes the writeMu by spamming send() from
// many goroutines and ensuring every line parses back as valid JSON.
func TestMCP_WriteIsConcurrencySafe(t *testing.T) {
	reg := tools.New()
	srv, buf := newTestServer(reg)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, _ := json.Marshal(i)
			srv.writeResult(id, map[string]any{"i": i})
		}(i)
	}
	wg.Wait()
	resps := readResponses(t, buf)
	if len(resps) != 50 {
		t.Fatalf("want 50, got %d", len(resps))
	}
}
