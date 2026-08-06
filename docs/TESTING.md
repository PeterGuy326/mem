# Testing and validating mem

This is the reproducible test contract for `mem`. It separates hermetic
regression, PostgreSQL integration, process-level service acceptance and
model-quality evaluation so a skipped external dependency cannot be mistaken
for a pass.

## 1. Supported validation environment

Checked-in CI fixes the versions below. Local validation must use the listed
major versions or compatible patch releases:

| Component | Required baseline |
| --- | --- |
| Go | 1.25.x (`server/go.mod`) |
| Node.js / npm | Node 24.x with the checked-in `package-lock.json` |
| Python | 3.11 or 3.12 |
| Python package manager | `uv`, with the checked-in `worker/uv.lock` |
| Browser | Playwright Chromium or an installed Google Chrome |
| Database integration | Docker Compose v2 and `pgvector/pgvector:pg16` |
| Process acceptance | `curl`, `jq`, Docker Compose v2 |

The default regression suite does not need Redis, MinIO, Ollama, a cloud
provider or any API key. The Web acceptance tests use MSW fixtures. Worker
tests use fakes except for the explicitly opt-in visual-model evaluation.

The two checked-in workflows have distinct responsibilities. `ci.yml` owns
component build, lint, unit/race, generated-protobuf, coverage, release
artifact checks, and production Compose/Helm/image validation.
`memory-validation.yml` owns repository metadata, deployment-script lint,
browser acceptance, owned-database migrations, and process-level HTTP/CLI/MCP
acceptance. Their shared toolchain and service versions must stay aligned.
Database tests intentionally overlap where artifact production and the
Agent-memory contract need independent evidence.

## 2. Bootstrap a clean checkout

From the repository root:

```bash
make bootstrap
```

This runs the following reproducible setup:

```bash
(cd server && go mod download)
(cd worker && uv sync --locked --extra test && make proto)
(cd web && npm ci && npx playwright install chromium)
```

On a clean Linux host, install Playwright's system packages as well:

```bash
(cd web && npx playwright install --with-deps chromium)
```

Expected result: every command exits `0`; the checked-in Worker protobuf stubs
are regenerated without a diff; Web dependencies and one Playwright browser
are installed. No service is started and no user data is created.

## 3. Hermetic regression

Run all DB-free checks:

```bash
make test
make test-race
```

`make test` executes:

- `go build ./...`, `go vet ./...` and uncached `go test ./...`;
- Worker `pytest` through the locked `uv` environment;
- Web typecheck, ESLint, production build;
- the Localization, Theme, File Enrichment, Memory, and Workspace Transfer browser acceptance suites.

`make test-race` executes the high-risk Go file/folder path-locking, memory,
handoff, transfer, API, client, tool, CLI, and MCP packages with the race
detector.

Expected result: both commands exit `0`; Go packages report `ok`; Worker tests
pass with only the real-model evaluation skipped; Web acceptance prints its
success summary; no `WARNING: DATA RACE` appears.

Important: the Go suite intentionally unsets `MEM_TEST_DB`. PostgreSQL tests
therefore do not run in this phase and must not be claimed as covered by
`make test`.

Individual component entry points are:

```bash
make test-server
make test-worker
make test-web
```

## 3.1 Production deployment assets

Validate the single-node Compose graph and the default, production and
Worker-disabled Helm renders:

```bash
make test-deploy
```

This fails when a stateful/internal service publishes a host port, the Compose
backend is not isolated, a production deployment file uses a mutable `latest`
tag, the Helm schema accepts `latest`, the Helm chart fails
schema/lint/template validation, disabling Worker still renders Worker
resources, or the production render omits its deployment, migration,
disruption, network, ingress or autoscaling resources. It also enforces a
single-replica, non-overlapping memd rollout and verifies that the production
example does not enable a memd HPA. When a local `helm` binary is unavailable,
the script uses the pinned `alpine/helm:3.17.1` container.

Build all three first-party production images as an additional clean-checkout
gate:

```bash
make test-deploy-build
```

Expected result: the images build successfully and the final line is
`PASS: production Compose and Helm deployment validation`. These checks render
configuration only; they do not create a Kubernetes cluster or mutate a
production environment. The `Deployment profiles` CI job runs
`make test-deploy-build` on every pull request and `main` push so Compose,
Helm, and all three production Dockerfiles remain continuously enforced.

## 4. Disposable PostgreSQL environment

Start the isolated test database:

```bash
make test-env-up
```

