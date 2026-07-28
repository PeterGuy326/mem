# ADR 0001: Agent memory records are model-independent source data

- Status: Accepted
- Date: 2026-07-28

## Context

mem already stores source files and returns bounded Context Packs to external
Agents. It does not yet have a durable contract for an Agent to write a
decision, preference, observation, or task state back into the shared memory
plane.

The first implementation must satisfy four properties:

1. an Agent write is durable even when the indexing Worker or a cloud model is
   unavailable;
2. retrying a write cannot silently duplicate or overwrite memory;
3. every recalled item retains workspace, path, actor, Agent/session/task, and
   source provenance;
4. mem returns evidence and never becomes the answer-generating Agent.

It is too early to define automatic fact consolidation, contradiction
resolution, or a canonical knowledge graph. Those operations would require
evaluation data and explicit correction semantics that the project does not
yet have.

## Decision

Each successful `remember` call creates one immutable, auditable memory record.
The record is an occurrence: identical content written with different
idempotency keys remains two records so that separate times and sources are not
lost.

`POST /v1/memories` is the canonical write API. The workspace comes from the
authenticated request rather than the body. `Idempotency-Key` is required and
is unique inside a workspace:

- the same normalized request and key returns the original record as a replay;
- the same key with a different normalized request returns
  `409 idempotency_conflict`;
- content hashes are evidence integrity fields, not deduplication keys.

The first recall lane is deterministic PostgreSQL lexical retrieval:

- exact phrase matching;
- `simple` full-text matching;
- `pg_trgm` word similarity for short and CJK text.

This lane is immediately consistent and does not require an embedding or chat
model. `mem_context` can combine it with the existing file retrieval lane while
reporting partial-source failures. A caller can also request only structured
memory.

Memory records use the same canonical virtual paths as files. Workspace and
Token path filters are mandatory in every read and recall query. Folder moves
rewrite memory paths transactionally. Folder deletion must not silently erase
memory; explicit archive/forget semantics will be added separately.

## Consequences

Positive:

- `remember -> context` works without a cloud service or local model.
- Agent retries are safe and concurrent writes cannot overwrite each other.
- Provenance remains available for later consolidation and correction.
- The first public contract stays small enough to review and test.

Trade-offs:

- Paraphrase recall is weaker than a versioned hybrid lexical/dense index.
- Every record is an occurrence; canonical facts and occurrence grouping are
  intentionally deferred.
- `feedback`, `supersede`, and `forget` remain separate follow-up changes.
- Lexical ranking is a baseline, not the final retrieval architecture.

Memory feedback, archive/restore and live-payload forgetting were subsequently
defined by [ADR 0003](0003-memory-lifecycle-control-plane.md). Immutable
correction/supersession remains a follow-up.

## Rejected alternatives

### Store Agent memories as synthetic files

This would reuse the existing embedding pipeline, but it would conflate source
assets with Agent observations and make provenance, correction, and forgetting
ambiguous.

### Require an embedding model on every write

This would make durable memory dependent on a replaceable provider and would
lose read-after-write behavior during model or Worker outages.

### Add canonical facts, occurrences, relations, and feedback in one change

The semantics are not yet backed by scenario fixtures. Introducing all layers
now would create a large schema whose invariants cannot yet be validated.

### Let mem call a chat model to normalize or answer

That crosses the product boundary. The external Agent owns reasoning and final
answers; mem owns durable evidence, recall, provenance, and control.
