# mem · server (Go backend)

> Canonical Memory Plane API, CLI and MCP adapter.
> Files, structured Agent memories and Context Packs share one authorization
> and service layer. See `/SPEC.md` for the full project spec.

## Layout

```
server/
├── cmd/
│   ├── memd/          — HTTP daemon entrypoint (default :8787)
│   ├── mem/           — User-facing CLI (cobra)
│   └── mem-mcp/       — stdio MCP adapter over the HTTP API
├── internal/
│   ├── api/           — chi HTTP router + handlers + middleware
│   ├── auth/          — User + Token (bcrypt + SHA-256 hash), scope checks
│   ├── config/        — env-var driven config loader (MEM_*)
│   ├── db/            — pgx pool + goose embedded migrations
│   ├── contextpack/   — bounded file + structured-memory evidence packs
│   ├── db/migrations/ — embedded, ordered SQL migrations
│   ├── file/          — Ingestion (SHA-256, 秒传 dedup), retrieval, listing
│   ├── folder/        — Folder service (mkdir -p, rename, move, tree)
│   ├── memory/        — model-independent remember/get/lexical recall kernel
│   ├── pathx/         — Virtual-path normalization + validation
│   ├── storage/       — S3-compatible blob store (minio-go)
│   └── workerpb/      — generated gRPC contract for the Python worker
├── go.mod
├── Dockerfile         — multi-stage build (memd / mem / mem-mcp)
└── README.md
```

## Local dev — quick start

```bash
# 1. Start backing services (postgres + redis + minio).
#    From the repo root:
docker compose up -d

# 2. Build + run memd
cd server
go run ./cmd/memd
# logs: "db connected" -> "db migrations applied" -> "http listening :8787"
```

Service URLs in the docker-compose dev stack (host ports shifted for Redis/MinIO
so the stack coexists with other local Redis/MinIO instances):
- Postgres: `localhost:5432` (user/pass: `mem` / `mem`, db `mem`)
- Redis: `localhost:6479`
- MinIO API: `http://localhost:9100` (`mem` / `mem-minio-password`)
- MinIO console: `http://localhost:9101`

## Environment variables

| Var | Default | Description |
|---|---|---|
| `MEM_HTTP_ADDR` | `:8787` | HTTP listen address |
| `MEM_DB_URL` | `postgres://mem:mem@localhost:5432/mem?sslmode=disable` | PostgreSQL DSN (pgvector required) |
| `MEM_REDIS_URL` | `redis://localhost:6479` | Redis URL (asynq queue) |
| `MEM_S3_ENDPOINT` | `http://localhost:9100` | S3-compatible endpoint |
| `MEM_S3_BUCKET` | `mem` | Bucket (auto-created on startup) |
| `MEM_S3_ACCESS_KEY` | `mem` | Access key |
| `MEM_S3_SECRET_KEY` | `mem-minio-password` | Secret key |
| `MEM_S3_REGION` | `us-east-1` | Region tag |
| `MEM_S3_USE_SSL` | derived from endpoint scheme | force TLS |
| `MEM_WORKER_GRPC` | `localhost:50051` | Worker dial target (`make worker` listen addr) |
| `MEM_WORKER_AUTH_KEY_ID` | — | HMAC key identifier; required in SaaS |
| `MEM_WORKER_AUTH_KEY_B64` | — | standard base64 of exactly 32 random bytes; required in SaaS and shared with Worker |
| `MEM_DEPLOYMENT_MODE` | `private` | `private` or `saas`; managed quality profiles are SaaS-only |
| `MEM_AI_PROFILES` | local V1 compatibility + selectable `local-fast-v2` | Comma-separated runtime allowlist; deprecated V1 is hidden from new selection; enable `idealab-quality-v2` explicitly |
| `MEM_MANAGED_EMBEDDING_PROVIDER` | — | Required in SaaS; primary exact compiled V1/V2 embedding provider; the full exact allow-set is derived from enabled profiles |
| `MEM_OPENAI_MANAGED_BINDING` | `false` | Must be `true` only for deprecated hosted `idealab-quality-v1`; distinguishes it from private OpenAI BYOM |
| `MEM_MANAGED_EMBEDDING_RESERVATION_TTL` | `10m` | Managed reservation lease; SaaS values below `6m` are rejected because indexing may run for five minutes |
| `MEM_SESSION_TTL` | `24h` | Login session token TTL |
| `MEM_LOG_LEVEL` | `info` | `debug|info|warn|error` |

