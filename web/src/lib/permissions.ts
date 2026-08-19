/**
 * Permissions surface client: who can access the current workspace.
 *
 * Two identity families exist since mem#89: human browser sessions and
 * workspace-bound Agent tokens (both rows in the same tokens table), plus the
 * durable-context.v1 recall allowlist that authorizes principals to resume
 * explicitly approved memories. This module only wraps the existing admin
 * API contracts; it never invents new semantics.
 */
import { api, ApiException } from './api';

/** Canonical token scopes (SPEC §3 F7.3), in server order. */
export const TOKEN_SCOPES = ['search', 'read', 'write', 'delete', 'admin'] as const;
export type TokenScope = (typeof TOKEN_SCOPES)[number];

/**
 * Wire shape of one `GET /v1/auth/tokens` item. The server marshals
 * `auth.Token` without JSON tags, so keys are the Go field names; the CLI
 * consumes the same shape. Never rename these.
 */
export interface ApiTokenRecord {
  ID: string;
  UserID: string;
  Name: string;
  Scopes: string[];
  Paths: string[];
  WorkspaceID: string | null;
  RedactPII: boolean;
  ExpiresAt: string | null;
  LastUsedAt: string | null;
  CreatedAt: string;
}

/** One credential row normalized for display. */
export interface TokenView {
  id: string;
  name: string;
  scopes: string[];
  paths: string[];
  /** null for unbound human sessions; a workspace id for Agent tokens. */
  workspaceId: string | null;
  /** Unbound rows are human sessions; workspace-bound rows are Agent tokens. */
  kind: 'session' | 'agent';
  expiresAt: string | null;
  lastUsedAt: string | null;
  createdAt: string;
}

export function normalizeTokenRecord(record: ApiTokenRecord): TokenView {
  return {
    id: record.ID,
    name: record.Name,
    scopes: record.Scopes ?? [],
    paths: record.Paths ?? [],
    workspaceId: record.WorkspaceID,
    kind: record.WorkspaceID ? 'agent' : 'session',
    expiresAt: record.ExpiresAt,
    lastUsedAt: record.LastUsedAt,
    createdAt: record.CreatedAt,
  };
}

/** Derived allowlist view states returned by the grants listing. */
export type GrantViewStatus = 'active' | 'revoked' | 'superseded' | 'forgotten';

/** Wire shape of one durable-context grant audit row. */
export interface DurableContextGrant {
  id: string;
  workspace_id: string;
  principal: string;
  memory_id: string;
  mode: string;
  granted_by_user_id?: string;
  granted_at: string;
  revoked_at?: string;
  updated_at: string;
}

/**
 * Wire shape of one `GET /v1/durable-context/grants` item: the audit row
 * annotated with the granted memory's lifecycle and the derived view status.
 */
export interface DurableContextGrantView extends DurableContextGrant {
  memory_status: string;
  status: GrantViewStatus | string;
}

/** Lists the caller's tokens and sessions (server hard-deletes on revoke). */
export async function listTokens(): Promise<TokenView[]> {
  const res = await api.get<{ tokens: ApiTokenRecord[] }>('/auth/tokens');
  return (res.tokens ?? []).map(normalizeTokenRecord);
}

/** Revokes one token by id. The credential stops working immediately. */
export async function revokeToken(id: string): Promise<void> {
  await api.del(`/auth/tokens/${id}`);
}

/** Lists the workspace durable-context allowlist with derived statuses. */
export async function listDurableContextGrants(): Promise<DurableContextGrantView[]> {
  const res = await api.get<{ grants: DurableContextGrantView[] }>('/durable-context/grants');
  return res.grants ?? [];
}

/**
 * Idempotent soft revoke: the audit row survives and is returned. The
 * response is the bare grant row — view annotations (status/memory_status)
 * belong to the list endpoint only.
 */
export async function revokeDurableContextGrant(id: string): Promise<DurableContextGrant> {
  return api.post<DurableContextGrant>(`/durable-context/grants/${id}/revoke`);
}

/** True when the caller lacks the admin scope these endpoints require. */
export function isForbiddenError(error: unknown): boolean {
  return error instanceof ApiException && error.status === 403;
}

/** Human-readable failure text for banners. */
export function permissionErrorText(error: unknown): string {
  if (error instanceof ApiException) {
    return error.hint ? `${error.message}: ${error.hint}` : error.message;
  }
  if (error instanceof Error) return error.message;
  return String(error);
}
