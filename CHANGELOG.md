# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
The project is not yet publishing stable semantic-versioned releases.

## [Unreleased]

### Added

- Production deployment profiles: a secret-generated, loopback-only
  single-node Compose stack for Web, memd, Worker, PostgreSQL, Redis and MinIO;
  a multi-node Helm chart with horizontally scalable Web/Worker, single-replica
  non-overlapping memd rollouts, external state services, single-run migration,
  probes, disruption/topology controls, optional Web/Worker HPA and Ingress,
  NetworkPolicy and existing-Secret integration; model-free Worker images with
  ASR/CLIP/face extras opt-in; plus backup/restore, operations guidance and
  continuous Compose/Helm/production-image validation in CI.
- Complete Web Chinese/English localization with a persisted runtime selector,
  locale-aware metadata and display formatting, dictionary parity and
  hard-coded-prose auditing, and bilingual browser acceptance coverage.
- Server-owned, workspace-scoped AI profiles: an offline-first
  `local-fast-v2` fixed to Ollama Qwen3 Embedding 0.6B at 768 dimensions, and
  a SaaS-only `idealab-quality-v2` fixed to an explicitly bound Idealab
  `text-embedding-3-large` plus Qwen3.7 Max, with explicit text/PDF stage
  contracts at profile revision `2026-07-30.1` / pipeline revision
  `file-enrichment-v2`, profile-aware CLI/API selection, preflight probing,
  per-stage usage receipts, crash-recoverable transactional settlement, and no
  implicit model download or provider fallback.
- Authenticated hosted memd-to-Worker execution with deterministic request and
  response HMACs, shared Redis replay protection, exact-provider startup
  readiness, fetched-content SHA verification before managed egress, and
  private/BYOM-compatible deployment classification.
- Repeatable stdio MCP certification for OpenClaw, Hermes Agent, Claude Code,
  OpenCode, and Codex, with versioned config manifests, a model-free fake-memd
  lifecycle/failure contract, isolated real-host evidence, and explicit
  `REGISTERED`/`DISCOVERED`/`INVOKED`/`NOT RUN` grading.
- Payment-provider-neutral workspace entitlements for optional managed
  embeddings, with atomic quota reservation, safe idempotent replay,
  indeterminate-outcome reconciliation, a read-only status API, and Web
  plan/quota/error presentation.
- Managed search/context idempotency support across CLI, MCP, and Web while
  preserving subscription-free private and local/BYOM providers.
- A versioned, synthetic Chinese/English recall benchmark with a deterministic
  lexical reference, provider-agnostic ranking import, checked-in baseline,
  per-slice quality/latency metrics and a fail-closed forbidden-source gate.
- A versioned, vendor-neutral local embedding catalog and `mem model
  list|recommend|install|activate` flow with hardware/runtime checks, explicit
  selection, pinned Ollama artifact integrity, and separate activation.
- Provenance-aware asynchronous file enrichment: bounded phone/device capture
  time and location, sanitized EXIF/media observations, reviewable AI
  description/tag suggestions, API/Web/CLI/MCP accept/reject controls,
  portable decisions, and provenance-aware CLI/MCP upload adapters.
- Workspace bundle v2 enrichment provenance with strict stable-key/source
  projection validation and read compatibility for historical v1 archives.
- Versioned `mem.handoff` v1 checkpoints, optimistic head comparison,
  deterministic `resume`, and task/checkpoint list/get inspection across API,
  CLI and MCP.
- Portable workspace bundle v1 with manifest, seven typed indexes, immutable
  checkpoint payloads, content-addressed blobs, checksums and dependency
  validation.
- Resource-bounded workspace export and empty-target `fresh` import across
  API, typed client, CLI and Web, including idempotent import ledger,
  structured/truncated conflicts and failure compensation.
- Web Drive trust surfaces for Tasks, checkpoint/Resume, Memories lifecycle
  control and Workspace Transfer.
- Real-image visual regression coverage and an opt-in multilingual CLIP
  ranking gate with an explicit checked-in baseline report.
- Model-independent structured Agent memories with provenance, stable
  `mem://memories/<id>` citations and PostgreSQL lexical recall.
- Idempotent `POST /v1/memories`, scoped `GET /v1/memories/{id}`, CLI
  `mem remember` / `mem memory` and MCP `mem_remember` / `mem_memory_get`.
- Authorization-bound cursor pagination and a bounded-summary memory ledger.
- Auditable memory feedback (`useful`, `not_useful`, `pin`, `unpin`),
  optimistic archive/restore transitions and retry-safe forgetting across the
  API, CLI, MCP and Web control surfaces.
- Context Packs that can recall files, structured memories or both through
  `source=all|file|memory`, with kind filters and context-size budgets.
- Explicit partial-retrieval warnings when one Context Pack lane fails.
- Architecture decision record for immutable memory occurrences.
- `mem auth status` for verifying the current token and reporting workspace
  access.
- Reproducible, project-specific validation for Go, Worker, Web, PostgreSQL
  migrations/race paths and isolated process-level HTTP/CLI/MCP acceptance,
  backed by repository CI.
- Issue-first contribution, triage, review, security, and community governance
  standards.
- Pull request policy and CI jobs for Go, the Python worker, and the Web
  application.
- Go and Python coverage artifacts plus verified Go, Python, and Web build
  artifacts.
- Checked-in Go and Python protobuf stubs for reproducible fresh-clone builds.

### Changed

- Use the platform-native sans-serif stack consistently in development and
  production so the Web UI never depends on a third-party font request.
