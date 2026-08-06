# mem MCP Server

`mem-mcp` lets Claude Desktop, Cursor, Cline and other agents use mem as an
**external memory brain** through the Model Context Protocol. Agents can write
source material, retrieve evidence-backed context and read original assets
through the same backend and authorization model as the AI drive.

MCP is an adapter, not the memory implementation. It does not host a model,
run an Agent loop or create a second copy of business logic. `mem-mcp` is a
thin wrapper over the canonical memd HTTP API; tools are registered in
[`internal/tools/builtin/`](../server/internal/tools/builtin/) and appear on
`tools/list`.

The product boundary is deliberate:

- **mem** owns durable memory, provenance, consolidation, retrieval and feedback.
- **The calling Agent** owns planning, reasoning, tool selection and the final answer.
- **The AI drive UI** gives the user visibility and control over the same memory.

---

## Architecture

```text
  mem CLI (human / scripts) ───────────────┐
                                           │ HTTP
  Agent host                               ▼
      └─ stdio JSON-RPC ─▶ mem-mcp ─▶ tools.Registry
                                      └─ apiclient ─▶ memd REST API
                                                           │
                                                           ▼
                                                    Memory Plane
```

The REST API is the semantic source of truth. `tools.Registry` is specifically
the MCP schema/dispatch layer; the CLI calls the same API directly rather than
pretending to be an MCP client.

Tool flow:

1. Agent calls `tools/call` with a tool name + JSON args.
2. `mem-mcp` looks the tool up in `tools.Registry`.
3. The tool's `Run` handler invokes `apiclient` against memd.
4. Result is returned as an MCP `content` block (text with JSON inside).

No MCP-only storage or index is created.

---

## Built-in tools (W1)

| Tool | Description |
|------|-------------|
| `mem_put` | Upload content (text or base64 binary) and trigger AI indexing |
| `mem_get` | Read file content; binary returned base64-encoded, capped at 4 MiB |
| `mem_info` | File metadata + AI fields (caption / summary / tags / timeline_at / index_status) |
| `mem_file_annotation_decide` | Accept or reject one pending AI description/tag suggestion |
| `mem_list` | List files with filters (tag / mime-prefix / since / until / path-prefix) |
| `mem_ls` | List immediate subfolders + files under a folder path |
| `mem_mkdir` | Create folder (mkdir -p semantics) |
| `mem_mv` | Move file to a different folder, or rename in place |
| `mem_folder_tree` | Full folder tree as nested structure |

### File provenance with `mem_put`

`mem_put.source_metadata` forwards the canonical upload provenance object to
memd. It is not an arbitrary metadata bag:

```json
{
  "name": "photo.jpg",
  "content": "<base64>",
  "encoding": "base64",
  "source_metadata": {
    "captured_at": "2026-07-29T08:00:00+08:00",
    "location": {
      "lat": 31.2304,
      "lon": 121.4737,
      "accuracy_m": 8,
      "label": "Shanghai"
    },
    "source_kind": "mobile",
    "source_name": "camera sync"
  }
}
```

If omitted, the adapter records `source_kind=mcp`. Unknown fields, invalid
coordinates, timezone-free timestamps and control characters are rejected by
the HTTP API. The metadata is persisted server-side and is not included in an
enrichment-model prompt.

### Reviewing file annotations

Use `mem_info` (or `mem info <file_id> --format json`) to read pending
annotations and their `state_version`. Human-facing CLI review has two explicit
commands:

```bash
mem annotation accept <file_id> <annotation_id> --expected-version 1
mem annotation reject <file_id> <annotation_id> --expected-version 1
```

Agents use the single non-overlapping MCP mutation tool:

```json
{
  "file_id": "9baadf78-6ad1-47a7-a719-57122f352a67",
  "annotation_id": "441bcc02-9fe2-44bb-a68b-8dd9a190fb6e",
  "decision": "accepted",
  "expected_version": 1
}
```