It listens only on `127.0.0.1:55432` by default and creates
`mem_integration_test` as a control database. It does not reuse the development
database or volumes. The Makefile derives a stable, checkout-specific Compose
project name so another clone's test environment is not stopped by cleanup.
Override the host port or project only when necessary, and use the same values
for `test-env-up` and `test-env-down`:

```bash
MEM_TEST_PROJECT=mem-test-review-42 MEM_TEST_PG_PORT=55433 make test-env-up
```

Run migration and database-backed regression:

```bash
make test-integration
make test-integration-race
```

When overriding the port, pass the matching URL:

```bash
MEM_TEST_DB='postgres://mem:mem@127.0.0.1:55433/mem_integration_test?sslmode=disable' \
  make test-integration
```

Before any write, the runner parses the effective pgx configuration, connects
and checks `current_database()`. A URL path cannot disguise a different
`dbname`. The control database is never downgraded. For each phase, the runner
creates a randomly named `mem_verify_*_test` database with a 256-bit ownership
nonce recorded both in the returned DSN and a protected marker table. Cleanup
connects to the target and verifies the exact database name, server role and
marker before it can drop anything. The control role therefore needs
`CREATEDB`; the checked-in Compose user already has it. The runner then:

1. validates migrations on a fresh owned database and applies `0001` through
   the declared head;
2. asserts the current privacy, enrichment, managed-entitlement and workspace
   AI-profile schema; proves the `15 → 18` migration path canonicalizes
   bounded model text and adds workspace AI profiles plus the managed-stage
   settlement outbox; explicitly proves the
   `16 → 17 → 20 → 17 → 20 → 16 → 20` table boundaries, including the
   additive index-generation schema and the additive durable-context grants
   table, then performs the broader `20 → 15 → 20`
   and `20 → 11 → 20` rollback round trips; asserts
   every intermediate schema state; and proves accepted model tags are
   removed before provenance disappears rather than being copied into
   `user_tags` on re-up;
3. creates separate fresh databases for normal and race runs, then executes
   the real PostgreSQL memory, handoff, workspace-transfer, HTTP-router,
   folder/file path-locking, folder-lifecycle, relator, managed-entitlement,
   replay-authorization, HTTP-ordering and durable-context scope tests
   serially; and
4. fails if any required integration test is skipped.

Required tests:

- `TestMemoryPostgres`
- `TestHandoffPostgres`
- `TestWorkspaceTransferPostgres`
- `TestHandoffCrossAgentHTTPIntegration`
- `TestMemoryPathLifecycleIntegration`
- `TestWorkspacePathLockingIntegration`
- `TestFilePathLockingIntegration`
- `TestAnnotationDecisionIntegration`
- `TestIndexerEnrichmentIntegration`
- `TestRecomputePerson`
- `TestManagedEmbeddingEntitlementPostgres`
- `TestManagedSearchReplayPostgres`
- `TestManagedEmbeddingHTTPAuthorizationPostgres`
- `TestAIProfilePostgres`
- `TestIndexGenerationPostgres`
- `TestManagedAISettlementOutboxPostgres`
- `TestReleasedFileStageRetryPostgres`
- `TestDurableContextPostgres`

Expected result: migrations reach the declared current head (currently `20`),
all rollback-state assertions pass, every named test prints `PASS`, all commands
exit `0`, and the race run reports no data race.

Cleanup is explicit and destructive only to the isolated Compose project:

```bash
make test-env-down
```

This removes only the derived (or explicitly supplied) test project,
containers, network and tmpfs-backed database state. It does not touch the
normal development Compose project, another checkout's test project or
`.dev/` data.

## 5. Process-level API, CLI and MCP acceptance

Run the isolated, model-free process test:

```bash
make test-acceptance
```

The script owns a per-run Compose project, random loopback PostgreSQL/MinIO
ports, an ephemeral database/store and a new memd process on an atomically
locked, twice-preflighted loopback port. It builds the current CLI, MCP adapter
and a standard-library MCP test client. The scenario creates real data outside
a path-scoped Agent token and proves that get/list/context cannot reveal it,
then proves HTTP remember/replay/conflict/model-free context. The current CLI
and MCP adapter both read full memory detail with citation/provenance, discover
tasks, list bounded checkpoint summaries and fetch one complete checkpoint
through the same real memd/PostgreSQL service. The MCP client waits for the
`initialize` response before sending `notifications/initialized`, then drives
real `tools/call → HTTP → PostgreSQL` remember, checkpoint inspection,
feedback, archive, restore and logical forget. Archive must remove the memory
from recall, restore must make it recallable again, and forget must redact the
live PostgreSQL projection and event actors before the database is destroyed.
The script never reads or writes the user's CLI configuration. Set
`MEM_ACCEPTANCE_HTTP_PORT` only when an explicit fixed port is required.

