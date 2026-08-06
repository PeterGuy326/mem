# Scoped Durable Context (`durable-context.v1`)

Status: additive read-only contract for resuming explicitly approved,
workspace-scoped active memory across sessions and channels.

Requirement: [mem#70](https://github.com/fullstack-ai-infra/mem/issues/70)
REQ-001 / AC-001.

## Why one pinned contract

A digital employee must resume approved context across sessions, but direct
database sharing or implicit private-chat capture would bypass workspace
authorization, provenance, forget, and version compatibility. This contract is
the only supported path: version-pinned, read-only, and grant-scoped.

## Contract rules

- The wire contract is pinned: every recall/read request MUST carry
  `contract=durable-context.v1`. Any other value fails with
  `400 contract_unsupported`; clients and servers never negotiate silently.
- Only **explicitly granted**, **workspace-scoped**, **active** memories are
  resumable. Superseded (archived) memories surface as stale, forgotten
  memories are redacted, and unapproved or out-of-scope items are reported as
  absent.
- No shared databases, copied storage, implicit chat capture, model
  credentials, or DWS dependency are required or supported.
- Recall is read-only. Writes remain the canonical memory APIs' job.

## HTTP surface

| Method | Path | Token scope | Purpose |
|--------|------|-------------|---------|
| `POST` | `/v1/durable-context/recall` | `read` | Resume granted active context for one principal |
| `GET` | `/v1/durable-context/memories/{id}` | `read` | Resolve one granted memory for one principal |
| `POST` | `/v1/durable-context/grants` | `admin` | Approve one explicit read grant |
| `GET` | `/v1/durable-context/grants` | `admin` | List the workspace allowlist (optional `principal` filter) |
| `POST` | `/v1/durable-context/grants/{grant_id}/revoke` | `admin` | Soft-revoke one grant (audit row retained) |

Recall request body:

```json
{
  "contract": "durable-context.v1",
  "principal": "alice",
  "session_ref": "session-2",
  "limit": 20
}
```

`principal` is the stable employee/user identity key
(`^[a-z0-9][a-z0-9._-]{0,127}$`). `session_ref` is opaque metadata only;
authorization is keyed by `(workspace, principal, memory)`. The token path
boundary is still enforced because each granted memory is resolved through the
canonical memory read path.

Each recall hit carries the memory, its provenance, its `state_version`, and a
version-pinned locator:

```
mem://memories/<memory-id>@<state-version>
```

The pinned locator lets a consumer detect that the resumed content moved to a
newer state instead of silently continuing with stale context.

## Error taxonomy

| Condition | Status | Error code |
|-----------|--------|------------|
| Unknown or missing contract version | 400 | `contract_unsupported` |
| Malformed command (principal, memory id, limit) | 400 | `invalid_durable_context` |
| Principal has no grants in this workspace | 403 | `context_scope_denied` |
| Memory/grant absent or out of scope | 404 | `not_found` |
| Memory was forgotten | 410 | `memory_forgotten` |
| Granted memory is superseded/archived | 409 | `context_stale` |
| Storage degradation | 502 | `context_unavailable` |
| Service disabled | 503 | `durable_context_disabled` |

Cross-principal and cross-workspace probes are indistinguishable from absent
objects (404). `403 context_scope_denied` means the principal has no active
grants in this workspace at all; an approved principal whose granted items are
all superseded or forgotten receives an empty `hits` list instead. The recall
`limit` window counts only active memories, so grants pointing at archived or
forgotten items can never crowd approved active context out of a recall.
`410 memory_forgotten` surfaces when the tombstone is still inside the
caller's token path scope; a forgotten item outside that scope is reported as
absent (404) per F5A.5 — both outcomes were observed in the manual AC-001
smoke and are compliant (forgotten context is never resumed either way).

## Grants lifecycle

- Grants upsert on `(workspace_id, principal, memory_id)`; re-granting after
  revocation restores the same audit row.
- Revocation is soft: the row keeps `revoked_at` plus the revoking user/token
  identifiers for audit.
- Forgotten memories cannot be granted; grants on forgotten or foreign
  memories fail.
- Grant token identifiers are server-internal audit fields and are never
  returned by the API.

## MCP tool

`mem_durable_context_recall` resumes approved context for one principal
through the pinned contract. Grant administration stays on the authenticated
HTTP/admin surface.

## Portability

Grants are operator-owned authorization policy for the target workspace, not
portable data. A workspace bundle carries the memories; the receiving
installation re-approves recall with explicit grants. Bundling grants would
require a bundle schema revision and is intentionally out of scope for this
contract.