Both adapters call the canonical
`PUT /v1/files/{fileID}/annotations/{annotationID}` endpoint. The server
enforces token `read + write` permission, workspace/path scope, and optimistic
concurrency. Repeating the same terminal decision succeeds with
`replayed=true`; an opposite decision returns
`409 annotation_decision_conflict`, while a stale pending version returns
`409 annotation_version_conflict`. Reload `mem_info` before retrying a version
conflict.

## Memory, recall and relation tools

The canonical product surface is:

| Tool | Description |
|------|-------------|
| `mem_remember` | Idempotently persist an observation, decision, preference, task state, fact, note or artifact reference |
| `mem_memory_list` | List bounded structured-memory summaries; `mem_list` remains the file list |
| `mem_memory_get` | Get one full structured memory by UUID within the token path boundary |
| `mem_feedback` | Record useful/not-useful or pin/unpin feedback with optimistic concurrency |
| `mem_archive` / `mem_restore` | Reversibly exclude a memory from or return it to normal recall |
| `mem_forget` | Irreversibly redact one live memory payload after explicit confirmation |
| `mem_checkpoint` | Persist a versioned task checkpoint or an explicit handoff to another Agent/device |
| `mem_task_list` | List bounded resumable-task summaries |
| `mem_checkpoint_list` | List newest-first bounded checkpoint summaries for one task |
| `mem_checkpoint_get` | Get one immutable checkpoint and its full handoff payload |
| `mem_resume` | Restore the current task head or a selected historical checkpoint, including resolved and missing evidence |
| `mem_search` | Natural-language search (text / visual / auto fuse); ranked files + snippets |
| `mem_context` | Build an evidence-backed context pack for the calling Agent |
| `mem_related` | Top-K files related to a `file_id` by embedding similarity |
| `mem_face` | Person clusters: `action=list` / `name` / `merge` |
| `mem_durable_context_recall` | Resume explicitly granted, workspace-scoped active memories for one principal through the pinned `durable-context.v1` contract |

`mem_ask` has been retired. There is one recall pipeline: `mem_context`
retrieves evidence, while the calling Agent synthesizes the answer.

Scoped durable context (cross-session resume of explicitly approved context)
is a separate pinned contract; see [`DURABLE_CONTEXT.md`](./DURABLE_CONTEXT.md).
Grant administration stays on the authenticated HTTP/admin surface.

## `mem_checkpoint` and `mem_resume`

Free-form memory and resumable task state serve different purposes:

- `mem_remember` records one durable occurrence.
- `mem_checkpoint` commits a complete, versioned task revision.
- `mem_resume` deterministically restores that revision and then, when the
  token also has `search`, may attach a bounded Context Pack.

The portable payload contract is
[`handoff.v1.schema.json`](schemas/handoff.v1.schema.json); a complete example
is in [`handoff.v1.example.json`](examples/handoff.v1.example.json).

The first checkpoint uses `base_checkpoint_id: null`. Every later write must
name the current head checkpoint. This compare-and-swap rule prevents two
Agents from silently overwriting one another. Reusing an idempotency key with
the identical payload returns the original checkpoint; reusing it with
different content is a conflict.

Typical handoff:

1. Claude Code calls `mem_checkpoint` with
   `checkpoint_kind: "handoff"` and a stable `task_key`.
2. Codex calls `mem_resume` with the same `task_key`.
3. Codex reads `state.goal`, `progress`, `decisions`, `next_steps`,
   `blockers`, `workspace_state`, plus `resolved`/`missing` references.
4. Codex continues the work and writes the next checkpoint using the returned
   checkpoint ID as `base_checkpoint_id`.

`complete: false` means at least one required artifact could not be resolved
or failed its SHA-256 check. The Agent must report that gap instead of
pretending the restore is complete.

For explicit inspection rather than resume, use `mem_task_list` to discover
task keys, `mem_checkpoint_list` to page through one task's immutable history,
and `mem_checkpoint_get` to read a selected versioned payload. All three use
the same workspace/path authorization and not-found semantics as the canonical
HTTP endpoints. `mem_checkpoint_list` never returns full handoff payloads or
reference arrays: each item contains immutable identity fields, status, a
500-code-point `progress_excerpt`, its original `progress_length`, and
completed/reference counts. Fetch only the selected checkpoint when an Agent
needs its full state.

