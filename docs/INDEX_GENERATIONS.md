# Versioned index generations

Index generations make a model or pipeline migration a separate, reversible
build instead of an in-place rewrite of the searchable corpus. They are a
control-plane contract: changing a default environment variable never starts
a rebuild and file count never selects a paid or higher-quality profile.

## Identity and isolation

Each route generation fixes all fields that define one comparable vector
space:

- workspace;
- route (`text` or `visual`);
- provider and pinned model revision;
- exact output dimension;
- pipeline revision; and
- profile ID and profile revision.

A build groups the route generations for one profile migration and persists
the complete credential-free profile snapshot: every stage's enabled state,
provider and dimension, the egress policy, MIME allowlist and both revisions.
Activation reads that immutable snapshot instead of re-resolving the current
compiled catalog revision. The operator profile allowlist remains a separate,
fail-closed kill switch. Grouping is
important: a profile's text and visual routes must be made current by one
transaction, not switched independently. Target rows also capture file content
SHA-256 plus generation and stage, so retries cannot silently apply a result to
different bytes.

Create takes the workspace's exclusive corpus-snapshot lock after content
writers take their canonical lock. The resulting target rows are the exact
membership contract: a file committed afterward is intentionally picked up by
a later build. Deleting a member leaves a `source_present=false` skipped
tombstone and clears its leased attempt, so progress counts never drift from
`required_targets` and a stale Worker cannot restore deleted content.
Lifecycle mutations use the canonical workspace (when applicable), file,
build, then target lock order; the delete trigger locks all affected builds in
UUID order before it touches any target.

The generation vector table uses pgvector's unconstrained `vector` storage
type. The canonical service rejects any vector whose length differs from its
generation identity. This supports the released 768-dimensional text and
512-dimensional visual spaces and does not preclude a later independently
versioned 1024-dimensional text space. Padding, truncation and cross-generation
comparison are forbidden.

There is deliberately no generic ANN index in this foundation. A follow-up
query-routing change must use the active generation ID and add
route/dimension-specific expression or partition indexes based on measured
query plans.

## Lifecycle

The persisted lifecycle is:

```text
building ──cancel──> cancelled ──resume──> building
    │                                      │
    ├──terminal target failure──> failed ──┘
    │
    └──all targets succeed/skip──> ready ──activate──> active
                                                       │
                                          replacement ─┴─> inactive
                                                            │
                                                    rollback│or discard
```

Creating the same non-terminal profile/revision/pipeline build is idempotent;
after it leaves that set, the same profile may create a new build for a later
corpus. Workers claim target rows with `FOR UPDATE SKIP LOCKED` and a unique,
expiring attempt token. Completion must present that exact live token, closing
the lease-expiry/resume ABA window. A cancelled or failed build cannot accept a
late result. Resume retains successful vectors and puts failed or interrupted
targets back into the pending set with their old leases revoked.

The default quality gate is `all_targets`: every required target must be
`succeeded` or explicitly `skipped`, no target may be failed, and every
succeeded target must have a vector with the generation's exact dimension.
Only then can the internal activation transaction:

1. lock the workspace/profile coordination row;
2. revalidate the complete gate and materialized vectors;
3. mark the previous active build and its routes inactive;
4. mark the replacement build and routes active; and
5. update the immutable workspace profile snapshot.

Any error rolls the entire transaction back, leaving the previous active build
unchanged. Rollback uses the same transaction in the opposite direction.

Discard is logical and explicit. It is allowed only for cancelled, failed or
inactive builds, records a seven-day retention deadline, keeps vectors in
place, and never deletes the active corpus or file-enrichment review
provenance. A discarded, previously complete build remains a valid rollback
target until that deadline. Physical cleanup after the retention deadline is
performed by the internal `CleanupExpired` lifecycle operation; child route,
target, vector and event rows are removed by database cascades. Activation
lineage is event-only, so no build-to-build pointer can form a cycle or block
cleanup.

Every lifecycle mutation appends a database-bounded audit event. Public reads
return at most the latest 100 events and expose only whether an actor was
present, not the actor UUID. Events have no field for credentials, source
content or raw provider responses.

## Current public surface

The HTTP and CLI surfaces are intentionally read-only in this foundation:

```text
GET /v1/workspaces/current/index-generations
GET /v1/workspaces/current/index-generations/{build-id}
GET /v1/workspaces/current/index-generations/{build-id}/events

mem generation list
mem generation status <build-id>
mem generation events <build-id>
```

They expose `execution_wired=false`. The server does not expose create,
activate, rollback, discard, cancel or resume yet. Publishing those mutations
before the Worker and search paths consume the same generation identity would
create a false state where metadata says “active” while queries still use the
released legacy embedding tables.

## Cost, time and benchmark gate

The target count is observable before execution, but it is not a provider-cost
quote. Actual time and cost depend on bytes, MIME processors, provider latency,
hardware, rate limits, retries and managed entitlements. An operator must make
the profile choice explicitly; no file-count threshold may perform that choice.

Before a production quality profile can be activated, the opt-in benchmark
from `docs/AI_PROFILE_EVALUATION.md` must record provider, pinned model
revision, dimension, pipeline revision, corpus version, hardware, p50/p95,
error rate and retrieval/enrichment metrics. The default model-free test suite
does not manufacture this evidence.

## Deliberately unfinished acceptance

Issue #55 remains open after this foundation. Separate reviewable changes must
still provide:

- durable queue orchestration that invokes the Worker for claimed generation
  targets, survives restart and uses the existing managed-usage boundary;
- generation-aware Worker result persistence for enrichment and review
  provenance;
- active-generation query embedding and ANN routing, including measured
  route/dimension-specific indexes;
- public create/cancel/resume/activate/rollback/discard commands after those
  execution and query paths share the same identity;
- safe generation identity in portable workspace export metadata;
- retained opt-in benchmark artifacts for a production quality profile.

Until those changes land, the released `embeddings_text vector(768)` and
`embeddings_visual vector(512)` tables remain the only searchable corpus and
are not copied, altered or deleted by migration 0019.
