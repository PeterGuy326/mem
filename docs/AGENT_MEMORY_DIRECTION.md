# Agent Memory and Portability Direction

> Product direction for mem: an external memory brain for Agents, presented to
> people as an AI drive.

This document explains the product boundary and next milestones;
[SPEC.md](../SPEC.md) is the corresponding current implementation contract.

## 1. Product contract

mem is a user-owned **Memory Plane** shared by people and authorized Agents:

- It preserves original files, photos, audio and other evidence.
- It turns sources into structured, searchable and related memory.
- It returns evidence-backed context to any Agent through stable protocols.
- It lets the user inspect, correct, scope, export and forget that memory.
- It lets a new Agent or device resume from an explicit, verifiable handoff
  instead of reconstructing state from a vendor's private conversation store.

The AI drive and the Agent memory service are two views of the same data:

- **AI drive** is the human trust and control surface.
- **API / MCP / CLI** are machine and automation surfaces.
- **Memory Plane** is the single capability and data layer underneath them.

mem is not a chat application, Agent runtime, workflow engine or model host.
The calling Agent owns reasoning and the final answer.

The product north star and migration acceptance scenarios are defined in
[GOAL.md](../GOAL.md).

## 2. Five high-frequency scenarios

| Scenario | What is written | What must be recalled | Success condition |
|---|---|---|---|
| Resume work across sessions | Task outputs, decisions, changed files, unresolved questions | The last valid state, rationale and next actions | A new Agent continues without the user restating the project |
| Find exact personal evidence | Contracts, invoices, certificates, notes | Exact amount/date/clause with page or paragraph provenance | The user can open the cited original and verify it |
| Consolidate meetings and communication | Recording, transcript, minutes, selected messages | Decisions, action items, people, dates and later corrections | Asking “what did we decide?” returns the current decision and its history |
| Reuse preferences and constraints | User-confirmed style, habits, technical or privacy constraints | The applicable preference for this user, workspace and task | An Agent applies it only in scope and the user can correct or disable it |
| Recall multimedia life events | Photos, video metadata, audio, captions | People + time + place + event, with original media | A natural-language query finds the intended event, not merely a similar image |

These scenarios should drive acceptance tests. Features that do not improve at
least one of them are not near-term priorities.

## 3. The five-stage memory loop

### 3.1 Write

Every write preserves two different identities:

1. **Asset identity** — deduplicated bytes and their storage location.
2. **Memory occurrence** — when, where, why and by whom the information appeared.

A write should carry `workspace`, `source`, `agent/session/task`, `event_at`,
scope and an idempotency key. Uploading identical bytes twice may reuse one
asset, but it must not erase the second occurrence or its context.

### 3.2 Consolidate

Asynchronous consolidation should:

- Parse content while retaining page, paragraph, timecode or bounding-box
  provenance.
- Produce chunks, summaries, captions, entities, times and typed relations.
- Resolve aliases and duplicates without losing observations.
- Detect contradiction and create `supersedes` links instead of silently
  overwriting facts.
- Write into a versioned index generation so model changes can be built,
  evaluated and activated atomically.

Partial processing must remain visible. “Text indexed, faces unavailable” is
more useful and safer than a single ambiguous `done` state.

### 3.3 Recall

Recall is a retrieval orchestration problem, not one vector query:

1. Parse the task into semantic text, exact terms, entities, time, scope and
   requested memory type.
2. Generate candidates from lexical, dense, metadata, entity/time and relation
   indexes.
3. Expand only high-confidence graph neighbours.
4. Rerank with relevance, recency, importance, confidence and user feedback.
5. Apply thresholds, diversity and a context token budget.

Every returned item must explain why it appeared and how to open its source.

### 3.4 Use

mem returns a **Context Pack**, not a chat response. A Context Pack contains:

- Evidence excerpts and stable citations.
- File/memory identity and source locator.
- Retrieval reasons and confidence.
- Applicable scope and freshness.
- Omission and token-budget metadata.

