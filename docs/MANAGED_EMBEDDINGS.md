# Managed AI usage and embedding entitlements

Managed AI stages (including embeddings) are an optional hosted-service
boundary. They do not turn mem into an answer-model proxy, and they do not
make embeddings mandatory for structured memory: `remember` and lexical
`context --source memory` remain model-independent.

## Deployment modes

| Mode | AI source | Commercial behavior |
| --- | --- | --- |
| `private` | Local or user-configured provider | No subscription or entitlement check |
| `saas`, `local-fast-v2` | The fixed local profile | No managed-service charge |
| `saas`, `idealab-quality-v2` | The fixed managed Idealab profile | Active workspace entitlement and available units required |

Workspace membership and commercial entitlement are intentionally separate.
Authentication resolves the account, Agent token, workspace membership,
scope, and path before the entitlement store or managed provider is touched.
The entitlement belongs to the workspace, so every member and Agent token uses
the same bounded workspace allowance without turning membership itself into a
paid-plan flag.

Payment collection is outside this change. A billing service or an operator
may activate `workspace_entitlements`; mem consumes that payment-provider-
neutral state.

`idealab-quality-v2` is a SaaS-only workspace profile. Private deployments may
continue to use their local/BYOM path, but they cannot select this managed
quality profile.

## SaaS configuration for `idealab-quality-v2`

To make the managed quality profile available, configure the hosted process and
the Worker with these exact values (inject the endpoint credential at runtime;
do not put it in a checked-in `.env` file):

```bash
MEM_DEPLOYMENT_MODE=saas
# Keep local-fast-v1 while any persisted workspace still uses that snapshot.
MEM_AI_PROFILES=local-fast-v1,local-fast-v2,idealab-quality-v2
MEM_MANAGED_EMBEDDING_PROVIDER=idealab:text-embedding-3-large
MEM_MANAGED_EMBEDDING_RESERVATION_TTL=10m
MEM_WORKER_GRPC=worker.internal:50051
MEM_WORKER_AUTH_KEY_ID=memd-primary
MEM_WORKER_AUTH_KEY_B64=<standard-base64-of-exactly-32-random-bytes>

# Worker process configuration: the dedicated binding has no default endpoint.
MEM_WORKER_AUTH_MODE=required
MEM_WORKER_AUTH_KEY_ID=memd-primary
MEM_WORKER_AUTH_KEY_B64=<the-same-32-byte-key>
MEM_WORKER_AUTH_REPLAY_REDIS_URL=rediss://<private-shared-redis>
IDEALAB_BASE_URL=https://<idealab-openai-compatible-endpoint>
IDEALAB_API_KEY=<injected-runtime-secret>
```

Generate the shared key through a secret manager or, for a disposable test,
`openssl rand -base64 32`. Never commit or log it. Every Worker replica must
use the same private Redis deployment for nonce claims. Production Redis
should use private networking or `rediss://`, authenticated access, adequate
capacity, and a policy that does not evict live ten-minute replay keys.

The selectable catalog exposes only `local-fast-v2`; the deprecated local V1
snapshot is retained internally for existing selections only when its ID
remains in the runtime allowlist. Listing the paid profile is an explicit
operator action. When only `idealab-quality-v2` is enabled in SaaS,
`MEM_MANAGED_EMBEDDING_PROVIDER` must be exactly
`idealab:text-embedding-3-large`. The value is the primary exact
`<provider>:<model>` spec; memd derives the complete exact managed-provider
allow-set from the enabled immutable profiles. There is no arbitrary-provider,
prefix, or fallback match across a billing or privacy boundary. SaaS startup
fails before the HTTP server and queue start unless an authenticated readiness
challenge succeeds for every derived V1/V2 binding with the same HMAC key and
shared replay store. This readiness check does not make a paid model call; the
profile-selection embedding preflight validates the real endpoint and
dimensions before activation. `/readyz` separately checks the entitlement
schema/configuration.
Private mode bypasses the entitlement readiness dependency. The selected
profile sends `dimensions: 768` on each embedding request; do not set a
process-wide dimension variable merely to activate this profile.
`OPENAI_API_KEY`, `OPENAI_BASE_URL`, and OpenAI's public default endpoint
cannot satisfy an `idealab:*` stage.