## `mem_remember`, `mem_context`, and citations

`mem_remember` writes one immutable, auditable occurrence. The caller must use
a stable `idempotency_key` for retries. Replaying the same normalized request
with that key returns the original memory ID; reusing the key for a different
request returns `idempotency_conflict`.

```json
{
  "content": "Use PostgreSQL for deterministic lexical recall",
  "kind": "decision",
  "path": "/Projects/mem",
  "idempotency_key": "task-42-recall-store-v1",
  "source_type": "agent",
  "agent_id": "codex",
  "session_id": "session-7",
  "task_id": "task-42",
  "attributes": {"confidence": "confirmed"}
}
```

The key is an MCP argument but is sent to the canonical HTTP API only through
the `Idempotency-Key` header. A new record returns `replayed:false`; an
equivalent retry returns the same ID with `replayed:true`; reusing the key for
a changed request returns `409 idempotency_conflict`. Public memory JSON hides
the token ID, idempotency key and request hash, while retaining auditable
workspace and creator fields.

## Memory inspection, feedback, and forgetting

`mem_memory_list` returns bounded summaries with `excerpt`, lifecycle state,
pin/feedback projections, `state_version`, provenance identifiers and an
opaque `next_cursor`. It never returns every record's full content. Pass the
cursor back unchanged with the same filters and Token path boundary.
`mem_memory_get` resolves one known UUID and returns its full content and
provenance; absent, cross-workspace and out-of-path records deliberately share
the same not-found response.

All control writes require the `state_version` last inspected by the caller
and a stable `idempotency_key`:

```json
{
  "memory_id": "4d394c42-5bc8-4ab8-9df4-d2121fe6d74a",
  "action": "useful",
  "expected_version": 3,
  "idempotency_key": "task-42-useful-v3"
}
```

A stale version returns `memory_version_conflict`; reload before making a new
intent with a new key. Retrying an already committed identical intent returns
`replayed:true`. Reusing a key for another action, version or memory is an
idempotency conflict. Successful feedback/archive/restore calls return only
bounded control state and event metadata; they do not echo full content,
attributes, paths or source locators into the Agent context.

Archive is reversible and excludes the record from normal `mem_context`
recall. Restore makes it eligible again. `mem_forget` is stronger:
`confirm:true` is mandatory, the live content/source/producer payload is
redacted, path/creator metadata is generalized, and stale retries of the
original `mem_remember` key cannot recreate that occurrence. Raw idempotency
keys are represented only by digests at rest. It does not delete a separately
stored source file. WAL, replicas and backups remain subject to the
deployment's retention policy, so this is not a
cryptographic-media-erasure claim.

## `mem_search`, `mem_context`, and `mem_get`

Use the narrowest tool that matches the Agent's need:

| Need | Tool | Expected result |
|------|------|-----------------|
| Discover candidate files | `mem_search` | Ranked assets and short matching snippets |
| Prepare context for the next reasoning step | `mem_context` | Budgeted context items with citations, provenance and confidence |
| Read a known original asset | `mem_get` | The original text or binary content |
| Explore associations from a known asset | `mem_related` | Typed related assets and relation evidence |

`mem_context` is not a chatbot. Its target input is a task or question plus
optional scope and context budget:

```json
{
  "query": "继续上次的合同审阅，重点检查续费和解约风险",
  "scope": "/Contracts",
  "source": "all",
  "memory_kind": "decision",
  "idempotency_key": "contract-review-context-v1",
  "limit": 8,
  "max_chars": 12000
}
```