### Workspace AI profiles

A workspace profile is a server-owned, fixed AI pipeline; the client selects a
profile ID and cannot supply a provider URL, model name, prompt, or credential.
The default local allowlist retains hidden `local-fast-v1` compatibility and
exposes `local-fast-v2`, which uses
`ollama:qwen3-embedding:0.6b` at 768 dimensions. Install that Ollama artifact
explicitly before selection with `mem model install qwen3-embedding-0.6b-ollama`.
That curated installer checks hardware/disk compatibility, the pinned artifact
digest, and a 768-dimensional output. Selection
performs a Worker-side 768-dimensional probe but never downloads a model or
falls back to a global default. Visual, LLM, VLM, ASR, and rerank stages remain
disabled in the local profile until each has an explicit offline installation
and execution contract.

`idealab-quality-v2` is SaaS-only. An upgrade deployment should explicitly
enable the profile with
`MEM_AI_PROFILES=local-fast-v1,local-fast-v2,idealab-quality-v2`, use the exact
managed embedding spec above, and configure the Worker with
`IDEALAB_BASE_URL` plus a runtime-injected `IDEALAB_API_KEY`. Generic
`OPENAI_*` settings cannot satisfy this binding. It is a pinned routing
contract, not a benchmark claim. Profile changes require a fresh versioned
index generation and rebuild; vectors from distinct providers are never mixed
in place. See [managed profile configuration](../docs/MANAGED_EMBEDDINGS.md)
and [local profile activation](../docs/LOCAL_EMBEDDING_MODELS.md).

SaaS additionally requires the same 32-byte HMAC key on memd and Worker plus
Worker-wide shared Redis replay protection. memd signs every Process request,
checks fetched-content SHA before managed egress through the Worker, and
verifies a response MAC before persistence/accounting. Startup performs a
signed exact-provider readiness challenge. HMAC is not encryption: keep Worker
on a private restricted network and prefer TLS/mTLS. Exact V1 snapshots remain
only for persisted-selection compatibility and are never listed or newly
selected.

If persisted `idealab-quality-v1` workspaces also exist, keep that ID in the
allowlist, set `MEM_OPENAI_MANAGED_BINDING=true`, and mount its explicit
`OPENAI_BASE_URL`/`OPENAI_API_KEY` Worker binding. memd supports both managed
generations in one process, derives an exact two-provider allow-set, and runs a
signed readiness challenge for each. V1 remains hidden and non-selectable; an
existing V1 corpus cannot switch to V2 without a versioned full rebuild.

## Bootstrap a dev user

Registration follows `MEM_REGISTRATION_MODE`. When it is disabled for a local
deployment, insert a development user with bcrypt once, then use
`mem auth login`:

```bash
# Generate a bcrypt hash (any tool works; here using Python for convenience)
python3 -c "import bcrypt;print(bcrypt.hashpw(b'devpassword', bcrypt.gensalt()).decode())"
# -> $2b$12$...

# Insert
psql 'postgres://mem:mem@localhost:5432/mem' -c \
  "INSERT INTO users (email, password_hash) VALUES ('dev@local','<paste-hash>');"

# Login from CLI (writes ~/.mem/config.yaml)
go run ./cmd/mem auth login   # prompts for email + password
```

## HTTP API (v1)

All token-protected routes require `Authorization: Bearer <token>`.