The reservation TTL must be at least six minutes because one indexing Worker
RPC may run for five minutes and still needs a bounded settlement margin.
The default is ten minutes. The reconciler must never reclaim an active
reservation while its Worker request is still within that execution window.

After the service is healthy, an authorized workspace administrator can inspect
the server allowlist and select only its ID:

```bash
mem profile list
mem profile status
mem profile select idealab-quality-v2
```

The CLI never accepts a model name, base URL, prompt, or API key for this
operation. The server fixes the managed profile to
`idealab:text-embedding-3-large` at 768 dimensions and the pinned
`idealab:qwen3.7-max-2026-06-08` text LLM. The profile currently accepts text
and `application/pdf`; only PDFs with a text layer produce embedding/LLM
output, while scanned PDFs have no OCR path and release both unused stages.
Visual embedding, VLM, ASR, and reranking are explicitly disabled until their
installation or managed API contracts are reviewed. The profile name is a
routing and support contract, not a benchmark or universal-quality claim.

The current immutable catalog snapshot is
`idealab-quality-v2@2026-07-30.1` with pipeline revision
`file-enrichment-v2`.

The published `idealab-quality-v1@2026-07-29` /
`file-enrichment-v1` snapshot remains exact and executable for a workspace
that already selected it and whose operator explicitly keeps
`idealab-quality-v1` enabled and sets `MEM_OPENAI_MANAGED_BINDING=true` on both
memd and Worker. The Worker must also receive the V1
`OPENAI_BASE_URL`/`OPENAI_API_KEY` binding. That explicit flag distinguishes
the hosted V1 credential from an unsigned private OpenAI BYOM configuration,
forces Worker authentication, and disables cross-origin HTTP redirects for the
managed V1 adapter.

During migration one process may enable both managed generations:

```bash
MEM_AI_PROFILES=local-fast-v1,local-fast-v2,idealab-quality-v1,idealab-quality-v2
MEM_MANAGED_EMBEDDING_PROVIDER=idealab:text-embedding-3-large
MEM_OPENAI_MANAGED_BINDING=true
```

memd derives and checks both exact embedding bindings at startup. V1 stays
hidden from `profile list` and cannot be newly selected, while persisted V1
workspaces continue to resolve through the exact published snapshot. Removing
either V1 ID is an operator kill switch that disables matching persisted
selections immediately. Existing V1 corpora cannot be reinterpreted as V2;
they stay on V1 until a versioned index generation performs a complete rebuild.

Once selected, the profile locks those model choices. A failure of a declared
managed stage never falls back to a local model, a `MEM_DEFAULT_*` model, or a
different cloud model. If the embedding stage is unavailable, the semantic
operation fails rather than changing vector spaces. For file processing, mem
may retain successful declared-stage outputs and report a partial result, but a
partial result never means that an undeclared fallback model saw the file.
`context --source all` likewise keeps independent lexical-memory evidence with
a bounded partial warning when managed file recall fails.

Profile selection is model routing, not a claim that every model has the same
price or benchmark result. Each declared managed stage uses the pre-invocation
accounting lifecycle described below. Switching between local and managed
profiles requires a fresh versioned index generation and complete rebuild; it
is not a provider setting to change in place.

For an initial, offline entitlement activation, an operator can use a
transaction like the following after replacing the workspace ID and quota.
It deliberately refuses to reset an entitlement with outstanding usage:

