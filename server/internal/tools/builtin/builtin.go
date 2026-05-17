// Package builtin wires the W1-scope tool set into a Registry.
//
// New tools register here (or in sibling files) so the call site in mem-mcp
// stays a single `builtin.RegisterAll(reg, client)` line. Each tool is a
// thin call into apiclient — the source of truth for input shape is the
// HTTP API in server/internal/api/.
//
// Naming convention: every tool name is `mem_<verb>` (snake_case) so it
// surfaces cleanly through MCP (SPEC §8) and reads natural in agent logs.
package builtin

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
)

// RegisterAll registers the full W1 tool set with reg, binding each tool's
// Run handler to the supplied apiclient. Subsequent phases (W3 search, W4
// ask, …) add their own Register* functions called from here.
func RegisterAll(reg *tools.Registry, client *apiclient.Client) error {
	for _, fn := range []func(*tools.Registry, *apiclient.Client) error{
		registerPut,
		registerGet,
		registerInfo,
		registerList,
		registerLs,
		registerMkdir,
		registerMv,
		registerFolderTree,
		registerSearch,
		registerAsk,
	} {
		if err := fn(reg, client); err != nil {
			return err
		}
	}
	return nil
}

// --- file tools ---

func registerPut(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name:        "mem_put",
		Description: "Upload content to the mem AI drive and trigger AI indexing. Supports plain text or base64-encoded binary. SPEC §8.1.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"name", "content"},
			Properties: map[string]tools.Property{
				"name":     {Type: "string", Description: "Filename, e.g. notes.md"},
				"content":  {Type: "string", Description: "Text content (or base64 if encoding=base64)"},
				"encoding": {Type: "string", Enum: []string{"utf8", "base64"}, Default: "utf8"},
				"mime":     {Type: "string", Description: "MIME type; inferred from extension if omitted"},
				"path":     {Type: "string", Description: "Destination folder, e.g. /Notes (mkdir -p applied)"},
				"tags":     {Type: "array", Items: &tools.Property{Type: "string"}},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			name, _ := args["name"].(string)
			content, _ := args["content"].(string)
			if name == "" || content == "" {
				return nil, fmt.Errorf("mem_put: name and content are required")
			}
			var body io.Reader
			switch enc, _ := args["encoding"].(string); enc {
			case "", "utf8":
				body = bytes.NewReader([]byte(content))
			case "base64":
				raw, err := base64.StdEncoding.DecodeString(content)
				if err != nil {
					return nil, fmt.Errorf("mem_put: invalid base64: %w", err)
				}
				body = bytes.NewReader(raw)
			default:
				return nil, fmt.Errorf("mem_put: unknown encoding %q", enc)
			}
			mime, _ := args["mime"].(string)
			folder, _ := args["path"].(string)
			tags := stringSlice(args["tags"])

			var out map[string]any
			if err := c.UploadMultipart(ctx, name, mime, folder, body, tags, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

func registerGet(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name:        "mem_get",
		Description: "Read file content. Returns text directly; binary content is returned base64-encoded.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"file_id"},
			Properties: map[string]tools.Property{
				"file_id": {Type: "string"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			id, _ := args["file_id"].(string)
			if id == "" {
				return nil, fmt.Errorf("mem_get: file_id required")
			}
			rc, ctype, err := c.DownloadStream(ctx, id)
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			// Cap at 4 MiB to keep agent context manageable. Bigger
			// payloads should go through the HTTP API.
			const cap = 4 << 20
			buf, err := io.ReadAll(io.LimitReader(rc, cap+1))
			if err != nil {
				return nil, err
			}
			if len(buf) > cap {
				return nil, fmt.Errorf("mem_get: content exceeds %d bytes; use HTTP GET /v1/files/%s/content", cap, id)
			}
			if isTextMIME(ctype) {
				return map[string]any{
					"content_type": ctype,
					"encoding":     "utf8",
					"content":      string(buf),
				}, nil
			}
			return map[string]any{
				"content_type": ctype,
				"encoding":     "base64",
				"content":      base64.StdEncoding.EncodeToString(buf),
			}, nil
		},
	})
}

func registerInfo(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name:        "mem_info",
		Description: "Fetch file metadata + AI fields (caption/summary/tags/timeline_at/index_status).",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"file_id"},
			Properties: map[string]tools.Property{
				"file_id": {Type: "string"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			id, _ := args["file_id"].(string)
			if id == "" {
				return nil, fmt.Errorf("mem_info: file_id required")
			}
			var out map[string]any
			if err := c.DoJSON(ctx, http.MethodGet, "/v1/files/"+id, nil, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

func registerList(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name:        "mem_list",
		Description: "List files with optional filters (tag/mime-prefix/since/until/path-prefix). Pagination via page/limit.",
		InputSchema: tools.Schema{
			Type: "object",
			Properties: map[string]tools.Property{
				"tag":    {Type: "string"},
				"type":   {Type: "string", Description: "mime prefix, e.g. image, text"},
				"since":  {Type: "string", Format: "date-time"},
				"until":  {Type: "string", Format: "date-time"},
				"prefix": {Type: "string", Description: "subtree filter, e.g. /Photos"},
				"path":   {Type: "string", Description: "exact folder path"},
				"limit":  {Type: "integer", Default: 50},
				"page":   {Type: "integer", Default: 1},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			q := url.Values{}
			for _, k := range []string{"tag", "type", "since", "until", "prefix", "path"} {
				if v, _ := args[k].(string); v != "" {
					q.Set(k, v)
				}
			}
			if v, ok := args["limit"]; ok {
				q.Set("limit", numToString(v))
			}
			if v, ok := args["page"]; ok {
				q.Set("page", numToString(v))
			}
			path := "/v1/files"
			if enc := q.Encode(); enc != "" {
				path += "?" + enc
			}
			var out map[string]any
			if err := c.DoJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

// --- folder tools ---

func registerLs(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name:        "mem_ls",
		Description: "List immediate subfolders and files under a folder path. Defaults to root '/'.",
		InputSchema: tools.Schema{
			Type: "object",
			Properties: map[string]tools.Property{
				"parent": {Type: "string", Default: "/"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			parent, _ := args["parent"].(string)
			if parent == "" {
				parent = "/"
			}
			q := url.Values{}
			q.Set("parent", parent)
			var out map[string]any
			if err := c.DoJSON(ctx, http.MethodGet, "/v1/folders?"+q.Encode(), nil, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

func registerMkdir(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name:        "mem_mkdir",
		Description: "Create a folder. mkdir -p semantics — missing parents are created automatically.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"path"},
			Properties: map[string]tools.Property{
				"path": {Type: "string", Description: "Absolute folder path, e.g. /Photos/2012"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			p, _ := args["path"].(string)
			if p == "" {
				return nil, fmt.Errorf("mem_mkdir: path required")
			}
			var out map[string]any
			if err := c.DoJSON(ctx, http.MethodPost, "/v1/folders", map[string]any{"path": p}, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

func registerMv(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name:        "mem_mv",
		Description: "Move a file to a different folder (mkdir -p applied to destination), or rename it in place.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"file_id"},
			Properties: map[string]tools.Property{
				"file_id": {Type: "string"},
				"path":    {Type: "string", Description: "Destination folder path"},
				"name":    {Type: "string", Description: "New basename (rename only)"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			id, _ := args["file_id"].(string)
			if id == "" {
				return nil, fmt.Errorf("mem_mv: file_id required")
			}
			body := map[string]any{}
			if v, _ := args["path"].(string); v != "" {
				body["path"] = v
			}
			if v, _ := args["name"].(string); v != "" {
				body["name"] = v
			}
			if len(body) == 0 {
				return nil, fmt.Errorf("mem_mv: supply at least one of `path` or `name`")
			}
			var out map[string]any
			if err := c.DoJSON(ctx, http.MethodPatch, "/v1/files/"+id, body, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

func registerFolderTree(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name:        "mem_folder_tree",
		Description: "Return the complete folder tree as a nested structure (path/name/file_count + children).",
		InputSchema: tools.Schema{Type: "object"},
		Run: func(ctx context.Context, _ map[string]any) (any, error) {
			var out map[string]any
			if err := c.DoJSON(ctx, http.MethodGet, "/v1/folders/tree", nil, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

// --- search tool ---

func registerSearch(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_search",
		Description: "Natural-language search across the mem AI drive. Returns ranked files with " +
			"matching text snippets. Use this BEFORE mem_get when you don't already know the file_id. SPEC §8.1.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"query"},
			Properties: map[string]tools.Property{
				"query": {Type: "string", Description: "Free-form natural-language query, e.g. \"2012 photos with Xiao Ming\""},
				"type":  {Type: "string", Description: "MIME prefix filter: image|text|application|audio|video"},
				"route": {Type: "string", Description: "Search route: text|visual|auto (default auto fuses both)", Enum: []string{"text", "visual", "auto"}},
				"since": {Type: "string", Description: "YYYY-MM-DD lower bound on timeline_at"},
				"until": {Type: "string", Description: "YYYY-MM-DD upper bound on timeline_at"},
				"limit": {Type: "integer", Description: "Max results (default 10, max 100)", Default: 10},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			q, _ := args["query"].(string)
			if q == "" {
				return nil, fmt.Errorf("mem_search: query is required")
			}
			body := map[string]any{"query": q}
			if v, _ := args["type"].(string); v != "" {
				body["type"] = v
			}
			if v, _ := args["route"].(string); v != "" {
				body["route"] = v
			}
			if v, _ := args["since"].(string); v != "" {
				body["since"] = v
			}
			if v, _ := args["until"].(string); v != "" {
				body["until"] = v
			}
			if v := args["limit"]; v != nil {
				body["limit"] = v
			}
			var out map[string]any
			if err := c.DoJSON(ctx, http.MethodPost, "/v1/search", body, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

// --- ask tool ---

func registerAsk(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_ask",
		Description: "Cross-file question answering: retrieve the top relevant snippets " +
			"from the user's drive then synthesize an answer with citations. " +
			"Returns {answer, sources[]}. Prefer this over mem_search+mem_get when " +
			"the user wants a synthesized answer, not raw files. SPEC §8.1.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"question"},
			Properties: map[string]tools.Property{
				"question": {Type: "string", Description: "Natural-language question, e.g. \"what's in my rental contract?\""},
				"scope":    {Type: "string", Description: "Path prefix filter (e.g. /Photos) — not yet implemented server-side"},
				"top_k":    {Type: "integer", Description: "Number of context snippets (default 5, max 20)", Default: 5},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			q, _ := args["question"].(string)
			if q == "" {
				return nil, fmt.Errorf("mem_ask: question is required")
			}
			body := map[string]any{"question": q}
			if v, _ := args["scope"].(string); v != "" {
				body["scope"] = v
			}
			if v := args["top_k"]; v != nil {
				body["top_k"] = v
			}
			var out map[string]any
			if err := c.DoJSON(ctx, http.MethodPost, "/v1/ask", body, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

// --- helpers ---

func stringSlice(v any) []string {
	xs, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if s, ok := x.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func numToString(v any) string {
	switch n := v.(type) {
	case int:
		return fmt.Sprintf("%d", n)
	case int64:
		return fmt.Sprintf("%d", n)
	case float64:
		return fmt.Sprintf("%d", int64(n))
	case string:
		return n
	}
	return ""
}

func isTextMIME(ct string) bool {
	if ct == "" {
		return false
	}
	// Strip "; charset=..."
	for i, r := range ct {
		if r == ';' {
			ct = ct[:i]
			break
		}
	}
	if len(ct) >= 5 && ct[:5] == "text/" {
		return true
	}
	switch ct {
	case "application/json",
		"application/xml",
		"application/yaml",
		"application/x-yaml",
		"application/javascript":
		return true
	}
	return false
}