In SaaS, `mem_search` and the file lane of `mem_context` may use the optional
platform-managed embedding service. Both tools accept `idempotency_key`; the
adapter sends it only as the HTTP `Idempotency-Key` header. If omitted, the
adapter creates a key for that one tool call. An Agent that might repeat the
same logical request should supply and retain a stable key so a committed
result can replay without another provider invocation or charge. A `504`
means the provider outcome is uncertain: do not automatically retry, and do
not invent a new key. `mem_context` with `source=memory` stays lexical and
model-independent.

Its target output is structured for an Agent to consume:

```json
{
  "query": "继续上次的合同审阅，重点检查续费和解约风险",
  "scope": "/Contracts",
  "source": "all",
  "evidence": [
    {
      "evidence_id": "…",
      "source_kind": "memory",
      "source_id": "…",
      "citation": "mem://memories/…",
      "memory_id": "…",
      "memory_kind": "decision",
      "path": "/Contracts",
      "content_sha256": "…",
      "excerpt": "…",
      "locator": {"kind": "memory_text"},
      "score": 0.91,
      "route": "memory_lexical",
      "reason": "exact",
      "provenance": {
        "source_type": "agent",
        "agent_id": "codex",
        "task_id": "task-42"
      }
    }
  ],
  "total_chars": 4280,
  "partial": false,
  "retrieved_at": "2026-07-28T10:00:00Z"
}
```

For `source=all`, file and memory lanes run independently. If one lane fails
but the other returns evidence, the response sets `partial=true` and includes
an explicit warning with code `file_retrieval_unavailable` or
`memory_retrieval_unavailable`; callers must not mistake a partial pack for a
complete search. If a requested lane fails and no other lane returns evidence,
the API fails with `502 context_unavailable`. In MCP, a partial pack is a
successful tool result, not `isError:true`, so the Agent must inspect
`partial/warnings`.

Filter combinations are explicit: `source=memory` cannot be combined with the
file MIME `type` filter, and `source=file` cannot be combined with
`memory_kind`. For backward compatibility, omitting `source` while providing
`type` keeps file-only semantics.

The calling Agent remains responsible for deciding what the evidence means and
for producing the user-facing answer.

## MCP in the memory loop

| Stage | MCP responsibility |
|------|--------------------|
| **Write** | `mem_put` stores original assets; `mem_remember` stores structured occurrences with provenance |
| **Consolidate** | Asynchronous server-side work; MCP may read index status but does not duplicate processing |
| **Recall** | `mem_search`, `mem_context`, `mem_related`; memory-only recall does not require a model |
| **Use** | `mem_info` and `mem_get` resolve file citations; memory citations currently resolve through `GET /v1/memories/{id}` |
| **Feedback** | `mem_file_annotation_decide`, `mem_feedback`, `mem_archive`, `mem_restore` and confirmed `mem_forget`; future corrections must preserve provenance |

See [Agent Memory Direction](AGENT_MEMORY_DIRECTION.md) for the product loop,
data model direction and acceptance criteria.

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

Versioned configuration shapes for OpenClaw, Hermes Agent, Claude Code,
OpenCode, and Codex—as well as the strict meaning of `REGISTERED`,
`DISCOVERED`, `INVOKED`, and `NOT RUN`—are maintained in
[Agent host certification](integrations/agent-hosts.md). Those fixtures keep
tokens as runtime environment references and are parsed in CI. A successful
config parse is not reported as a tool invocation.

`mem-mcp` reads configuration in order of precedence: command-line flag →
environment variable → built-in default.

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `--server` | `MEM_SERVER` | `http://localhost:8787` | memd base URL |
| `--token` | `MEM_TOKEN` | _(empty)_ | Bearer token; required for any non-public operation |
| `--workspace` | `MEM_WORKSPACE` | _(personal workspace)_ | Explicit memory workspace UUID |

Create a token first:

```bash
mem auth login                                         # creates a 24h admin token
mem auth token create --name claude-desktop \
  --scope search,read,write \
  --path /AgentMemory
# → copy the printed token; show-once.
```

New Agent tokens are bound to the workspace selected when they are created.
`--workspace`/`MEM_WORKSPACE` selects that workspace; the same token cannot be
reused to switch into another membership.

Permission rules are enforced by memd, not by MCP arguments:

