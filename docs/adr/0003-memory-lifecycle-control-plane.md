# ADR 0003: Memory lifecycle is an auditable control plane

- Status: Accepted
- Date: 2026-07-28

## Context

ADR 0001 introduced immutable Agent memory occurrences and deliberately left
feedback and forgetting for a separate change. Without those operations, an
Agent can write durable context but neither the user nor another Agent can
control whether that context remains eligible for recall.

The lifecycle API must preserve four properties:

1. retries and concurrent clients have deterministic outcomes;
2. every object lookup is filtered by workspace and Token path before its
   existence or state is disclosed;
3. feedback may influence retrieval but cannot silently rewrite source
   evidence;
4. forgetting removes the user payload from the live memory plane without
   claiming that PostgreSQL backups or storage media have been cryptographically
   erased.

## Decision

### Keep immutable evidence separate from mutable control state

The content, source, producer and occurrence time of a memory are immutable.
The row also carries a small control projection:

- lifecycle status (`active`, `archived` or the internal `forgotten`
  tombstone state);
- an optimistic `state_version`;
- pin state;
- bounded explicit-feedback counters.

Every control-plane write also appends a `memory_event`. The event records the
action, authenticated actor, hashed idempotency key, normalized request hash,
expected/resulting version and time. It does not copy the memory content.
When a memory is forgotten, raw actor identifiers are removed from its event
history; the terminal event retains only a one-way, user-bound replay receipt.

`useful`, `not_useful`, `pin` and `unpin` are feedback actions. Archive and
restore are explicit lifecycle transitions. They never mutate the immutable
evidence fields.

### Make every transition idempotent and compare-and-swap

Control-plane writes require both:

- an `Idempotency-Key` header identifying one caller intent; and
- `expected_version` identifying the state the caller inspected.

The service resolves replay and projection state in one transaction under a
row lock. Forget additionally checks a one-way principal receipt because its
original path has already been erased. Reusing a key with the same normalized
command returns the committed result; reusing it for a different command
returns an idempotency conflict. A new command locks the row, compares
`expected_version`, applies its projection and appends its event in one
transaction.

This gives browser, CLI and MCP retries the same semantics and prevents two
stale clients from silently overwriting each other's control changes.

### Use stable, authorization-bound list cursors

Memory browsing is newest-first by `(created_at, id)`. Unlike `updated_at` or
`event_at`, those fields cannot change during a pin, archive, folder move or
feedback event, so keyset pagination does not duplicate or skip records when
control state changes.

The opaque cursor is bound to the normalized filters and the Token's allowed
paths. A cursor issued under one query or authorization boundary is rejected
when reused under another. Workspace, lifecycle, scope and Token path filters
are all applied in PostgreSQL before `LIMIT`.

Forgotten tombstones are never included in list or recall results.

### Forget the memory, not its independent source

`POST /v1/memories/{id}/forget` requires delete scope and a workspace role that
permits deletion. It synchronously redacts the memory's user payload from the
live table; clears source, producer, original path, creator and request-hash
projections; marks the row forgotten; and retains only the minimum tombstone
required for:

- retry safety;
- prevention of stale `remember` retries resurrecting the same occurrence;
- authorized audit and deterministic conflict handling.

Forgetting a memory does not delete a separately owned source file. That
requires an explicit file deletion. Normal reads and recall never return a
forgotten payload. Remember and control idempotency keys are stored only as
SHA-256 digests, never as caller plaintext.

This operation is a logical deletion from the live service, not a claim of
cryptographic erasure. WAL, replicas and backups follow the deployment's
documented retention policy. True immediate crypto-erasure would require
envelope encryption and destruction of a per-memory or per-workspace key.

### Authorize the persisted path, including replays

The request path is checked before a new write, but that is insufficient for
an idempotent replay because a folder move may have changed the persisted
memory path. Replay lookup therefore applies the caller's current allowed-path
filter to the stored row. A record that is absent, cross-workspace or outside
the Token path boundary has the same not-found response.

Lifecycle operations also authorize the current persisted path inside the
database query. They do not trust a path supplied by the client. After Forget
has generalized the path, only an exact, user-bound one-way receipt can replay
that Forget result; it does not reopen path-scoped reads.

### Keep retrieval effects bounded and explainable

Recall continues to hard-filter for `active` records. Archive removes a memory
from normal recall and restore makes it eligible again. A pin may only add a
small, explicit boost to an already matched candidate; it cannot manufacture a
candidate or move a weak fuzzy result across a stronger exact/full-text lane.
The recall reason exposes the pin signal.

Useful/not-useful feedback is recorded now. Any stronger learned reranking is
deferred until scenario fixtures can measure its effect.

## Consequences

Positive:

- users and Agents can close the remember → recall → feedback/forget loop;
- retries through HTTP, CLI, MCP and Web converge on one committed event;
- stale UIs receive a version conflict instead of silently changing newer
  state;
- folder moves cannot turn an old idempotency key into a path-scope data leak;
- the Web surface can present a trustworthy memory ledger without becoming a
  chat client.

Trade-offs:

- every control mutation takes a short row lock;
- a minimal forgotten tombstone remains until a future retention policy
  removes it;
- pin and explicit feedback use intentionally conservative ranking semantics;
- correction/supersession remains a follow-up transaction rather than an
  in-place edit.

## Rejected alternatives

### Edit memory content in place

This destroys the evidence that an Agent originally wrote and makes citations
change meaning. Correction will create a new occurrence and an explicit
supersession relationship.

### Treat archive as forget

Archive is reversible and should retain evidence. Forget is an irreversible
live-data redaction and requires stronger authorization.

### Physically delete the row with no tombstone

That allows a delayed retry of the original `remember` request to recreate the
forgotten occurrence and makes a lost forget response impossible to replay
safely.

### Claim that SQL deletion erases backups

PostgreSQL MVCC, WAL and backup retention make that claim false. The API and
documentation state the actual live-service guarantee.