```sql
BEGIN;

SELECT workspace_id
FROM workspace_entitlements
WHERE workspace_id = '00000000-0000-0000-0000-000000000000'
FOR UPDATE;

UPDATE workspace_entitlements
SET plan_key = 'pro',
    status = 'active',
    period_start = date_trunc('month', now()),
    period_end = date_trunc('month', now()) + interval '1 month',
    managed_embedding_unit_limit = 10000,
    managed_embedding_units_reserved = 0,
    managed_embedding_units_consumed = 0,
    updated_at = now()
WHERE workspace_id = '00000000-0000-0000-0000-000000000000'
  AND managed_embedding_units_reserved = 0
  AND managed_embedding_units_consumed = 0;

COMMIT;
```

A production billing integration should own plan changes and preserve the same
row-locking and counter invariants. It must not write payment identifiers,
invoices, credentials, or raw provider responses into the usage tables.

## Request and accounting lifecycle

For managed text/auto search and file Context Pack recall, the server executes:

```text
authenticate → resolve workspace membership → authorize scope/path
             → resolve exact provider → atomically reserve one unit
             → call provider → persist safe replay references
             → finalize reserved → consumed → return result
```

`Idempotency-Key` is required at the HTTP boundary. CLI, Web, and MCP attach
one; CLI and MCP callers may provide a stable key explicitly. The uniqueness
domain is workspace plus operation. The same key and request replays persisted
references without a provider call or a second charge; the same key with a
different request returns `409`.

The last available unit is serialized by the workspace entitlement row, so
concurrent requests cannot oversubscribe it. A provider timeout or uncertain
failure is marked `indeterminate` and holds its current-period unit because
the platform cannot prove that the provider did not execute. Do not
automatically retry a `504`; first inspect the workspace usage/request state
or contact an administrator. Stale crash reservations are reconciled to the
same state after the configured TTL. Period rollover freezes crossing
reservations as indeterminate before resetting the new period's counters.

Managed profile activation and asynchronous file enrichment follow the same
pre-invocation rule. Selecting `idealab-quality-v2` first reserves its fixed
embedding preflight; the Worker is not asked to probe until that succeeds.
For each supported file, mem reserves every declared managed stage that its
MIME pipeline can invoke before sending the source to the Worker (for example,
text/PDF embedding plus LLM). The Worker returns a closed per-stage receipt:
`not_invoked`, `succeeded`, or `indeterminate`. Short text therefore releases
the unused LLM reservation, and empty text or a PDF without a text layer
releases both units. Only a stage proven successful is finalized, and only
after its indexing result commits. The file result and exact stage-settlement
intent commit in one PostgreSQL transaction; an idempotent outbox drain runs
immediately and before stale-reservation reconciliation, so a process crash
between result persistence and quota settlement cannot trigger a duplicate
provider call or lose the known outcome. An attempted stage with an uncertain
outcome remains indeterminate. If a failed Worker receipt proves a stage was
`not_invoked`, mem durably releases it, removes the non-replayable failed-attempt
marker, and derives a chained reservation key for a safe retry. A failed
attempt containing any `succeeded` or `indeterminate` stage is not invoked
again. Deleting the source file sets the outbox `file_id` to `NULL` instead of
deleting a pending accounting intent and atomically clears its
`content_sha256`; the global reconciler can still apply the closed outcome by
usage ID. Images and audio are rejected by this profile before Worker dispatch;
their bytes are not sent to a guessed provider.

All hosted Worker `Process` calls are authenticated before JSON parsing,
storage access, provider construction, or request logging. The request MAC
covers the full gRPC method, exact scope, key ID, timestamp, a 192-bit nonce,
and the deterministic protobuf body. Redis accepts a nonce once across all
replicas. For a managed route, Worker also hashes the fetched bytes and
requires equality with the signed `sha256` field before egress. Successful
responses carry a request-nonce-bound MAC over their deterministic protobuf
body; memd verifies it before persisting outputs or applying the per-stage
receipt. A missing/invalid response proof is indeterminate, never a trusted
success.

HMAC provides authentication and integrity, not encryption. Do not expose
Worker port 50051 to the Internet. Run memd and Worker on a private,
policy-restricted network and prefer TLS/mTLS for transport confidentiality.
Unsigned `HealthCheck` is liveness only; it does not establish managed
readiness.