- `mem_remember` requires `write`; linking `source_file_id` also requires `read`.
- `mem_memory_list` and `mem_memory_get` require `read`.
- `mem_file_annotation_decide` requires `read + write`.
- `mem_feedback`, `mem_archive` and `mem_restore` require `read + write`.
- `mem_forget` requires `delete` plus a workspace role that permits deletion.
- `mem_checkpoint` requires `write`; referenced `mem://` evidence also requires
  `read`.
- `mem_task_list`, `mem_checkpoint_list` and `mem_checkpoint_get` require
  `read`.
- `mem_resume` requires `read`; its optional related-evidence enrichment runs
  only with `search`.
- `mem_context` requires `search`; resolving a memory or original file requires
  `read`.
- Memory paths must fall under the Token's `paths[]` boundary.
- Workspace identity comes from the bound Token and selected workspace, never
  from a caller-supplied memory field.

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

> "把这段文字保存为 hello.txt 放在 /AgentMemory/Notes 下面"
>
> "列出 /AgentMemory/Photos 下面有什么"
>
> "从 mem 准备一份上下文，让我继续上次的合同审阅"
>
> "记住我们决定使用 PostgreSQL，并标记为 task-42 的决定"

---

## Claude Code

Claude Code supports local stdio MCP servers through `claude mcp add`.
With an absolute path to the built binary:

```bash
claude mcp add --scope user --transport stdio \
  --env MEM_SERVER=http://localhost:8787 \
  --env MEM_TOKEN=mem_... \
  --env MEM_WORKSPACE=00000000-0000-0000-0000-000000000000 \
  mem -- /absolute/path/to/bin/mem-mcp

claude mcp list
```

Then ask Claude Code:

> “任务结束前调用 `mem_checkpoint`，task_key 用
> `project-x/migration`，checkpoint_kind 用 `handoff`，完整记录当前进度、
> 决策、阻塞和下一步。”

Claude Code's current MCP command and scope semantics are documented in the
[official Claude Code MCP guide](https://code.claude.com/docs/en/mcp).

## Codex

Codex CLI and the Codex desktop app share MCP configuration. Register the same
stdio server:

```bash
codex mcp add mem \
  --env MEM_SERVER=http://localhost:8787 \
  --env MEM_TOKEN=mem_... \
  --env MEM_WORKSPACE=00000000-0000-0000-0000-000000000000 \
  -- /absolute/path/to/bin/mem-mcp

codex mcp list
```

Start Codex in the target project and ask:

> “调用 `mem_resume` 恢复 task_key `project-x/migration`。先报告缺失或
> hash 不一致的必需产物，再按 next_steps 接着做。”

Codex's supported transports, CLI command and shared configuration behavior
are documented in the
[official Codex MCP guide](https://learn.chatgpt.com/docs/extend/mcp?surface=cli).

Use a least-privilege workspace-bound token for each Agent host. The examples
show literal placeholders for clarity; do not commit real tokens to
`.mcp.json`, `.codex/config.toml` or the repository.

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
- **Tool errors**: returned in-band as `{ content: [{type:"text", text:"..."}], isError: true }` so the calling Agent can read and react. JSON-RPC errors are reserved for protocol-level failures (parse, method-not-found, invalid-params).

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
`tools/list` payload with every registered tool and its input schema.

---

## Adding a new tool

1. Add a new `registerXxx(reg, client)` function in
   [`server/internal/tools/builtin/`](../server/internal/tools/builtin/).
2. Append it to the `RegisterAll` list.
3. Write a unit test that asserts the HTTP request shape it emits.
4. Document whether it belongs to write, consolidate, recall, use or feedback;
   do not add a second tool with overlapping semantics.

The CLI doesn't auto-gain the command — yet — because cobra needs
hand-written flag wiring for good UX. The current pattern (CLI subcommand
calls apiclient directly) is intentional and stays unless we have a reason
to refactor. The Registry's job is to keep agent-facing surfaces aligned;
CLI follows when there's value.
