# mem MCP Server

`mem-mcp` is the Model Context Protocol server that lets agents (Claude
Desktop, Cursor, Cline, …) read and write the mem AI drive through MCP
tools — same backend, same auth, no glue code.

It's a thin wrapper over the memd HTTP API. Every MCP tool is registered
once in [`internal/tools/builtin/`](../server/internal/tools/builtin/) and
automatically appears on `tools/list`. Adding a new agent-callable
capability is a single registration — no need to touch the MCP server,
the CLI, or the HTTP server independently.

---

## Architecture

```
  ┌──────────────────┐         ┌──────────────────┐
  │   mem CLI        │         │   mem-mcp        │  ← Claude Desktop /
  │   (cobra)        │         │   (stdio,        │     Cursor / Cline
  │                  │         │    JSON-RPC 2.0) │     spawn this
  └────────┬─────────┘         └────────┬─────────┘
           │                            │
           │      ┌─────────────────┐   │
           └──────▶  tools.Registry ◀───┘   ← single source of truth
                  │  (name, schema, │
                  │   handler)      │
                  └────────┬────────┘
                           │
                  ┌────────▼────────┐
                  │   apiclient     │   ← shared HTTP client +
                  │                 │     typed errors
                  └────────┬────────┘
                           │  HTTPS / HTTP
                  ┌────────▼────────┐
                  │   memd (REST)   │   ← canonical surface
                  └─────────────────┘
```

Tool flow:
1. Agent calls `tools/call` with a tool name + JSON args.
2. `mem-mcp` looks the tool up in `tools.Registry`.
3. The tool's `Run` handler invokes `apiclient` against memd.
4. Result is returned as an MCP `content` block (text with JSON inside).

---

## Built-in tools (W1)

| Tool | Description |
|------|-------------|
| `mem_put` | Upload content (text or base64 binary) and trigger AI indexing |
| `mem_get` | Read file content; binary returned base64-encoded, capped at 4 MiB |
| `mem_info` | File metadata + AI fields (caption / summary / tags / timeline_at / index_status) |
| `mem_list` | List files with filters (tag / mime-prefix / since / until / path-prefix) |
| `mem_ls` | List immediate subfolders + files under a folder path |
| `mem_mkdir` | Create folder (mkdir -p semantics) |
| `mem_mv` | Move file to a different folder, or rename in place |
| `mem_folder_tree` | Full folder tree as nested structure |

## Built-in tools (W3 / W4)

These plug into the same registry and ship today (12 tools total):

| Tool | Description |
|------|-------------|
| `mem_search` | Natural-language search (text / visual / auto fuse); ranked files + snippets |
| `mem_ask` | Cross-file RAG: synthesized answer with citations |
| `mem_related` | Top-K files related to a `file_id` by embedding similarity |
| `mem_face` | Person clusters: `action=list` / `name` / `merge` |

---

## Build

```bash
make build-mem-mcp     # produces ./bin/mem-mcp
```

Or directly:

```bash
cd server && go build -o ../bin/mem-mcp ./cmd/mem-mcp
```

The binary is single-file with no runtime dependencies beyond a reachable
`memd` instance.

---

## Configuration

`mem-mcp` reads configuration in order of precedence: command-line flag →
environment variable → built-in default.

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `--server` | `MEM_SERVER` | `http://localhost:8787` | memd base URL |
| `--token` | `MEM_TOKEN` | _(empty)_ | Bearer token; required for any non-public operation |

Create a token first:

```bash
mem auth login                                         # creates a 24h admin token
mem auth token create --name claude-desktop --scope read,write
# → copy the printed token; show-once.
```

---

## Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) — or the equivalent on Windows / Linux:

```json
{
  "mcpServers": {
    "mem": {
      "command": "/absolute/path/to/bin/mem-mcp",
      "env": {
        "MEM_SERVER": "http://localhost:8787",
        "MEM_TOKEN":  "mem_..."
      }
    }
  }
}
```

Restart Claude Desktop. The mem tools appear in the tool picker. Try:

> "把这段文字保存为 hello.txt 放在 /Notes 下面"
>
> "列出 /Photos 下面有什么"

---

## Cursor / Cline

Both follow the same `mcpServers` shape:

```json
{
  "mcpServers": {
    "mem": {
      "command": "mem-mcp",
      "env": { "MEM_SERVER": "http://localhost:8787", "MEM_TOKEN": "mem_..." }
    }
  }
}
```

Use the absolute path if `mem-mcp` isn't on `PATH`.

---

## Protocol details

- **Transport**: newline-delimited JSON-RPC 2.0 over stdio. Each message is one line. `stdout` is reserved for MCP messages; all logging goes to `stderr`.
- **Protocol version**: `2024-11-05` (baseline).
- **Methods implemented**: `initialize`, `notifications/initialized`, `tools/list`, `tools/call`, `ping`.
- **Tool errors**: returned in-band as `{ content: [{type:"text", text:"..."}], isError: true }` so the LLM can read and react. JSON-RPC errors are reserved for protocol-level failures (parse, method-not-found, invalid-params).

---

## Smoke test (no Claude needed)

```bash
make build-mem-mcp
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | MEM_SERVER=http://localhost:8787 MEM_TOKEN=$YOUR_TOKEN ./bin/mem-mcp
```

You should see two JSON-RPC responses: the server handshake, and a
`tools/list` payload with all registered tools and their input schemas.

---

## Adding a new tool

1. Add a new `registerXxx(reg, client)` function in
   [`server/internal/tools/builtin/`](../server/internal/tools/builtin/).
2. Append it to the `RegisterAll` list.
3. Write a unit test that asserts the HTTP request shape it emits.

The CLI doesn't auto-gain the command — yet — because cobra needs
hand-written flag wiring for good UX. The current pattern (CLI subcommand
calls apiclient directly) is intentional and stays unless we have a reason
to refactor. The Registry's job is to keep agent-facing surfaces aligned;
CLI follows when there's value.