- Preserve the published `local-fast-v1` and `idealab-quality-v1` snapshots
  exactly for enabled persisted workspaces while hiding them from new
  selection; one SaaS process can authenticate and account for both exact
  managed generations during migration, V2 owns the revised text/PDF-only
  contract, and any populated V1→V2 switch now requires a versioned generation
  rebuild.
- Serialize managed result/outbox commits with stale reconciliation and period
  rollover, safely terminalize late file state, and scrub outbox file/content
  identity when the source file is deleted.
- Ollama text embeddings now use one modern batched `/api/embed` request with
  an explicit 768-dimensional contract and fail closed on batch or dimension
  mismatches.
- Checkpoint history lists now return bounded summaries; full handoff payloads
  and evidence references require an explicit checkpoint get or resume.
- Retired the built-in ask/chat path. mem now returns evidence while the
  calling Agent owns reasoning and answer generation.
- `mem_context` now defaults to `source=all`, so evidence may identify a
  structured memory instead of carrying `file_id`. Existing file-only clients
  should request `source=file`; other clients should branch on
  `source_kind/source_id`.
- Bound Agent tokens and retrieval to a workspace and canonical path scope.
- Track the actual embedding provider used by an index and fail closed for
  legacy vectors whose provider cannot be proven.
- Namespaced login, logout and token management under `mem auth`; legacy
  top-level paths remain hidden compatibility aliases with deprecation
  warnings.
- Inherit organization-wide contribution, issue, pull-request, conduct, and
  support defaults from `fullstack-ai-infra/.github`; keep only `mem`-specific
  development, security, triage, ownership, release, and validation rules in
  this repository.
- Align pull-request policy with the inherited controlled exceptions for
  trusted Dependabot updates and maintainer-labeled security advisories.
- Removed the repository-specific cloud-model credential and model-probing
  helpers. Core Agent memory remains model-independent; optional indexing
  providers stay behind the Worker contract.

### Security

- Fail closed in production on development state-service credentials,
  automatic per-replica migrations, open registration, wildcard CORS or an
  unauthenticated Worker.
- Make profile IDs the only client-selectable AI-routing input: reject model,
  endpoint, credential, and stale-profile injection; validate 768-dimensional
  embedding responses; reserve managed usage before a declared paid stage; and
  fail closed rather than silently falling back across a billing or data-egress
  boundary.
- Authorize account, workspace membership, scope, and path before entitlement
  lookup or provider invocation; persist only bounded identifiers, accounting
  state, timestamps, and hashes in the managed-embedding usage ledger.
- Fail closed for SaaS entitlement readiness and prevent provider fallback,
  duplicate charging, or raw upstream error leakage after timeout and
  indeterminate outcomes.
- Reject hidden-reasoning wrappers and nested JSON-like model values across
  Worker, server, database, and workspace-bundle boundaries; keep raw provider
  errors and malformed processor facts out of persistence, APIs, and bundles.
- Remove accepted model tags from the legacy effective projection before an
  enrichment downgrade, preventing a later re-up from reclassifying them as
  user-authored tags.
- Exclude credentials, tokens, provider secrets, runtime state and derived
  indexes from workspace bundles; carry only hashed memory idempotency keys.
- Bound transfer time, archive bytes, expanded metadata/records and concurrent
  operations; use `0700` spool directories and `0600` temporary files.
- Keep duplicate-content file entries independent after restore so deleting
  one object key cannot break another file.
- Derive workspace, actor and token provenance on the server instead of
  trusting client-supplied identity.
- Hide absent and out-of-scope memories behind the same not-found contract.
- Bound remember request/content/metadata fields and reject unknown JSON
  fields, malformed source hashes and idempotency-key conflicts.
- Prevent folder deletion from silently deleting active or archived memories.
- Re-authorize the persisted path on idempotent memory replay, preventing an
  old path and key from exposing a record after its folder is moved.
- Require compare-and-swap state versions and hashed idempotency keys for
  memory writes; forgotten records clear their payload, path, creator and
  request fingerprint, retain only a generic retry-safe tombstone, and never
  re-enter list or recall results.
- Escape terminal control/bidirectional sequences in human-readable memory
  output and reject them in new virtual paths.
- Return bounded feedback/lifecycle control projections so MCP mutations
  cannot echo full untrusted memory payloads into an Agent context.
- Prevent checkpoint-list pages from amplifying up to 200 complete handoff
  payloads and reference arrays into one Agent context.

### Fixed

- Keep rejected/superseded AI descriptions out of file detail, visual-search
  snippets, and workspace bundle projections; serialize same-file index runs
  and preserve the last usable text embedding when a partial retry produces no
  replacement.
- Preserve uploaded workspace objects after an indeterminate database commit
  and expose a stable `503` recovery contract requiring the exact same bundle.
- Serialized folder prefix mutations with folder, file, memory and checkpoint
  writers so concurrent renames cannot split a subtree or leave a file path
  pointing at a differently named folder.
- Made `same_person` ranking use directional source-person coverage and stable
  file-ID tie-breaking so a full person-set match ranks ahead of a partial
  match and equal candidates are selected deterministically.
- Align the Worker protobuf/gRPC runtime floors with the checked-in generated
  stubs and verify that contract in regression tests.
- Generate Go protobuf stubs directly into their destination instead of
  deleting a repository-root `github.com/` directory after generation.
- Preserve the primary Web acceptance failure when browser or Vite cleanup
  also fails.

[Unreleased]: https://github.com/fullstack-ai-infra/mem/commits/main