Expected result: the final line is
`PASS: isolated HTTP, CLI and MCP Agent-memory lifecycle acceptance` and the
command exits `0`. A trap stops only the spawned memd, removes only the
per-run Compose project and deletes its validated repository-local
`.dev/acceptance.*` directory. A cleanup failure changes a successful test to
a failure.

Checkpoint/resume is covered by a real Router + PostgreSQL integration test.
Workspace transfer has PostgreSQL service integration plus independent
handler/client contract tests; it is not represented as a single
Router-to-PostgreSQL end-to-end test. A two-deployment bundle round trip
remains release-level manual acceptance. Agent-host evidence is graded
separately in the next section.

## 6. Agent-host MCP certification

The host-neutral gate needs only Python's standard library and an explicit
current `mem-mcp` binary:

```bash
MEM_MCP_CERT_BINARY='/absolute/path/to/mem-mcp' \
  make test-agent-certification
```

It parses all five host manifests/config fixtures and drives a fake
loopback-only memd through the real adapter handshake and safe tool calls. It
also injects missing/invalid token, insufficient role, unavailable server,
unknown tool, timeout, malformed response, partial context, stdout pollution,
foreign response ID, and path-with-spaces cases. Timeout cleanup kills the
whole POSIX process group and is regression-tested. No Agent answer model,
desktop host, cloud service, API key, database, or global host config is
required.

CI builds the adapter into a runner-temporary path and sets
`MEM_MCP_CERT_BINARY`; therefore the conditional current-adapter test must not
skip there. On a managed macOS computer, do not execute a temporary Go binary:
run this gate in the same local Linux/Docker boundary used by the safe
acceptance workflow.

Installed-host probes are opt-in:

```bash
report="$(mktemp)"
python3 scripts/agent_certification/certify.py real-hosts \
  --mcp-binary /absolute/path/to/mem-mcp >"$report" &&
  python3 -m json.tool "$report" >/dev/null &&
  mv "$report" docs/integrations/agent-host-certification.json
```

The runner configures documented host-specific roots, checks temporary config
files for token material, retains bounded command output, sanitizes its report,
and grades registration/discovery/invocation separately. Before returning, it
validates the same canonical schema used for the checked file and recomputes
each status/result solely from the complete sanitized command evidence. The
checked JSON is therefore the command's verbatim output, not a manually
transcribed summary. `VERIFIED` isolation means those host-specific roots were
configured and generated temporary files were checked; it is not a
syscall/filesystem-audit claim that a third-party binary never attempted
another read. Codex and absent Hermes remain isolation `NOT VERIFIED` and
runtime `NOT RUN`. See
[Agent host certification](integrations/agent-hosts.md) and its
[machine-readable report](integrations/agent-host-certification.json).

## 7. Visual-search quality gate

The default Worker regression proves that original image bytes reach the
visual provider and that only 512-dimensional vectors enter the current
schema. It does not prove multilingual ranking quality.

The real-model gate is opt-in:

```bash
cd worker
uv sync --locked --extra test --extra clip
MEM_RUN_VISUAL_MODEL_EVAL=1 \
MEM_VISUAL_EVAL_PROVIDER='clip:xlm-roberta-base-ViT-B-32:laion5b_s13b_b90k' \
  uv run pytest -q tests/test_multilingual_visual_acceptance.py
```

All English and Chinese ranking assertions must pass before that provider can
be described as validated. Model download failure, timeout or an unavailable
checkpoint is `NOT VERIFIED`, not a pass. See
[VISUAL_SEARCH_BASELINE.md](acceptance/VISUAL_SEARCH_BASELINE.md).

## 8. Multilingual recall benchmark

The repository includes a standalone Python 3.11+ benchmark for structured
memory, text-file and image-caption retrieval. Its checked-in corpus is
hand-authored synthetic Chinese/English data under CC0-1.0. It does not read a
database, contact a provider, download a model, require a GPU or use a secret.

Run its unit, determinism, baseline-comparison and forbidden-source checks:

```bash
make test-recall
```

Record a machine-readable lexical reference artifact and an informational
comparison:

```bash
python3 -m benchmarks.recall run \
  --output /tmp/mem-recall.json \
  --compare benchmarks/recall/baselines/lexical-reference.v1.json \
  --comparison-output /tmp/mem-recall-comparison.json
```