The calling Agent decides, reasons and responds. This keeps mem useful across
Claude, Codex, Cursor, Cline and future Agents without binding it to one runtime.

### 3.5 Feedback

The loop closes with:

- Explicit actions: correct, pin, merge, supersede, archive and forget.
- Implicit signals: recalled, opened, cited, ignored or followed by another
  search.
- Periodic consolidation that uses those signals to improve ranking.

Implicit feedback may change ranking, but it must never silently turn an
unverified model inference into a user fact.

## 4. Minimal domain model direction

The existing files, folders, embeddings, entities and relations remain useful.
The smallest additive model is:

| Concept | Responsibility |
|---|---|
| `assets` | Deduplicated bytes, MIME, checksum and object-store key |
| `memories` | Normalized fact, observation, decision, preference or artifact reference |
| `memory_occurrences` | Source, actor, session/task, event time and scope for each occurrence |
| `memory_chunks` | Searchable text plus page/paragraph/timecode/bbox locator |
| `entity_mentions` | Entity links with evidence span and confidence |
| `memory_relations` | Typed edge, score, evidence, provenance and computation version |
| `index_generations` | Provider, model, dimension, configuration and active state |
| `recall_events` / `feedback` | Usage and correction signals |

PostgreSQL and pgvector are sufficient for the personal-data scale. A graph
database is not required until measured traversal or scale limits justify it.

## 5. Surface responsibilities

| Surface | Owns | Does not own |
|---|---|---|
| **API** | Canonical commands, queries, auth, scope, idempotency and response contracts | Client-specific presentation |
| **MCP** | Agent-friendly schemas for remember, context, source resolution and feedback | Storage, indexing or an Agent loop |
| **CLI** | Batch ingestion, scripting, reindex, diagnostics, eval, export and administration | A second business-logic implementation |
| **UI** | AI drive, source preview, recall explanation, corrections, permissions and forgetting | Hidden retrieval rules that differ from API |

Core semantics live once in the API. MCP, CLI and UI may have different
ergonomics, but they must not assign different meanings to the same operation.

## 6. `mem_context` replaces `mem_ask`

The canonical Agent tool is `mem_context`:

- `mem_search` discovers candidate assets.
- `mem_context` prepares evidence within a context budget.
- `mem_get` resolves a known source.
- The calling Agent produces the final answer.

The Phase 1 implementation retires `mem_ask`; the product does not maintain
divergent “ask” and “context” pipelines.

Memory-native tools (the control-plane subset is now implemented; correction
and merge remain follow-ups):

| Tool | Purpose |
|---|---|
| `mem_remember` | Write a structured observation, decision, preference or artifact reference |
| `mem_context` | Retrieve and pack evidence for an Agent task |
| `mem_feedback` | Record useful/not-useful and pin/unpin signals; future correction/merge keeps provenance |
| `mem_archive` / `mem_restore` | Reversible recall eligibility control |
| `mem_forget` | Irreversibly redact the live memory payload without implicitly deleting an independent source |

## 7. Competitive boundary