| Method | Path | Scope | Description |
|---|---|---|---|
| `GET`    | `/healthz`                  | — | liveness |
| `GET`    | `/v1/version`               | — | server version |
| `POST`   | `/v1/auth/register`         | — | create a user when deployment registration policy allows it |
| `POST`   | `/v1/auth/login`            | — | email+password → admin-scoped session token |
| `POST`   | `/v1/auth/tokens`           | admin | create token (plaintext returned once) |
| `GET`    | `/v1/auth/tokens`           | admin | list tokens (no secrets) |
| `DELETE` | `/v1/auth/tokens/{id}`      | admin | revoke a token |
| `POST`   | `/v1/memories`              | write | idempotently persist structured memory; requires `Idempotency-Key` |
| `GET`    | `/v1/memories`              | read | list bounded memory summaries with authorization-bound cursor pagination |
| `GET`    | `/v1/memories/{id}`         | read | resolve a stable memory citation within workspace/path scope |
| `POST`   | `/v1/memories/{id}/feedback` | read + write | useful/not-useful or pin/unpin with `expected_version` and `Idempotency-Key` |
| `POST`   | `/v1/memories/{id}/archive` | read + write | reversibly exclude a memory from normal recall |
| `POST`   | `/v1/memories/{id}/restore` | read + write | restore an archived memory to normal recall |
| `POST`   | `/v1/memories/{id}/forget`  | delete + delete-capable workspace role | irreversibly redact the live memory payload; source files are independent |
| `POST`   | `/v1/context`               | search | build a bounded evidence pack from `all`, `file` or `memory` sources |
| `GET`    | `/v1/tasks`                 | read | list path-scoped portable Agent tasks |
| `POST`   | `/v1/tasks/{task_key}/checkpoints` | write (+ read for `mem://` refs) | commit an immutable `mem.handoff` revision; requires `Idempotency-Key` |
| `GET`    | `/v1/tasks/{task_key}/checkpoints` | read | list bounded checkpoint summaries newest first |
| `GET`    | `/v1/tasks/{task_key}/checkpoints/{id}` | read | inspect one versioned handoff |
| `POST`   | `/v1/tasks/{task_key}/resume` | read | restore the task head or selected checkpoint; `search` optionally enriches related context |
| `GET`    | `/v1/workspaces/current/ai-profile` | read | selected snapshot and server-enabled profile catalog |
| `PUT`    | `/v1/workspaces/current/ai-profile` | admin + provider-write workspace role + unrestricted paths | select one server-allowlisted `profile_id` |
| `GET`    | `/v1/workspaces/current/export` | read + admin, unrestricted path, owner/admin role | build and download a validated `.membundle` v1 archive |
| `POST`   | `/v1/workspaces/current/import?mode=fresh` | write + admin, unrestricted path, owner/admin role | validate and atomically restore into an empty workspace |
| `POST`   | `/v1/files`                 | write | upload (multipart `file=`, optional form field `path=/Photos/2012`; or `?stream=1&name=...&path=...`) |
| `GET`    | `/v1/files`                 | read  | list (`?tag=&type=&path=&prefix=&since=&until=&limit=&page=`); `path` = exact folder, `prefix` = subtree (mutually exclusive) |
| `GET`    | `/v1/files/{id}`            | read  | metadata + AI fields |
| `GET`    | `/v1/files/{id}/content`    | read  | stream raw bytes |
| `PATCH`  | `/v1/files/{id}`            | write | body `{name?, path?}` — rename and/or move |
| `GET`    | `/v1/files/{id}/related`    | read  | ranked related files from the relation service |
| `POST`   | `/v1/folders`               | write | body `{path:"/Photos/2012"}` — mkdir -p, idempotent |
| `GET`    | `/v1/folders`               | read  | list direct children of `?parent=/Photos` (defaults to root) |
| `GET`    | `/v1/folders/tree`          | read  | full tree with per-node file counts |
| `PATCH`  | `/v1/folders/{id}`          | write | body `{name?, parent_path?}` — rename and/or move (cascades to descendants in one tx) |
| `DELETE` | `/v1/folders/{id}`          | delete | recursive or empty-folder delete; returns `409` when the subtree retains memory or immutable task checkpoints |

