import { api } from './api';
import { memoryActionKey } from './memory-idempotency';
import type {
  AgentMemory,
  AgentMemorySummary,
  CreateMemoryRelationRequest,
  ListMemoriesResponse,
  ListMemoryRelationsResponse,
  MemoryFeedbackAction,
  MemoryForgetResponse,
  MemoryForgetReason,
  MemoryKind,
  MemoryLifecycleFilter,
  MemoryMutationResponse,
  MemoryRelation,
  MemoryRelationType,
} from './types';

export interface ListMemoriesParams {
  scope?: string;
  kind?: MemoryKind;
  lifecycle?: MemoryLifecycleFilter;
  pinned?: boolean;
  limit?: number;
  cursor?: string;
}

export interface VersionedMemoryAction {
  memoryID: string;
  expectedVersion: number;
}

export interface FeedbackMemoryInput extends VersionedMemoryAction {
  action: MemoryFeedbackAction;
}

export interface ForgetMemoryInput extends VersionedMemoryAction {
  reason: MemoryForgetReason;
}

interface AuthenticatedAction {
  actorID: string;
}

function memoryPath(memoryID: string): string {
  return `/memories/${encodeURIComponent(memoryID)}`;
}

function normalizeObject(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

/**
 * Older servers do not carry control-plane fields yet. Keeping defaults here
 * lets the read-only ledger render during a rolling upgrade while mutations
 * remain capability-gated.
 */
export function normalizeMemory(raw: AgentMemory): AgentMemory {
  const sourceLocator = normalizeObject(raw.source_locator);
  return {
    ...raw,
    attributes: normalizeObject(raw.attributes),
    source_locator: sourceLocator,
    lifecycle_status: raw.lifecycle_status ?? 'active',
    state_version: raw.state_version ?? 1,
    pinned: raw.pinned ?? false,
    useful_count: raw.useful_count ?? 0,
    not_useful_count: raw.not_useful_count ?? 0,
    feedback_score: raw.feedback_score ?? 0,
    feedback_count: raw.feedback_count ?? 0,
    citation: raw.citation || `mem://memories/${raw.id}`,
    provenance: {
      workspace_id: raw.provenance?.workspace_id ?? raw.workspace_id,
      created_by_user_id: raw.provenance?.created_by_user_id ?? raw.created_by_user_id,
      event_at: raw.provenance?.event_at ?? raw.event_at,
      source_type: raw.provenance?.source_type ?? raw.source_type,
      source_ref: raw.provenance?.source_ref ?? raw.source_ref,
      source_file_id: raw.provenance?.source_file_id ?? raw.source_file_id,
      source_file_sha256: raw.provenance?.source_file_sha256 ?? raw.source_file_sha256,
      source_locator: normalizeObject(raw.provenance?.source_locator ?? sourceLocator),
      producer_agent: raw.provenance?.producer_agent ?? raw.producer_agent,
      producer_session: raw.provenance?.producer_session ?? raw.producer_session,
      producer_task: raw.provenance?.producer_task ?? raw.producer_task,
    },
  };
}

export function normalizeMemorySummary(raw: AgentMemorySummary): AgentMemorySummary {
  return {
    ...raw,
    excerpt: raw.excerpt ?? '',
    content_length: raw.content_length ?? Array.from(raw.excerpt ?? '').length,
    lifecycle_status: raw.lifecycle_status ?? 'active',
    state_version: raw.state_version ?? 1,
    superseded: raw.superseded ?? false,
    pinned: raw.pinned ?? false,
    useful_count: raw.useful_count ?? 0,
    not_useful_count: raw.not_useful_count ?? 0,
    feedback_score: raw.feedback_score ?? 0,
    feedback_count: raw.feedback_count ?? 0,
    citation: raw.citation || `mem://memories/${raw.id}`,
  };
}

export async function listMemories(params: ListMemoriesParams = {}): Promise<ListMemoriesResponse> {
  const response = await api.get<ListMemoriesResponse>('/memories', {
    query: {
      scope: params.scope,
      kind: params.kind,
      lifecycle: params.lifecycle,
      pinned: params.pinned,
      limit: params.limit,
      cursor: params.cursor,
    },
  });
  return {
    ...response,
    memories: (response.memories ?? []).map(normalizeMemorySummary),
  };
}

export async function getMemory(memoryID: string): Promise<AgentMemory> {
  return normalizeMemory(await api.get<AgentMemory>(memoryPath(memoryID)));
}

export function feedbackMemory(
  input: FeedbackMemoryInput & AuthenticatedAction,
): Promise<MemoryMutationResponse> {
  return api.post<MemoryMutationResponse>(
    `${memoryPath(input.memoryID)}/feedback`,
    {
      action: input.action,
      expected_version: input.expectedVersion,
    },
    {
      headers: {
        'Idempotency-Key': memoryActionKey(
          input.actorID,
          input.memoryID,
          input.expectedVersion,
          `feedback-${input.action}`,
        ),
      },
    },
  );
}

export function archiveMemory(
  input: VersionedMemoryAction & AuthenticatedAction,
): Promise<MemoryMutationResponse> {
  return api.post<MemoryMutationResponse>(
    `${memoryPath(input.memoryID)}/archive`,
    { expected_version: input.expectedVersion },
    {
      headers: {
        'Idempotency-Key': memoryActionKey(
          input.actorID,
          input.memoryID,
          input.expectedVersion,
          'archive',
        ),
      },
    },
  );
}

export function restoreMemory(
  input: VersionedMemoryAction & AuthenticatedAction,
): Promise<MemoryMutationResponse> {
  return api.post<MemoryMutationResponse>(
    `${memoryPath(input.memoryID)}/restore`,
    { expected_version: input.expectedVersion },
    {
      headers: {
        'Idempotency-Key': memoryActionKey(
          input.actorID,
          input.memoryID,
          input.expectedVersion,
          'restore',
        ),
      },
    },
  );
}

export function forgetMemory(
  input: ForgetMemoryInput & AuthenticatedAction,
): Promise<MemoryForgetResponse> {
  return api.post<MemoryForgetResponse>(
    `${memoryPath(input.memoryID)}/forget`,
    {
      expected_version: input.expectedVersion,
      reason: input.reason,
    },
    {
      headers: {
        'Idempotency-Key': memoryActionKey(
          input.actorID,
          input.memoryID,
          input.expectedVersion,
          `forget-${input.reason}`,
        ),
      },
    },
  );
}

export interface ListMemoryRelationsParams {
  direction?: 'source' | 'target';
  relationType?: MemoryRelationType;
  limit?: number;
}

export async function listMemoryRelations(
  memoryID: string,
  params: ListMemoryRelationsParams = {},
): Promise<MemoryRelation[]> {
  const response = await api.get<ListMemoryRelationsResponse>(
    `${memoryPath(memoryID)}/relations`,
    {
      query: {
        direction: params.direction,
        relation_type: params.relationType,
        limit: params.limit,
      },
    },
  );
  return response.relations ?? [];
}

/**
 * Writes one immutable relation edge. The backend de-duplicates on
 * (workspace, source, target, type), so retries are safe without an
 * Idempotency-Key header.
 */
export function createMemoryRelation(
  request: CreateMemoryRelationRequest,
): Promise<MemoryRelation> {
  return api.post<MemoryRelation>('/memory-relations', {
    source_id: request.source_id,
    target_id: request.target_id,
    relation_type: request.relation_type,
    ...(request.reason ? { reason: request.reason } : {}),
  });
}
