// Command mem-mcp is the Model Context Protocol server for the mem AI drive.
//
// Transport: newline-delimited JSON-RPC 2.0 over stdio.
// Protocol:  MCP 2024-11-05 baseline (initialize, tools/list, tools/call,
//
//	notifications/initialized).
//
// Configuration is read from flags or environment:
//
//	--server / MEM_SERVER   memd base URL (default http://localhost:8787)
//	--token  / MEM_TOKEN    bearer token (required for any write/read scope)
//
// Claude Desktop config example (~/Library/Application Support/Claude/claude_desktop_config.json):
//
//	{
//	  "mcpServers": {
//	    "mem": {
//	      "command": "/usr/local/bin/mem-mcp",
//	      "env": {
//	        "MEM_SERVER": "http://localhost:8787",
//	        "MEM_TOKEN":  "..."
//	      }
//	    }
//	  }
//	}
//
// stdout is reserved for MCP messages; ALL logging goes to stderr.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"os"
	"sync"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
	"github.com/PeterGuy326/mem/server/internal/tools/builtin"
)

// protocolVersion is the MCP spec revision we implement. Clients (Claude
// Desktop, Cursor, Cline) negotiate on this in `initialize`.
const protocolVersion = "2024-11-05"

// serverInfo is what we report back in the `initialize` handshake.
var serverInfo = map[string]any{
	"name":    "mem-mcp",
	"version": "0.1.0", // bumped manually until ldflags wire-up lands
}

func main() {
	var (
		server string
		token  string
	)
	flag.StringVar(&server, "server", envOr("MEM_SERVER", "http://localhost:8787"), "memd base URL")
	flag.StringVar(&token, "token", os.Getenv("MEM_TOKEN"), "bearer token (or MEM_TOKEN env var)")
	flag.Parse()

	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("mem-mcp: ")

	if token == "" {
		log.Println("warning: no token supplied; only public endpoints will work. Set MEM_TOKEN or pass --token.")
	}

	reg := tools.New()
	if err := builtin.RegisterAll(reg, apiclient.New(server, token)); err != nil {
		log.Fatalf("register builtin tools: %v", err)
	}
	log.Printf("ready: server=%s, %d tools registered", server, len(reg.List()))

	srv := &mcpServer{reg: reg, out: bufio.NewWriter(os.Stdout)}
	if err := srv.serve(os.Stdin); err != nil && err != io.EOF {
		log.Fatalf("serve: %v", err)
	}
}

// mcpServer is the JSON-RPC dispatch loop.
type mcpServer struct {
	reg *tools.Registry

	// writeMu guards concurrent writes to out. Stdio MCP is request/response
	// and tool calls run synchronously per request, but we keep the lock so
	// future streaming notifications can be added safely.
	writeMu sync.Mutex
	out     *bufio.Writer
}

// rpcMessage is the union shape for both incoming requests/notifications
// and outgoing responses. ID is encoded as json.RawMessage so it
// round-trips with the same JSON type (number or string) the client sent.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC 2.0 reserved error codes.
const (
	errParse          = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternal       = -32603
)

func (s *mcpServer) serve(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	// MCP messages can be larger than the default 64KB Scanner buffer
	// (multipart file uploads via mem_put easily blow past that). Allow up
	// to 8 MiB lines.
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			s.writeErr(nil, errParse, "parse error: "+err.Error())
			continue
		}
		s.dispatch(&msg)
	}
	return scanner.Err()
}

func (s *mcpServer) dispatch(req *rpcMessage) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "notifications/initialized":
		// fire-and-forget; no response per JSON-RPC notification rules
	case "tools/list":
		s.handleToolsList(req)
	case "tools/call":
		s.handleToolsCall(req)
	case "ping":
		s.writeResult(req.ID, map[string]any{})
	default:
		if req.ID != nil {
			s.writeErr(req.ID, errMethodNotFound, "method not found: "+req.Method)
		}
	}
}

// --- handlers ---

func (s *mcpServer) handleInitialize(req *rpcMessage) {
	// We accept any client protocolVersion; we echo our own. Clients that
	// require a specific version will read this and act accordingly.
	s.writeResult(req.ID, map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{
				"listChanged": false, // builtin registration is static
			},
		},
		"serverInfo": serverInfo,
	})
}

func (s *mcpServer) handleToolsList(req *rpcMessage) {
	list := s.reg.List()
	items := make([]map[string]any, 0, len(list))
	for _, t := range list {
		items = append(items, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	s.writeResult(req.ID, map[string]any{"tools": items})
}

type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *mcpServer) handleToolsCall(req *rpcMessage) {
	var p toolsCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeErr(req.ID, errInvalidParams, "bad params: "+err.Error())
		return
	}
	if p.Name == "" {
		s.writeErr(req.ID, errInvalidParams, "tools/call requires `name`")
		return
	}

	// We deliberately don't pipe the JSON-RPC `id` into ctx; MCP doesn't
	// have a cancellation channel in 2024-11-05 baseline, so a stale call
	// will run to completion. Future: hook this into a per-id cancel map
	// once we adopt the `$/cancelRequest` notification.
	out, err := s.reg.Call(s.ctx(), p.Name, p.Arguments)
	if err != nil {
		// Tool-level errors are reported as content with isError=true
		// (MCP convention) so the client/LLM can read and react. Reserve
		// JSON-RPC errors for protocol-level failures.
		s.writeResult(req.ID, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": err.Error()},
			},
			"isError": true,
		})
		return
	}

	// Encode result as a single text content block of pretty JSON. This is
	// what Claude Desktop and Cursor expect; the LLM reads the JSON
	// directly. If a future tool returns rich content (images, audio), it
	// can switch to typed content blocks.
	body, _ := json.MarshalIndent(out, "", "  ")
	s.writeResult(req.ID, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(body)},
		},
		"isError": false,
	})
}

// --- helpers ---

func (s *mcpServer) ctx() context.Context { return context.Background() }

func (s *mcpServer) writeResult(id json.RawMessage, result any) {
	s.send(rpcMessage{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *mcpServer) writeErr(id json.RawMessage, code int, msg string) {
	s.send(rpcMessage{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *mcpServer) send(m rpcMessage) {
	if m.JSONRPC == "" {
		m.JSONRPC = "2.0"
	}
	b, err := json.Marshal(m)
	if err != nil {
		log.Printf("marshal response: %v", err)
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.out.Write(b)
	_, _ = s.out.Write([]byte{'\n'})
	_ = s.out.Flush()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