Error envelope (SPEC §8.2): `{"error": "<code>", "hint": "<actionable hint>"}`.

### Structured memory contract

`POST /v1/memories` accepts `kind`, `content`, `path`, `source`,
optional `producer`, `event_at` and `attributes`. `Idempotency-Key` is a
required Header: a new write returns `201/replayed:false`, an equivalent retry
returns the original ID with `200/replayed:true`, and the same key with a
different normalized request returns `409/idempotency_conflict`. The body is
limited to 256 KiB and memory content to 64 KiB. Linking `source.file_id`
additionally requires `read` scope.

Workspace and actor provenance come from authenticated middleware, never the
request body. `X-Workspace-ID` selects an accessible workspace; a token bound
to another workspace receives `403 token_workspace_forbidden`.

`GET /v1/memories` returns a bounded `excerpt` rather than full content and
uses an opaque `(created_at,id)` cursor bound to the normalized filters and the
Token path boundary. Details add the stable `mem://memories/<id>` citation and
public provenance.

`GET /v1/tasks/{task_key}/checkpoints` likewise returns a bounded history
projection: status, a 500-code-point progress excerpt, total progress length,
completed/reference counts and immutable identity fields. Full handoff state
and references are available only through the selected checkpoint detail or
resume endpoint.

Every feedback/lifecycle write requires a stable `Idempotency-Key` and the
memory's current `state_version`. New events return `201`; an equivalent retry
returns the same event with `200/replayed:true`; stale versions and changed
idempotency payloads return `409`. Archive is reversible and removes a record
from normal recall. Mutation responses return bounded control state rather
than echoing content, path or provenance into an MCP context. Forget replaces
the live content/source/producer/path/creator projection with a generic
tombstone, cannot be undone, and does not delete a separately stored source
file. Raw remember/control idempotency keys are never stored. WAL, replicas
and backups remain governed by deployment retention; this is not a claim of
cryptographic media erasure.

`POST /v1/context` accepts `source=all|file|memory`, `memory_kind`, path/date
scope, result limit and character budget. Memory evidence contains
`memory_id`, `memory_kind`, a stable citation, retrieval reason and provenance.
If one requested lane fails but another supplies evidence, the response is
`200` with `partial=true` and `warnings[]`; a failed lane with no surviving
evidence returns `502/context_unavailable`.

## CLI commands

