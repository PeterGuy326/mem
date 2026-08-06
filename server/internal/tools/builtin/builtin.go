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
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
	"github.com/google/uuid"
)

// RegisterAll registers the full W1 tool set with reg, binding each tool's
// Run handler to the supplied apiclient. Memory-native lifecycle tools use
// the same registry while preserving mem_list's historical file semantics.
func RegisterAll(reg *tools.Registry, client *apiclient.Client) error {
	for _, fn := range []func(*tools.Registry, *apiclient.Client) error{
		registerPut,
		registerRemember,
		registerMemoryList,
		registerMemoryGet,
		registerMemoryFeedback,
		registerMemoryArchive,
		registerMemoryRestore,
		registerMemoryForget,
		registerDurableContextRecall,
		registerCheckpoint,
		registerTaskList,
		registerCheckpointList,
		registerCheckpointGet,
		registerResume,
		registerGet,
		registerInfo,
		registerFileAnnotationDecision,
		registerList,
		registerLs,
		registerMkdir,
		registerMv,
		registerFolderTree,
		registerSearch,
		registerContext,
		registerRelated,
		registerFace,
	} {
		if err := fn(reg, client); err != nil {
			return err
		}
	}
	return nil
}

// --- memory tools ---

func registerRemember(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_remember",
		Description: "Persist a model-independent observation, decision, preference, task state, " +
			"or artifact reference with source provenance. Safe retries require a stable idempotency_key.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"content", "kind", "path", "idempotency_key"},
			Properties: map[string]tools.Property{
				"content": {Type: "string", Description: "Memory content to persist"},
				"kind": {Type: "string", Description: "Memory kind",
					Enum: []string{"observation", "decision", "preference", "task_state", "fact", "note", "artifact"}},
				"path":            {Type: "string", Description: "Canonical virtual path used to scope the memory"},
				"idempotency_key": {Type: "string", Description: "Stable retry key, unique within the workspace"},
				"event_at":        {Type: "string", Format: "date-time", Description: "Optional RFC 3339 event time"},
				"source_type":     {Type: "string", Default: "agent", Description: "Source type (default agent)"},
				"source_ref":      {Type: "string", Description: "Optional external source reference"},
				"source_file_id":  {Type: "string", Description: "Optional mem source file UUID"},
				"source_locator":  {Type: "object", Description: "Optional structured source locator, e.g. page, paragraph, or timecode"},
				"agent_id":        {Type: "string", Description: "Producer Agent identifier"},
				"session_id":      {Type: "string", Description: "Producer session identifier"},
				"task_id":         {Type: "string", Description: "Producer task identifier"},
				"attributes":      {Type: "object", Description: "Optional application-defined attributes"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			content, _ := args["content"].(string)
			kind, _ := args["kind"].(string)
			path, _ := args["path"].(string)
			idempotencyKey, _ := args["idempotency_key"].(string)
			if strings.TrimSpace(content) == "" ||
				strings.TrimSpace(kind) == "" ||
				strings.TrimSpace(path) == "" ||
				strings.TrimSpace(idempotencyKey) == "" {
				return nil, fmt.Errorf("mem_remember: content, kind, path, and idempotency_key are required")
			}

			sourceType, _ := args["source_type"].(string)
			if strings.TrimSpace(sourceType) == "" {
				sourceType = "agent"
			}
			source := map[string]any{"type": sourceType}
			for arg, field := range map[string]string{
				"source_ref":     "ref",
				"source_file_id": "file_id",
			} {
				if value, _ := args[arg].(string); value != "" {
					source[field] = value
				}
			}
			if locator, ok, err := rememberObjectArg(args, "source_locator"); err != nil {
				return nil, err
			} else if ok {
				source["locator"] = locator
			}

			producer := map[string]any{}
			for _, key := range []string{"agent_id", "session_id", "task_id"} {
				if value, _ := args[key].(string); value != "" {
					producer[key] = value
				}
			}

			body := map[string]any{
				"kind":     kind,
				"content":  content,
				"path":     path,
				"source":   source,
				"producer": producer,
			}
			if eventAt, _ := args["event_at"].(string); eventAt != "" {
				body["event_at"] = eventAt
			}
			if attributes, ok, err := rememberObjectArg(args, "attributes"); err != nil {
				return nil, err
			} else if ok {
				body["attributes"] = attributes
			}

			var out map[string]any
			if err := c.DoJSONWithHeaders(
				ctx,
				http.MethodPost,
				"/v1/memories",
				body,
				&out,
				map[string]string{"Idempotency-Key": strings.TrimSpace(idempotencyKey)},
			); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
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
				"source_metadata": {
					Type:        "object",
					Description: "Caller/device provenance and capture facts. AI-inferred metadata belongs in pending annotations instead.",
					Properties: map[string]tools.Property{
						"captured_at": {Type: "string", Format: "date-time", Description: "RFC3339 capture time with timezone"},
						"location": {
							Type:        "object",
							Description: "Capture location in latitude/longitude order",
							Required:    []string{"lat", "lon"},
							Properties: map[string]tools.Property{
								"lat":        {Type: "number", Description: "Latitude (-90..90)"},
								"lon":        {Type: "number", Description: "Longitude (-180..180)"},
								"accuracy_m": {Type: "number", Description: "Optional non-negative accuracy in metres"},
								"label":      {Type: "string", Description: "Optional human-readable place"},
							},
							AdditionalProperties: boolPtr(false),
						},
						"source_kind": {
							Type:    "string",
							Enum:    []string{"api", "web", "cli", "mcp", "mobile", "ai_device", "import", "other"},
							Default: "mcp",
						},
						"source_name": {Type: "string", Description: "Optional non-sensitive source/device description"},
					},
					AdditionalProperties: boolPtr(false),
				},
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
			sourceMetadata, err := fileSourceMetadataArg(args)
			if err != nil {
				return nil, err
			}

			var out map[string]any
			if err := c.UploadMultipartWithSourceMetadata(ctx, name, mime, folder, body, tags, sourceMetadata, &out); err != nil {
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
				"scope": {Type: "string", Description: "Optional virtual-folder scope, e.g. /Projects/mem"},
				"type":  {Type: "string", Description: "MIME prefix filter: image|text|application|audio|video"},
				"route": {Type: "string", Description: "Search route: text|visual|auto (default auto fuses both)", Enum: []string{"text", "visual", "auto"}},
				"since": {Type: "string", Description: "YYYY-MM-DD lower bound on timeline_at"},
				"until": {Type: "string", Description: "YYYY-MM-DD upper bound on timeline_at"},
				"limit": {Type: "integer", Description: "Max results (default 10, max 100)", Default: 10},
				"idempotency_key": {
					Type: "string",
					Description: "Stable key for safely replaying the same managed request; " +
						"generated for one call when omitted",
				},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			q, _ := args["query"].(string)
			if q == "" {
				return nil, fmt.Errorf("mem_search: query is required")
			}
			body := map[string]any{"query": q}
			if v, _ := args["scope"].(string); v != "" {
				body["scope"] = v
			}
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
			headers, err := managedEmbeddingHeaders("search", args)
			if err != nil {
				return nil, err
			}
			var out map[string]any
			if err := c.DoJSONWithHeaders(
				ctx,
				http.MethodPost,
				"/v1/search",
				body,
				&out,
				headers,
			); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

// --- context tool ---

func registerContext(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_context",
		Description: "Recall a bounded, source-verifiable evidence pack for the calling Agent. " +
			"Returns citations, excerpts, content hashes, and source URLs; mem does not synthesize an answer.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"query"},
			Properties: map[string]tools.Property{
				"query":  {Type: "string", Description: "What the Agent needs to remember"},
				"scope":  {Type: "string", Description: "Optional virtual-folder scope, e.g. /Projects/mem"},
				"source": {Type: "string", Description: "Evidence source: all|file|memory", Enum: []string{"all", "file", "memory"}, Default: "all"},
				"type":   {Type: "string", Description: "MIME prefix filter: image|text|application|audio|video"},
				"memory_kind": {Type: "string", Description: "Structured memory kind filter",
					Enum: []string{"observation", "decision", "preference", "task_state", "fact", "note", "artifact"}},
				"since":     {Type: "string", Description: "YYYY-MM-DD inclusive lower bound"},
				"until":     {Type: "string", Description: "YYYY-MM-DD inclusive upper bound"},
				"limit":     {Type: "integer", Description: "Max evidence items (default 8, max 50)", Default: 8},
				"max_chars": {Type: "integer", Description: "Total context character budget (default 12000)", Default: 12000},
				"idempotency_key": {
					Type: "string",
					Description: "Stable key for safely replaying the same managed request; " +
						"generated for one call when omitted",
				},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			q, _ := args["query"].(string)
			if strings.TrimSpace(q) == "" {
				return nil, fmt.Errorf("mem_context: query is required")
			}
			body := map[string]any{"query": q}
			for _, key := range []string{"scope", "source", "type", "memory_kind", "since", "until"} {
				if v, _ := args[key].(string); v != "" {
					body[key] = v
				}
			}
			for _, key := range []string{"limit", "max_chars"} {
				if v := args[key]; v != nil {
					body[key] = v
				}
			}
			headers, err := managedEmbeddingHeaders("context", args)
			if err != nil {
				return nil, err
			}
			var out map[string]any
			if err := c.DoJSONWithHeaders(
				ctx,
				http.MethodPost,
				"/v1/context",
				body,
				&out,
				headers,
			); err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

func managedEmbeddingHeaders(operation string, args map[string]any) (map[string]string, error) {
	key, _ := args["idempotency_key"].(string)
	key = strings.TrimSpace(key)
	if len(key) > 200 {
		return nil, fmt.Errorf("%s: idempotency_key must be at most 200 characters", operation)
	}
	if key == "" {
		key = "mcp-" + operation + "-" + uuid.NewString()
	}
	return map[string]string{"Idempotency-Key": key}, nil
}

// --- related tool ---

func registerRelated(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_related",
		Description: "Given a file_id, return the top-K files most related by embedding " +
			"similarity — the \"open a contract, surface its receipts/chats\" link-out. " +
			"Use after mem_search/mem_info when you have a file_id and want neighbours. " +
			"Returns {file_id, related[]}. SPEC §8.1.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"file_id"},
			Properties: map[string]tools.Property{
				"file_id": {Type: "string", Description: "Anchor file id to find neighbours of"},
				"type":    {Type: "string", Description: "Relation type filter", Enum: []string{"same_topic", "same_event", "same_person"}},
				"limit":   {Type: "integer", Description: "Max results (default 10, max 100)", Default: 10},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			id, _ := args["file_id"].(string)
			if id == "" {
				return nil, fmt.Errorf("mem_related: file_id is required")
			}
			q := url.Values{}
			if v, _ := args["type"].(string); v != "" {
				q.Set("type", v)
			}
			if v := args["limit"]; v != nil {
				q.Set("limit", fmt.Sprintf("%v", v))
			}
			path := "/v1/files/" + url.PathEscape(id) + "/related"
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

// --- face tool ---

func registerFace(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_face",
		Description: "Manage person clusters detected in images. action=list returns " +
			"{clusters[]} (id + size + name); action=name labels a cluster so later " +
			"searches like \"photos with Xiao Ming\" resolve; action=merge folds one " +
			"cluster into another. SPEC §F6.1 / §8.1.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"action"},
			Properties: map[string]tools.Property{
				"action":     {Type: "string", Description: "list | name | merge", Enum: []string{"list", "name", "merge"}},
				"cluster_id": {Type: "string", Description: "Target cluster id (required for name/merge; the surviving cluster for merge)"},
				"name":       {Type: "string", Description: "Person name to assign (required for action=name)"},
				"merge_id":   {Type: "string", Description: "Cluster id to fold into cluster_id, then removed (required for action=merge)"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			action, _ := args["action"].(string)
			var out map[string]any
			switch action {
			case "list":
				if err := c.DoJSON(ctx, http.MethodGet, "/v1/faces", nil, &out); err != nil {
					return nil, err
				}
			case "name":
				id, _ := args["cluster_id"].(string)
				name, _ := args["name"].(string)
				if id == "" || name == "" {
					return nil, fmt.Errorf("mem_face: action=name requires cluster_id and name")
				}
				path := "/v1/faces/" + url.PathEscape(id) + "/name"
				if err := c.DoJSON(ctx, http.MethodPost, path, map[string]any{"name": name}, &out); err != nil {
					return nil, err
				}
			case "merge":
				id, _ := args["cluster_id"].(string)
				into, _ := args["merge_id"].(string)
				if id == "" || into == "" {
					return nil, fmt.Errorf("mem_face: action=merge requires cluster_id (keep) and merge_id (folded in)")
				}
				path := "/v1/faces/" + url.PathEscape(id) + "/merge"
				if err := c.DoJSON(ctx, http.MethodPost, path, map[string]any{"into": into}, &out); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("mem_face: action must be one of list|name|merge, got %q", action)
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

func rememberObjectArg(args map[string]any, key string) (map[string]any, bool, error) {
	value, exists := args[key]
	if !exists || value == nil {
		return nil, false, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("mem_remember: %s must be an object", key)
	}
	return object, true, nil
}

func fileSourceMetadataArg(args map[string]any) (*apiclient.FileSourceMetadata, error) {
	metadata := &apiclient.FileSourceMetadata{SourceKind: "mcp"}
	value, exists := args["source_metadata"]
	if !exists || value == nil {
		return metadata, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("mem_put: source_metadata must be an object")
	}
	allowed := map[string]bool{
		"captured_at": true, "location": true, "source_kind": true, "source_name": true,
	}
	for key := range object {
		if !allowed[key] {
			return nil, fmt.Errorf("mem_put: source_metadata contains unsupported field %q", key)
		}
	}
	var typeOK bool
	if capturedAt, exists := object["captured_at"]; exists {
		metadata.CapturedAt, typeOK = capturedAt.(string)
		if !typeOK {
			return nil, fmt.Errorf("mem_put: source_metadata.captured_at must be a string")
		}
	}
	if sourceKind, exists := object["source_kind"]; exists {
		metadata.SourceKind, typeOK = sourceKind.(string)
		if !typeOK {
			return nil, fmt.Errorf("mem_put: source_metadata.source_kind must be a string")
		}
	}
	switch metadata.SourceKind {
	case "api", "web", "cli", "mcp", "mobile", "ai_device", "import", "other":
	default:
		return nil, fmt.Errorf("mem_put: unsupported source_metadata.source_kind %q", metadata.SourceKind)
	}
	if sourceName, exists := object["source_name"]; exists {
		metadata.SourceName, typeOK = sourceName.(string)
		if !typeOK {
			return nil, fmt.Errorf("mem_put: source_metadata.source_name must be a string")
		}
	}
	if rawLocation, exists := object["location"]; exists {
		location, ok := rawLocation.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("mem_put: source_metadata.location must be an object")
		}
		for key := range location {
			switch key {
			case "lat", "lon", "accuracy_m", "label":
			default:
				return nil, fmt.Errorf("mem_put: source_metadata.location contains unsupported field %q", key)
			}
		}
		lat, latOK := numberArg(location["lat"])
		lon, lonOK := numberArg(location["lon"])
		if !latOK || !lonOK || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			return nil, fmt.Errorf("mem_put: source_metadata.location requires valid lat (-90..90) and lon (-180..180)")
		}
		result := &apiclient.FileSourceLocation{Lat: lat, Lon: lon}
		if rawAccuracy, exists := location["accuracy_m"]; exists {
			accuracy, ok := numberArg(rawAccuracy)
			if !ok || math.IsNaN(accuracy) || math.IsInf(accuracy, 0) || accuracy < 0 {
				return nil, fmt.Errorf("mem_put: source_metadata.location.accuracy_m must be non-negative")
			}
			result.AccuracyM = &accuracy
		}
		if rawLabel, exists := location["label"]; exists {
			result.Label, typeOK = rawLabel.(string)
			if !typeOK {
				return nil, fmt.Errorf("mem_put: source_metadata.location.label must be a string")
			}
		}
		metadata.Location = result
	}
	return metadata, nil
}

func numberArg(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, !math.IsNaN(number) && !math.IsInf(number, 0)
	case float32:
		result := float64(number)
		return result, !math.IsNaN(result) && !math.IsInf(result, 0)
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	default:
		return 0, false
	}
}

func boolPtr(value bool) *bool {
	return &value
}
