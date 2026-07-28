# ADR 0002: Task handoffs are immutable versioned checkpoints

- Status: Accepted
- Date: 2026-07-28

## Context

mem needs to let one Agent stop work and another Agent resume the same task
without access to the producer's private conversation history. The existing
`memories` table safely persists immutable observations and free text, but its
`task_state` kind does not define:

- a stable cross-Agent task identity;
- a validated, portable state shape;
- an ordered checkpoint lineage;
- an optimistic-concurrency precondition;
- or an enumerable dependency set for restore and missing-object reporting.

Storing another arbitrary object under `memories.attributes` would make a demo
possible, but it would not establish a contract that can be evolved, imported,
or rejected safely.

The authoritative public v1 shape is
[`docs/schemas/handoff.v1.schema.json`](../schemas/handoff.v1.schema.json).

## Decision

### Separate task, checkpoint, and reference records

Migration `0009_task_checkpoints.sql` introduces:

- `agent_tasks`: workspace-scoped stable `task_key`, authorization
  `scope_path`, and the current checkpoint head;
- `task_checkpoints`: immutable handoff payloads, schema version, lineage,
  authenticated provenance, request/payload hashes, and idempotency evidence;
- `task_checkpoint_refs`: the ordered references declared by decisions, next
  steps, blockers, and artifacts.

The normalized reference table is deliberately not a second copy of referenced
files or memories. It is an immutable dependency manifest. API code may resolve
each URI under the caller's current workspace and path authorization and report
it as available, unavailable, or integrity-mismatched.

### Version meanings remain independent

Three numbers have different meanings and must not be conflated:

1. `/v1` is the HTTP API major version.
2. `schema_version: 1` identifies the `mem.handoff` data contract.
3. `sequence` is a server-assigned revision number inside one task.

The v1 decoder rejects unknown JSON fields, missing required arrays, invalid
bounds, and any schema version other than `1`. A future incompatible payload
gets a new validator and reader. It must never be silently interpreted as v1 or
downgraded into free text.

### Checkpoint writes are idempotent compare-and-swap transactions

`Service.Checkpoint` applies these rules in one PostgreSQL transaction:

1. Normalize and hash the complete semantic request, excluding the
   authenticated user/Token and idempotency key.
2. Replay an existing `(workspace_id, idempotency_key)` record when its request
   hash is equal; return `ErrIdempotencyConflict` when it differs.
3. Insert or resolve the task and lock its row with `SELECT ... FOR UPDATE`.
4. Check idempotency again after the lock, because another writer may have
   committed while the task upsert waited.
5. Require a nil base for sequence 1. Once a head exists, require
   `base_checkpoint_id` and compare it to the locked head.
6. Insert the checkpoint and every declared reference.
7. Advance the task head only from the observed base/sequence.
8. Commit all rows atomically.

Checking replay before the base precondition is intentional: a retry of an
already committed request remains a successful replay even after a later
checkpoint advances the head.

Two different writes based on one head cannot both succeed. PostgreSQL row
locking and uniqueness constraints enforce this invariant across processes;
there is no in-memory mutex or check-then-insert race.

### Resume is deterministic and authorization-filtered

`Service.Resume` selects either an explicit checkpoint or the task head. Every
query includes:

- `workspace_id`;
- the stable `task_key`;
- an optional narrower scope;
- and the Token's allowed paths.

Path comparisons are segment-safe: `/Work` does not authorize `/Workflows`.
Absent, cross-workspace, and out-of-path records all return `ErrNotFound`.

The persistence service returns the checkpoint plus its declared references. It
does not perform semantic search, invoke a model, or claim that references are
currently available. The HTTP layer resolves references under the same
authorization model and may apply response budgets.

### Scope paths are immutable checkpoint authorization labels

A checkpoint's `scope_path` occurs in the signed/hash-verified payload and in
its indexed database projection. It is part of the checkpoint's historical
meaning and authorization boundary.

Folder rename, move, and delete operations must **not** rewrite
`task_checkpoints.scope_path`, the JSON payload, or `payload_sha256`. Doing so
would invalidate checkpoint integrity and could silently change who may read a
historical handoff.

For P0, folder mutations whose subtree contains an Agent task/checkpoint should
be rejected, just as destructive folder operations already protect durable
memory. A future explicit task re-scope operation may:

1. require authorization to both the old and new scopes;
2. create a new rebase checkpoint/audit event;
3. update only the task's current scope and head;
4. retain historical checkpoints under their original scopes.

This explicit operation is intentionally outside migration `0009`.

## Consequences

Positive:

- Claude Code and Codex can exchange one vendor-neutral task state.
- Retries, concurrent Agents, and stale writers have explicit outcomes.
- Every checkpoint has immutable provenance, content integrity, and ordered
  dependencies.
- Resume works without an embedding provider, Worker, or chat model.
- UI and export code can list tasks, checkpoint history, and references without
  interpreting arbitrary memory attributes.

Trade-offs:

- The write path has one short row lock per task. This favors correct personal
  task state over concurrent divergent heads.
- v1 allows one linear head rather than branches or automatic merges. A stale
  Agent must resume and create a new checkpoint.
- Reference availability is resolved at read time; a deleted source remains a
  visible missing dependency instead of cascading away its provenance.
- Legacy free-text `task_state` memories remain searchable evidence but are not
  resumable checkpoints.

## Rejected alternatives

### Store the handoff only in `memories.attributes`

This cannot enforce schema version, ordered lineage, base/head CAS, or reference
integrity without making every query parse application-defined JSON.

### Keep one mutable “current task state” row

This makes retries overwrite provenance, prevents historical inspection, and
cannot distinguish two Agents racing from the same base.

### Use client-assigned sequence numbers

Two clients can choose the same revision. The server instead allocates sequence
under the locked task row and treats the checkpoint UUID as the immutable
identity.

### Rewrite checkpoint paths during folder moves

This changes an immutable payload's authorization meaning and breaks its stored
hash. P0 rejects the mutation; future re-scope must be explicit and auditable.

### Use lexical `mem_context` to guess the latest task state

Retrieval ranking is not a state-machine guarantee. Exact resume uses
`task_key` and the database head; semantic context retrieval remains a
complementary evidence path.