| Project | Primary product | What mem should learn | Why mem remains distinct |
|---|---|---|---|
| [Tencent/WeKnora](https://github.com/Tencent/WeKnora) | Document knowledge platform with RAG, Agents and auto-Wiki | Parser quality, hybrid retrieval, citations, evaluation and Agent-first CLI/MCP | mem is a longitudinal personal data layer with cross-Agent write-back and original multimedia assets, not an Agent runtime |
| [mem0ai/mem0](https://github.com/mem0ai/mem0) | Universal memory layer for AI Agents | Structured memory operations, cross-session recall, memory evaluation | mem couples memory with a user-visible, self-hosted AI drive and source originals |
| [supermemoryai/supermemory](https://github.com/supermemoryai/supermemory) | Documents and user memory exposed through API/MCP | A small context-oriented tool surface and evolving user profiles | mem emphasizes user-owned original media, locators and local/private deployment |
| [getzep/graphiti](https://github.com/getzep/graphiti) | Bi-temporal knowledge graph memory | Validity intervals, contradictions and episode provenance | mem starts file-native and PostgreSQL-native instead of requiring a graph-first product |
| [topoteretes/cognee](https://github.com/topoteretes/cognee) | Knowledge-graph memory pipelines | Remember/recall/forget/improve semantics | mem keeps the human AI-drive control surface and a narrower personal-memory scope |
| [letta-ai/letta](https://github.com/letta-ai/letta) | Stateful Agent platform/runtime | Context management and continual memory concepts | mem does not run the Agent and can serve many runtimes simultaneously |
| [khoj-ai/khoj](https://github.com/khoj-ai/khoj) | Personal AI, chat, research and custom Agents | Private personal knowledge UX | mem is protocol-first infrastructure, not a bundled assistant persona or chat destination |
| [nextcloud/server](https://github.com/nextcloud/server) | Self-hosted file sync and collaboration | Trust, ownership and file lifecycle | mem adds multimodal understanding, memory relations, Agent write-back and contextual recall |

WeKnora is a useful reference for the retrieval kernel, but importing it
wholesale would move mem toward a generic knowledge-base chat product. Adopt
capabilities, not product identity.

## 8. Defensible differentiation

The moat is a compounding loop rather than one model:

1. User-controlled original assets provide durable evidence.
2. Agents write decisions and outcomes back into the same memory.
3. Time, person, event and provenance links accumulate across modalities.
4. Real recall/use/correction feedback personalizes ranking.
5. Open API and MCP make the memory portable across Agent vendors.
6. The AI drive makes otherwise invisible Agent memory inspectable and trusted.

Model providers can be replaced. A clean, user-corrected longitudinal memory
graph with source evidence is much harder to replace.

## 9. Near-term roadmap

### R0 — Make the current index trustworthy

- Persist processor entities and metadata end-to-end.
- Link face clusters to file/entity relations.
- Index image captions in the text route.
- Correct relation ranking, thresholds, directionality and refresh behaviour.
- Preserve chunk locators and index provider/version.

Exit criterion: the five reference scenarios return verifiable sources and no
known-broken relation type is advertised.

### R1 — Introduce the Memory Plane contract

- Separate asset deduplication from memory occurrences.
- Add versioned index generations.
- Define API contracts for `remember`, `context`, `feedback` and `forget`.
- Keep `mem_context` as the single evidence-recall pipeline.

Exit criterion: two different Agents can write and resume the same scoped task,
with provenance and corrections preserved.

### R2 — Build the recall and feedback loop

- Add lexical + dense + entity/time/relation candidate generation.
- Add calibrated reranking, thresholds, diversity and token budgets.
- Capture explicit and implicit feedback.
- Establish offline scenario fixtures and online recall telemetry.

Exit criterion: scenario-level recall quality, citation coverage and latency are
measured continuously rather than judged from demos.

### R3 — Strengthen the trust UI

- Explain why each result was recalled.
- Preview and navigate to exact source locations.
- Let users correct entities, relations, scope and validity.
- Expose export, archive and forget controls.

Exit criterion: a user can inspect and correct every durable memory that may
influence an Agent.

## 10. Metrics

Track product-loop metrics, not only model latency:

- Resume success rate across Agent sessions.
- Recall@K / MRR for exact, semantic, entity and temporal queries.
- Citation coverage and source-locator correctness.
- Context utilization: returned items actually cited or opened.
- Correction, supersession and false-memory rates.
- Index freshness and partial-index rate.
- P50/P95/P99 recall latency and cost per Context Pack.

## 11. Explicit non-goals

Until the memory loop is proven, mem will not:

- Become a general chat frontend or ship a canonical assistant persona.
- Become an Agent runtime, workflow engine or multi-Agent orchestrator.
- Clone a generic enterprise knowledge-base product.
- Add a graph database or split into microservices without measured need.
- Treat unverified model inference as a durable user fact.
- Build a large connector marketplace before ingestion, recall and feedback are reliable.
- Optimize visual polish ahead of memory correctness, provenance and evaluation.