The output is prominently labeled `engine=lexical-reference` and
`not production recall`. Its zero latency is a deterministic sentinel, not a
performance claim. Opt-in vector or hybrid systems export rankings with an
explicit provider, model, dimension, index/search configuration and coarse
hardware summary; they are never invoked by the default suite. Any
cross-workspace, path-filter or unknown-document result makes the run exit
non-zero. Metric deltas are informational: this issue establishes no
model-quality threshold. See
[`benchmarks/recall/README.md`](../benchmarks/recall/README.md) for the input
contract and exact denominators.

For the opt-in, profile-specific comparison of local and managed text
embeddings, use [AI profile evaluation](AI_PROFILE_EVALUATION.md). It supplies
a separate file-only synthetic fixture, requires isolated workspaces/index
generations, and explains why its external-ranking adapter is not a cloud-model
CI gate.

## 9. Regression ledger

Use this table in pull requests and add implementation-specific scenarios:

| ID | Observable acceptance criterion | Command | Expected result |
| --- | --- | --- | --- |
| V1 | Server, CLI and MCP build and preserve unit contracts | `make test-server` | Exit `0`; no skipped DB claim |
| V2 | Worker processing regressions remain hermetic | `make test-worker` | Exit `0`; real-model gate explicitly skipped |
| V3 | Localization, theme, enrichment, memory, transfer and managed-embedding control surfaces work in a browser | `make test-web` | Typecheck/lint/build, the localization audit, all browser acceptance suites and managed status mapping pass |
| V4 | High-risk Go paths are race-free | `make test-race` | Exit `0`; no data-race warning |
| V5 | Fresh schema, rollback and PostgreSQL semantics hold | `make test-integration` | Migration head and seventeen named tests pass, none skipped |
| V6 | DB concurrency paths are race-free | `make test-integration-race` | The same seventeen tests pass under `-race` |
| V7 | Real service boundaries agree | `make test-acceptance` | HTTP, CLI and MCP share one isolated service; memory citation/provenance, bounded checkpoint listing, full checkpoint get, lifecycle and forget redaction pass |
| V8 | Five config shapes and the real adapter preserve the host-neutral MCP contract | `MEM_MCP_CERT_BINARY=... make test-agent-certification` | All fixtures and current-adapter scenarios pass with no skip |
| V9 | Multilingual visual quality meets the chosen checkpoint | Opt-in command in section 7 | All fixed ranking assertions pass |
| V10 | Offline recall math, determinism and source boundaries hold | `make test-recall` | Unit checks pass; two lexical artifacts differ only by timestamp; malicious fixture is rejected |
| V11 | CLI and MCP file-annotation review delegate to the canonical typed HTTP client | `(cd server && go test ./internal/apiclient ./internal/tools/builtin ./cmd/mem)` | Accept/reject request shapes, auth/workspace forwarding, local validation and conflict propagation pass |

## 10. Known limitations

- GitHub CI covers hermetic checks, owned fresh-PostgreSQL
  migration/integration and process-level lifecycle acceptance. It does not
  run external cloud models.
- The two-deployment bundle round trip remains release-level manual
  acceptance. Installed Agent hosts are opt-in evidence: current results are
  explicit `NOT RUN`, not inferred from the hermetic fixture harness.
- POSIX host-runner process-group cleanup is automated. Windows process-tree
  cleanup is `NOT VERIFIED`.
- `web/acceptance.mjs` and `web/e2e-smoke.mjs` are legacy, environment-specific
  manual scripts. The standard portable Web gates are
  `npm run test:i18n`, `npm run test:theme`, `npm run test:enrichment`, `npm run test:memory`, and
  `npm run test:transfer`.
- Migration downgrade proves DDL behavior on disposable data. It is not a
  promise that privacy-redacted payloads or discarded identifiers can be
  reconstructed during a production rollback.
- Forget acceptance verifies the live PostgreSQL projection and retained
  minimal event receipts. Physical backup, snapshot and WAL retention remain a
  deployment policy and are not claimed as erased by this test.
- A historical `v11 → v12` fixture containing pre-migration live and forgotten
  rows is not automated yet. The fresh-database round trip does not prove that
  historical-data conversion, so it must be reported as `NOT VERIFIED`.
- Web builds and test caches remain in ignored repository paths. Browser tests
  print their retained screenshot directories under the operating-system temp
  directory; delete only those printed directories after review.