Migration `0017_workspace_ai_profiles.sql` stores the selected harmless
snapshot. Migration `0018_managed_ai_stage_settlement_outbox.sql` adds the safe
managed-stage settlement outbox for databases that already applied `0017`.
Before rolling back from 18 to 17, stop indexing and drain every pending
outbox row; that rollback drops only the recovery table and keeps profile
selections. Rolling back from 17 to 16 then removes workspace selections.
Restore both migrations and reselect profiles after a rollback that crossed
17. Removing a profile ID from `MEM_AI_PROFILES` immediately disables old
persisted selections at read/route time.

If managed file recall fails while lexical structured-memory recall succeeds,
`context --source all` returns the surviving evidence with
`partial=true` and a bounded warning. It never discards useful lexical memory.

## API and client behavior

`GET /v1/entitlements/current` is an authenticated, workspace-scoped,
read-only summary. It does not reserve a unit. Private mode reports
`commercial_gate=false`; SaaS reports plan status, limit, reserved, consumed,
remaining, and reset time.

| HTTP | Meaning | Client behavior |
| --- | --- | --- |
| `401` | Missing or expired session | Sign in |
| `403` | Workspace, scope, or path denied | Do not inspect entitlement or call provider |
| `402` | Managed plan inactive or absent | Offer a managed-plan upgrade; keep `local-fast-v2` available |
| `409` | Idempotency conflict, active request, or released key | Review the original request/key |
| `410` | A succeeded replay reference is no longer authorized or available | Never fall back to a new provider call |
| `429` | Workspace unit allowance exhausted | Honor `Retry-After` and reset headers |
| `502` | Provider failure or indeterminate accounting | Show a redacted, stable failure |
| `503` | SaaS entitlement control plane unavailable | Fail closed for managed service |
| `504` | Provider timed out with uncertain outcome | Never auto-retry; inspect usage/request state |

The Web presentation distinguishes `401`, `403`, `402`, `429`, `502`, and
`504`. Failure to load the optional entitlement summary does not hide the SaaS
local profile or private-deployment BYOM settings.

## Stored data and privacy

The usage projection and event ledger contain only workspace ID, operation,
provider/model identifiers, units, status, period/timestamps, and SHA-256
request/idempotency hashes. Successful replay stores bounded file/evidence
UUIDs, source, rank, and score in typed columns. While a source file exists,
the settlement outbox additionally stores its raw `content_sha256` so a
duplicate queue delivery can match the exact file/profile/pipeline result.
That digest can confirm a known file and correlate equal content, so it is
treated as file identity metadata rather than anonymous data. Deleting the
file atomically nulls both outbox `file_id` and `content_sha256`; pending
accounting retains only usage ID, closed stage outcome, profile revisions, and
timestamps. Raw query text, file content, snippets, vectors, tokens,
credentials, idempotency keys, and provider response bodies are not stored in
these tables.

Replay always re-authorizes user ownership, workspace, requested path, Agent
token paths, MIME filters, and time bounds. A deleted or newly out-of-scope
reference returns `410` and never causes an unaccounted provider retry.

Entitlement and usage rows cascade when their workspace is deleted. This is a
deliberate account-deletion/privacy choice: the in-database usage audit is
erased with the workspace. Operators that require longer financial retention
must implement it in a separately governed system with an explicit retention
policy.

## Migration and rollback

Migration `0014_managed_embedding_entitlements.sql` seeds every existing and
new workspace with an inactive `free` entitlement and zero units. Existing
private deployments continue to operate without configuration changes.

Before downgrading below `0014`, stop SaaS traffic. The down migration removes
entitlements, usage events, replay references, and counters; consumed usage
cannot be reconstructed by the schema rollback. A new SaaS binary against the
older schema fails readiness, while an older/private binary remains compatible
because it does not use these tables.
