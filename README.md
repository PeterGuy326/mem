# mem

[![CI](https://github.com/fullstack-ai-infra/mem/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/fullstack-ai-infra/mem/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-experimental-orange.svg)](#project-status)

**A portable, self-hosted memory plane for AI agents.**

`mem` keeps files, metadata, and embeddings under your control and exposes the
same core through an HTTP API, a command-line client, an MCP server, and a web
interface.

> [!WARNING]
> `mem` is experimental. Interfaces, storage schemas, and release artifacts may
> change without notice. Do not use it as the only copy of important data.

## What is in this repository?

| Component | Purpose |
| --- | --- |
| `server/` | Go HTTP service (`memd`), CLI (`mem`), and stdio MCP server (`mem-mcp`) |
| `worker/` | Python gRPC worker for extraction, embeddings, and model providers |
| `web/` | React/Vite web interface |
| `docker-compose.yml` | Local PostgreSQL/pgvector, Redis, and MinIO dependencies |

The current implementation includes file and folder operations, token-based
access, search and retrieval surfaces, an extensible processing worker, and MCP
tools backed by the same service API. See [SPEC.md](SPEC.md) for the evolving
product and architecture contract.

## Developer quick start

Prerequisites:

- Go 1.25
- Python 3.11+ and [`uv`](https://docs.astral.sh/uv/)
- Node.js 22 and npm
- `protoc` 34.1, `protoc-gen-go` v1.36.11, and
  `protoc-gen-go-grpc` v1.6.2 when changing protobuf definitions
- Docker with Compose

Clone the repository, start the backing services, and build the Go binaries:

```bash
git clone https://github.com/fullstack-ai-infra/mem.git
cd mem

make up        # PostgreSQL, Redis, and MinIO only
make build     # bin/memd, bin/mem, and bin/mem-mcp
make server    # starts memd on :8787
```

Run the worker in another terminal:

```bash
cd worker
uv sync --extra test --extra dev
make proto
uv run python -m mem_worker.server
```

Run the web interface in a third terminal:

```bash
cd web
npm ci
npm run dev
```

The service requires a development user and token before protected operations
can be used. Follow [docs/RUN_LOCAL.md](docs/RUN_LOCAL.md) for the complete
local setup and smoke test. For agent integration, build `mem-mcp` and follow
the [MCP setup guide](docs/mcp.md).

## Verify a change

CI applies the following checks to pull requests. Run the applicable groups
locally before opening one; [CONTRIBUTING.md](CONTRIBUTING.md) explains the
review evidence expected when a check cannot be run.

Go integration tests require an explicitly disposable PostgreSQL database
whose name ends in `_test`. With the Docker Compose services running, this
creates `mem_test` if needed:

```bash
make up
docker compose exec -T postgres sh -c \
  "psql -U mem -d postgres -tAc \"SELECT 1 FROM pg_database WHERE datname='mem_test'\" | grep -q 1 || createdb -U mem mem_test"
```

Run the Go protobuf, formatting, vet, migration, race/coverage, and build
checks:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
PATH="$(go env GOPATH)/bin:${PATH}"
export PATH
make proto-go
git diff --exit-code -- server/internal/workerpb

cd server
test -z "$(gofmt -l .)"
go vet ./...
export MEM_TEST_DB=postgres://mem:mem@localhost:5432/mem_test?sslmode=disable
go run github.com/pressly/goose/v3/cmd/goose@v3.22.1 \
  -dir internal/db/migrations postgres "${MEM_TEST_DB}" up
go test -race -p 1 -coverpkg=./... -covermode=atomic \
  -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
cd ..
make build
```

Run the Worker protobuf, coverage, and package checks:

```bash
cd worker
uv sync --frozen --extra test --extra dev
make proto
git diff --exit-code -- mem_worker/proto
uv run pytest --cov=mem_worker --cov-report=term-missing \
  --cov-report=xml:coverage.xml
uv build
```

Run the Web checks:

```bash
cd ../web
npm ci
npm audit --omit=dev --audit-level=high  # advisory; findings do not gate CI
npm run lint
npm run typecheck
npm run build
```

Go and Python tests report coverage in CI. The web package currently uses
linting, type checking, and a production build as its required baseline.
Production dependency auditing is advisory so that a newly published external
advisory cannot block an unrelated pull request.

## Contributing

All changes follow an issue-first, pull-request-only workflow:

1. Open or select an issue and record its type, evidence, scope, and acceptance
   criteria; bugs also receive an impact severity.
2. Develop on a branch linked to that issue.
3. Add tests and verification evidence with the change.
4. Open a pull request that closes or references the issue.
5. Obtain an independent review and pass required CI checks before merge.

Read [CONTRIBUTING.md](CONTRIBUTING.md) for the development and review rules,
and [docs/maintainers/triage.md](docs/maintainers/triage.md) for the issue
taxonomy. Security reports must follow [SECURITY.md](SECURITY.md), not a public
issue. Release maintainers should use
[docs/maintainers/releasing.md](docs/maintainers/releasing.md).

## Project status

`mem` is in active experimental development. The repository is establishing a
stable contribution, test, and release baseline before committing to broad
distribution channels or compatibility guarantees.

## License

[Apache License 2.0](LICENSE) © mem contributors.
