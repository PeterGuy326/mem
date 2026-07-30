# Managed AI usage and embedding entitlements

Managed AI stages (including embeddings) are an optional hosted-service
boundary. They do not turn mem into an answer-model proxy, and they do not
make embeddings mandatory for structured memory: `remember` and lexical
`context --source memory` remain model-independent.

## Deployment modes

| Mode | AI source | Commercial behavior |
| --- | --- | --- |
| `private` | Local or user-configured provider | No subscription or entitlement check |
| `saas`, local/BYOM provider | A provider spec other than the platform-managed exact spec | No managed-service charge |
| `saas`, managed provider | The exact `MEM_MANAGED_EMBEDDING_PROVIDER` spec | Active workspace entitlement and available units required |

Workspace membership and commercial entitlement are intentionally separate.
Authentication resolves the account, Agent token, workspace membership,
scope, and path before the entitlement store or managed provider is touched.
The entitlement belongs to the workspace, so every member and Agent token uses
the same bounded workspace allowance without turning membership itself into a
paid-plan flag.

Payment collection is outside this change. A billing service or an operator
may activate `workspace_entitlements`; mem consumes that payment-provider-
neutral state.

`idealab-quality-v1` is a SaaS-only workspace profile. Private deployments may
continue to use their local/BYOM path, but they cannot select this managed
quality profile.

## SaaS configuration for `idealab-quality-v1`

To make the managed quality profile available, configure the hosted process and
the Worker with these exact values (inject the endpoint credential at runtime;
do not put it in a checked-in `.env` file):

```bash
MEM_DEPLOYMENT_MODE=saas
MEM_AI_PROFILES=local-fast-v1,idealab-quality-v1
MEM_MANAGED_EMBEDDING_PROVIDER=openai:text-embedding-3-large
MEM_MANAGED_EMBEDDING_RESERVATION_TTL=2m

# Worker process configuration: use the Idealab OpenAI-compatible endpoint.
OPENAI_BASE_URL=https://<idealab-openai-compatible-endpoint>
OPENAI_API_KEY=<injected-runtime-secret>
```

`MEM_AI_PROFILES` defaults to only `local-fast-v1`; listing the paid profile is
an explicit operator action. When `idealab-quality-v1` is enabled in SaaS,
`MEM_MANAGED_EMBEDDING_PROVIDER` must be exactly
`openai:text-embedding-3-large`. The provider value must be one exact
`<provider>:<model>` spec. There is no fallback across a billing or privacy
boundary. SaaS startup and `/readyz` fail closed when the entitlement schema or
managed provider configuration is unavailable. Private mode bypasses this
readiness dependency. The selected profile sends `dimensions: 768` on each
embedding request; do not set a process-wide dimension variable merely to
activate this profile.

After the service is healthy, an authorized workspace administrator can inspect
the server allowlist and select only its ID:

```bash
mem profile list
mem profile status
mem profile select idealab-quality-v1
```

The CLI never accepts a model name, base URL, prompt, or API key for this
operation. The server fixes the managed profile to
`openai:text-embedding-3-large` at 768 dimensions and the pinned
`openai:qwen3.7-max-2026-06-08` text LLM; CLIP remains the fixed local visual
space. VLM, ASR, and reranking are explicitly disabled until Idealab exposes
reviewed API contracts for each capability. The profile name is a routing and
support contract, not a benchmark or universal-quality claim.

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
pre-invocation rule. Selecting `idealab-quality-v1` first reserves its fixed
embedding preflight; the Worker is not asked to probe until that succeeds.
For each supported file, mem reserves every declared managed stage that its
MIME pipeline can invoke before sending the source to the Worker (for example,
text/PDF embedding plus LLM). Images currently stay on the fixed local CLIP
stage and consume no managed unit; their bytes are not sent to a guessed VLM.
A Worker timeout, failed response, or partial managed result is retained as
indeterminate rather than retried into a second provider call.

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
| `402` | Managed plan inactive or absent | Offer membership; keep local/BYOM available |
| `409` | Idempotency conflict, active request, or released key | Review the original request/key |
| `410` | A succeeded replay reference is no longer authorized or available | Never fall back to a new provider call |
| `429` | Workspace unit allowance exhausted | Honor `Retry-After` and reset headers |
| `502` | Provider failure or indeterminate accounting | Show a redacted, stable failure |
| `503` | SaaS entitlement control plane unavailable | Fail closed for managed service |
| `504` | Provider timed out with uncertain outcome | Never auto-retry; inspect usage/request state |

The Web presentation distinguishes `401`, `403`, `402`, `429`, `502`, and
`504`. Failure to load the optional entitlement summary does not hide local or
BYOM provider settings.

## Stored data and privacy

The usage projection and event ledger contain only workspace ID, operation,
provider/model identifiers, units, status, period/timestamps, and SHA-256
request/idempotency hashes. Successful replay stores bounded file/evidence
UUIDs, source, rank, and score in typed columns. Raw query text, file content,
snippets, vectors, tokens, credentials, idempotency keys, and provider
response bodies are not stored in these tables.

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
