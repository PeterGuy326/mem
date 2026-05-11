# mem · server (Go backend)

> W1 deliverable — Go HTTP API + PostgreSQL schema + `mem put / get / cat` + Token auth.
> See `/SPEC.md` for the full project spec.

## Layout

```
server/
├── cmd/
│   ├── memd/          — HTTP daemon entrypoint (default :8080)
│   ├── mem/           — User-facing CLI (cobra)
│   └── mem-mcp/       — MCP server stub (W4 scope)
├── internal/
│   ├── api/           — chi HTTP router + handlers + middleware
│   ├── auth/          — User + Token (bcrypt + SHA-256 hash), scope checks
│   ├── config/        — env-var driven config loader (MEM_*)
│   ├── db/            — pgx pool + goose embedded migrations
│   ├── db/migrations/ — SQL migrations (`0001_init.sql`)
│   ├── file/          — Ingestion (SHA-256, 秒传 dedup), retrieval, listing
│   ├── storage/       — S3-compatible blob store (minio-go)
│   └── workerpb/      — Stub for gRPC client to Python worker (W2+)
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
# logs: "db connected" -> "db migrations applied" -> "http listening :8080"
```

Service URLs in the docker-compose dev stack:
- Postgres: `localhost:5432` (user/pass: `mem` / `mem`, db `mem`)
- Redis: `localhost:6379`
- MinIO API: `http://localhost:9000` (`mem` / `mem-minio-password`)
- MinIO console: `http://localhost:9001`

## Environment variables

| Var | Default | Description |
|---|---|---|
| `MEM_HTTP_ADDR` | `:8080` | HTTP listen address |
| `MEM_DB_URL` | `postgres://mem:mem@localhost:5432/mem?sslmode=disable` | PostgreSQL DSN (pgvector required) |
| `MEM_REDIS_URL` | `redis://localhost:6379` | Redis URL (used in W2+) |
| `MEM_S3_ENDPOINT` | `http://localhost:9000` | S3-compatible endpoint |
| `MEM_S3_BUCKET` | `mem` | Bucket (auto-created on startup) |
| `MEM_S3_ACCESS_KEY` | `mem` | Access key |
| `MEM_S3_SECRET_KEY` | `mem-minio-password` | Secret key |
| `MEM_S3_REGION` | `us-east-1` | Region tag |
| `MEM_S3_USE_SSL` | derived from endpoint scheme | force TLS |
| `MEM_WORKER_GRPC` | (empty) | Worker dial target (W2+) |
| `MEM_SESSION_TTL` | `24h` | Login session token TTL |
| `MEM_LOG_LEVEL` | `info` | `debug|info|warn|error` |

## Bootstrap a dev user

memd does not ship a public sign-up endpoint in W1. Insert a user with bcrypt
once, then use `mem login`:

```bash
# Generate a bcrypt hash (any tool works; here using Python for convenience)
python3 -c "import bcrypt;print(bcrypt.hashpw(b'devpassword', bcrypt.gensalt()).decode())"
# -> $2b$12$...

# Insert
psql 'postgres://mem:mem@localhost:5432/mem' -c \
  "INSERT INTO users (email, password_hash) VALUES ('dev@local','<paste-hash>');"

# Login from CLI (writes ~/.mem/config.yaml)
go run ./cmd/mem login   # prompts for email + password
```

## HTTP API (v1)

All token-protected routes require `Authorization: Bearer <token>`.

| Method | Path | Scope | Description |
|---|---|---|---|
| `GET`    | `/healthz`                  | — | liveness |
| `GET`    | `/v1/version`               | — | server version |
| `POST`   | `/v1/auth/login`            | — | email+password → admin-scoped session token |
| `POST`   | `/v1/auth/tokens`           | admin | create token (plaintext returned once) |
| `GET`    | `/v1/auth/tokens`           | admin | list tokens (no secrets) |
| `DELETE` | `/v1/auth/tokens/{id}`      | admin | revoke a token |
| `POST`   | `/v1/files`                 | write | upload (multipart `file=`, or `?stream=1&name=...`) |
| `GET`    | `/v1/files`                 | read  | list (`?tag=&type=&since=&until=&limit=&page=`) |
| `GET`    | `/v1/files/{id}`            | read  | metadata + AI fields |
| `GET`    | `/v1/files/{id}/content`    | read  | stream raw bytes |