| Command | Description |
|---|---|
| `mem auth login` | Prompt email + password, save token to `~/.mem/config.yaml` |
| `mem auth logout` | Clear local config token |
| `mem auth status` | Verify the saved token and show workspace access |
| `mem auth token create --name X --scope read,write` | Create token (one-time plaintext printed) |
| `mem auth token list` | List tokens |
| `mem auth token revoke <id>` | Revoke |
| `mem profile list` | List the server-enabled workspace AI profile IDs |
| `mem profile status` | Show the selected workspace AI profile snapshot |
| `mem profile select local-fast-v2` | Select an allowlisted profile; the server probes its exact embedding contract |
| `mem put <path>` | Upload a single file (auto MIME) |
| `mem put <path> --to /Photos/2012` | Upload into a virtual folder (mkdir -p) |
| `mem put <dir> --recursive [--to /Albums]` | Upload every file under dir, mirroring the on-disk tree into `--to` |
| `mem put - --name foo.txt [--to /]` | Upload from stdin |
| `mem put <path> --tag x --tag y` | With tags |
| `mem remember <content> --kind decision --path /Projects/x --idempotency-key key` | Write structured Agent memory |
| `mem memory <id> [--scope /Projects/x]` | Get one full structured memory by UUID |
| `mem memories [--scope /Projects/x] [--lifecycle active\|archived\|all]` | List bounded structured-memory summaries |
| `mem feedback <id> --action useful\|not_useful\|pin\|unpin --expected-version N --idempotency-key key` | Append explicit feedback |
| `mem archive <id> ...` / `mem restore <id> ...` | Reversible recall lifecycle control |
| `mem forget <id> --expected-version N --idempotency-key key --reason user_request --yes` | Irreversibly redact a live memory payload |
| `mem context <query> --source all\|file\|memory` | Build a bounded evidence pack without generating an answer |
| `mem checkpoint --input <handoff.json\|-> --idempotency-key key` | Commit an immutable portable task checkpoint |
| `mem tasks [--scope /Projects/x]` | List resumable task summaries |
| `mem checkpoints <task_key>` | List bounded immutable checkpoint summaries for one task |
| `mem checkpoint get <task_key> <checkpoint_id>` | Get one immutable checkpoint and handoff payload |
| `mem resume <task_key>` | Restore the current task head and resolved/missing references |
| `mem workspace export --output <file.membundle>` | Export the complete current workspace without overwriting by default |
| `mem workspace import --input <file.membundle> --mode fresh --yes` | Restore a validated bundle into an empty target workspace |
| `mem get <file_id> -o <path>` | Download (use `-` for stdout) |
| `mem cat <file_id>` | Print text content to stdout (binary refused) |
| `mem info <file_id>` | Pretty metadata |
| `mem ls` / `mem ls /Photos` | List subfolders + files under root or a folder (folders prefixed `📁`) |
| `mem ls --tag x --type image --prefix /Photos` | Flat-file list with filters |
| `mem mkdir /Photos/2012` | Create folder (mkdir -p — auto-creates parents) |
| `mem mv <file_id> /Albums/2012` | Move a file to a different folder |
| `mem rename <file_id> new_name.jpg` | Rename a file (basename only) |
| `mem folder tree` | Print full folder tree with file counts |
| `mem folder rename <folder_id> <new_name>` | Rename a folder (cascades to descendants) |
| `mem folder move <folder_id> <new_parent_path>` | Move a folder (cascades to descendants) |
| `mem folder rm <folder_id> [--recursive]` | Delete a folder (must be empty unless `--recursive`) |
| `mem version` | Client + server version |

Legacy `mem login`, `mem logout` and `mem token ...` paths remain hidden
compatibility aliases and print a deprecation warning.

Global flags: `--format text|json`, `--server URL`, `--workspace UUID`.
Exit codes (SPEC §7.1): `0` ok · `2` not_found · `3` auth · `4` quota · `5` provider_error.

## Folders model

The folder layer is a strict materialization of SPEC §6.3 / §6bis (v0.3
"方案 B" decision):

- `folders` is a real table; empty folders are first-class.
- The root "/" is **implicit** — there is no row for it. Files in the root
  have `folder_id = NULL` and `path = "/"`.
- `files.folder_id` is the source of truth for parentage; `files.path` is a
  redundant cache of the parent folder's absolute path (kept in sync inside
  the same tx as folder rename / move).
- All mutating folder ops (`Create / Rename / Move / Delete`) run inside one
  PostgreSQL transaction. Rename / move use literal segment-boundary prefix
  rewrites for all descendants, including `memories.path`; valid `%` and `_`
  path characters are never interpreted as SQL wildcards.
- Active or archived memories block both direct and recursive folder deletion.
  Folder deletion never acts as implicit memory forget.
- Cycle prevention: `Move` refuses any destination that equals the source
  or sits under it (`pathx.IsDescendantOrSelf`).
- Path rules are centralised in `internal/pathx`:
  - absolute, leading `/`, no trailing `/` (except root)
  - segments may not be `""`, `"."`, `".."`, or contain `/` or `\x00`
  - case-sensitive
- `mem put --to /Photos/2012` auto-creates every missing ancestor folder
  (mkdir -p) before inserting the file row.

See SPEC §6.3 for the full consistency rule table.

## DB schema

See `internal/db/migrations/`. In addition to users, workspaces, files and
folders, migrations `0008_agent_memories.sql` and
`0010_memory_lifecycle.sql` plus privacy hardening in
`0012_memory_privacy_hardening.sql` add:

- `memories` — immutable Agent occurrences with source/producer provenance,
  workspace-local idempotency, lifecycle state and stable content hashes
- deterministic FTS and trigram indexes so remember → recall works without a
  Worker, embedding provider or answer model
- optimistic lifecycle projections plus append-only, retry-safe
  `memory_events`; forgotten rows retain only the minimum live tombstone

Core tables also include:
- `users`, `tokens`
- `folders` — `(id, user_id, parent_id, path, name, created_at, updated_at)`,
  UNIQUE `(user_id, path)`
- `files` (incl. all AI-Native columns from SPEC §6.1: `summary`, `caption`,
  `tags`, `timeline_at`, `geo`, `index_status`, plus `folder_id` FK)
- `entities`, `file_entities`, `file_relations`
- `embeddings_text(vector(768))`, `embeddings_visual(vector(512))`,
  `embeddings_face(vector(512))` — populated by the worker in W2+

Key indexes:
- `files (user_id, timeline_at)`
- `files (sha256)` and `UNIQUE (user_id, sha256)` — 秒传
- `files (user_id, folder_id)` — list folder contents
- `files (user_id, path text_pattern_ops)` — subtree `LIKE '/Photos/2012%'`
- `folders (user_id, parent_id)` — list direct children
- `folders (user_id, path text_pattern_ops)` — subtree prefix matches
- `folders UNIQUE (user_id, path)` — path uniqueness
- `tokens (hash)`

The `vector` extension is enabled at migration time.

## Tests

```bash
cd server && go test ./...
```

`internal/file` ships unit tests for SHA-256 streaming, storage key layout,
the dedup hashing contract, and target-path normalization on upload.
`internal/folder` covers mkdir-p ancestors, cycle detection, prefix rewrites,
path-name validation and memory-safe lifecycle behavior. `internal/memory`
covers normalization, idempotency, workspace/path isolation and PostgreSQL
recall. `internal/contextpack` covers source selection, budgets and explicit
partial-lane warnings. `internal/pathx` exhaustively tests path rule edges.

## Verify

```bash
cd server
go build ./...     # compiles all 3 binaries (memd, mem, mem-mcp)
go vet ./...
go test ./...
```

## Open questions / TODO

- [ ] **S3 cleanup on recursive folder delete** — `DELETE /v1/folders/{id}?recursive=true`
      removes DB rows but leaves the S3 objects behind. Needs a background GC
      pass that scans `users/<u>/<f>/...` keys without a backing file row.
- [ ] **`mem put -r --to /Albums` on Windows hosts** — path joining uses
      `filepath.ToSlash`; Windows-specific separators in `--name` are not
      currently sanitized beyond `pathx.ValidateName`. Probably fine since
      we explicitly forbid `/` in basenames, but needs a smoke test.
- [ ] Real OAuth / device-flow login — replaces dev `POST /v1/auth/login` (W4+).
- [ ] Quota enforcement — `tokens.quota` is stored but unread (W3).
- [ ] Chunked / resumable upload (`F1.3`) — W1 implements single-shot upload
      with a temp-file spill; multipart S3 upload is W2.
- [x] Folder-scoped tokens — file, memory, search, context and folder operations
      enforce `tokens.paths[]`; whole-workspace aggregate operations require an
      unrestricted token.
- [ ] `go.mod` go directive is `1.25.0` even though the spec asks for `1.22`,
      because transitive deps (`modernc.org/sqlite` via `pressly/goose/v3`) pin
      higher minimums. Builds + tests pass on Go 1.22+ toolchains via the
      `toolchain` mechanism. If strict 1.22 is required, replace goose's SQLite
      dialect with a slimmer migration runner.

## Contract with other agents

- **Worker (Python)**: writes to `embeddings_*`, `entities`, `file_entities`,
  and updates `files.summary / caption / tags / timeline_at / index_status`
  through the current gRPC processing contract.
- **Frontend (web/)**: consumes the v1 HTTP API table above. JSON shapes are
  whatever `internal/file.File` / `internal/auth.Token` serialize to.