Error envelope (SPEC §8.2): `{"error": "<code>", "hint": "<actionable hint>"}`.

## CLI commands

| Command | Description |
|---|---|
| `mem login` | Prompt email + password, save token to `~/.mem/config.yaml` |
| `mem logout` | Clear local config token |
| `mem token create --name X --scope read,write` | Create token (one-time plaintext printed) |
| `mem token list` | List tokens |
| `mem token revoke <id>` | Revoke |
| `mem put <path>` | Upload a single file (auto MIME) |
| `mem put <dir> --recursive` | Upload every file under dir |
| `mem put - --name foo.txt` | Upload from stdin |
| `mem put <path> --tag x --tag y` | With tags |
| `mem get <file_id> -o <path>` | Download (use `-` for stdout) |
| `mem cat <file_id>` | Print text content to stdout (binary refused) |
| `mem info <file_id>` | Pretty metadata |
| `mem ls [--tag x] [--type image]` | List files |
| `mem version` | Client + server version |

Global flags: `--format text|json`, `--server URL`.
Exit codes (SPEC §7.1): `0` ok · `2` not_found · `3` auth · `4` quota · `5` provider_error.

## DB schema (W1)

See `internal/db/migrations/0001_init.sql`. Tables:
- `users`, `tokens`
- `files` (incl. all AI-Native columns from SPEC §6.1: `summary`, `caption`,
  `tags`, `timeline_at`, `geo`, `index_status`)
- `entities`, `file_entities`, `file_relations`
- `embeddings_text(vector(768))`, `embeddings_visual(vector(512))`,
  `embeddings_face(vector(512))` — populated by the worker in W2+

Key indexes:
- `files (user_id, timeline_at)`
- `files (sha256)` and `UNIQUE (user_id, sha256)` — 秒传
- `tokens (hash)`

The `vector` extension is enabled at migration time.

## Tests

```bash
cd server && go test ./...
```

`internal/file` ships unit tests for SHA-256 streaming, storage key layout,
and the dedup hashing contract. End-to-end tests against a real Postgres land
in W2 (testcontainers).

## Verify

```bash
cd server
go build ./...     # compiles all 3 binaries (memd, mem, mem-mcp)
go vet ./...
go test ./...
```

## Open questions / TODO (W1)

- [ ] Real OAuth / device-flow login — replaces dev `POST /v1/auth/login` (W4+).
- [ ] Worker gRPC integration — `internal/workerpb/` is a placeholder; needs
      the worker team's `.proto` (W2).
- [ ] Async post-upload hook — currently files land with `index_status='pending'`
      and nothing pulls them. W2 will add an Asynq enqueue here.
- [ ] Quota enforcement — `tokens.quota` is stored but unread (W3).
- [ ] Chunked / resumable upload (`F1.3`) — W1 implements single-shot upload
      with a temp-file spill; multipart S3 upload is W2.
- [ ] `go.mod` go directive is `1.25.0` even though the spec asks for `1.22`,
      because transitive deps (`modernc.org/sqlite` via `pressly/goose/v3`) pin
      higher minimums. Builds + tests pass on Go 1.22+ toolchains via the
      `toolchain` mechanism. If strict 1.22 is required, replace goose's SQLite
      dialect with a slimmer migration runner.

## Contract with other agents

- **Worker (Python)**: writes to `embeddings_*`, `entities`, `file_entities`,
  and updates `files.summary / caption / tags / timeline_at / index_status`.
  See `internal/workerpb/README.md` for the gRPC integration plan.
- **Frontend (web/)**: consumes the v1 HTTP API table above. JSON shapes are
  whatever `internal/file.File` / `internal/auth.Token` serialize to.
