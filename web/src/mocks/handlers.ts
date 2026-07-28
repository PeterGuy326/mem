import { http, HttpResponse, delay } from 'msw';
import { FILES, GHOST_FOLDERS, findFile, relatedFor, searchFiles, ENTITIES } from './fixtures';
import { MEMORY_FIXTURES } from './memoryFixtures';
import type {
  AgentMemory,
  AgentMemoryRecord,
  AgentMemorySummary,
  Capabilities,
  MemFile,
  IndexStatus,
  MemoryEvent,
  MemoryFeedbackAction,
  MemoryForgetReason,
} from '@/lib/types';
import {
  ROOT_PATH,
  basename,
  buildTree,
  isDescendant,
  joinPath,
  normalizePath,
  parentPath,
} from '@/lib/folder-tree';
import {
  MOCK_CAPABILITIES,
  MOCK_CHECKPOINTS_BY_TASK,
  MOCK_TASKS,
  MOCK_WORKSPACE,
  mockResume,
} from './handoff-fixtures';
import { WORKSPACE_BUNDLE_MEDIA_TYPE } from '@/lib/workspace-transfer';

const BASE = '/v1';

function jitter(min = 120, max = 320) {
  return delay(min + Math.random() * (max - min));
}

function inferKind(mime: string, name: string): MemFile['kind'] {
  if (mime.startsWith('image/')) return 'image';
  if (mime.startsWith('audio/')) return 'audio';
  if (mime.startsWith('video/')) return 'video';
  if (mime === 'application/pdf' || name.endsWith('.pdf')) return 'pdf';
  if (mime.startsWith('text/') || /\.(md|txt|json|log|ya?ml)$/i.test(name)) return 'text';
  if (/\.(docx?|xlsx?|pptx?)$/i.test(name)) return 'doc';
  return 'other';
}

function listByPath(path: string): MemFile[] {
  const target = normalizePath(path);
  return FILES.filter((f) => parentPath(f.path) === target);
}

const MOCK_MEMORIES: AgentMemory[] = MEMORY_FIXTURES.map((memory) => ({
  ...memory,
  attributes: { ...memory.attributes },
  source_locator: { ...memory.source_locator },
  provenance: {
    ...memory.provenance,
    source_locator: { ...memory.provenance.source_locator },
  },
}));
const FORGOTTEN_MEMORY_IDS = new Set<string>();
const MEMORY_ACTION_REPLAYS = new Map<
  string,
  { signature: string; result: Record<string, unknown> }
>();
const MOCK_WORKSPACE_BUNDLE_TEXT =
  'PK\u0003\u0004MEM.WORKSPACE_BUNDLE.V1\nmanifest.json\nchecksums.sha256\n';
const IMPORTED_WORKSPACE_BUNDLES = new Set<string>();

function mockToken(request: Request): string {
  return request.headers.get('Authorization')?.replace(/^Bearer\s+/i, '') ?? '';
}

function memorySummary(memory: AgentMemory): AgentMemorySummary {
  const runes = Array.from(memory.content);
  return {
    id: memory.id,
    workspace_id: memory.workspace_id,
    kind: memory.kind,
    excerpt: runes.slice(0, 500).join(''),
    content_length: runes.length,
    path: memory.path,
    event_at: memory.event_at,
    source_type: memory.source_type,
    source_ref: memory.source_ref,
    source_file_id: memory.source_file_id,
    source_file_sha256: memory.source_file_sha256,
    producer_agent: memory.producer_agent,
    producer_session: memory.producer_session,
    producer_task: memory.producer_task,
    content_sha256: memory.content_sha256,
    lifecycle_status: memory.lifecycle_status,
    state_version: memory.state_version,
    pinned: memory.pinned,
    pinned_at: memory.pinned_at,
    useful_count: memory.useful_count,
    not_useful_count: memory.not_useful_count,
    feedback_score: memory.feedback_score,
    feedback_count: memory.feedback_count,
    feedback_at: memory.feedback_at,
    citation: memory.citation,
    created_at: memory.created_at,
    updated_at: memory.updated_at,
  };
}

function memoryRecord(memory: AgentMemory): AgentMemoryRecord {
  const { citation: _citation, provenance: _provenance, ...record } = memory;
  return structuredClone(record);
}

interface MockMemoryCursor {
  v: 1;
  created_at: string;
  id: string;
  filter: string;
}

function encodeBase64URL(value: string): string {
  const bytes = new TextEncoder().encode(value);
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
}

function decodeBase64URL(value: string): string {
  const padded = value.replaceAll('-', '+').replaceAll('_', '/');
  const binary = atob(padded + '='.repeat((4 - (padded.length % 4)) % 4));
  return new TextDecoder().decode(Uint8Array.from(binary, (char) => char.charCodeAt(0)));
}

function encodeMemoryCursor(memory: AgentMemory, filter: string): string {
  return encodeBase64URL(
    JSON.stringify({
      v: 1,
      created_at: memory.created_at,
      id: memory.id,
      filter,
    } satisfies MockMemoryCursor),
  );
}

function decodeMemoryCursor(cursor: string | null, filter: string): MockMemoryCursor | null {
  if (!cursor) return null;
  try {
    const parsed = JSON.parse(decodeBase64URL(cursor)) as Partial<MockMemoryCursor>;
    if (
      parsed.v !== 1 ||
      typeof parsed.created_at !== 'string' ||
      typeof parsed.id !== 'string' ||
      parsed.filter !== filter
    ) {
      return null;
    }
    return parsed as MockMemoryCursor;
  } catch {
    return null;
  }
}

function memoryAt(id: string): AgentMemory | undefined {
  return MOCK_MEMORIES.find((memory) => memory.id === id);
}

function pathInScope(path: string, scope: string): boolean {
  if (!scope || scope === '/') return true;
  const normalized = scope.length > 1 ? scope.replace(/\/+$/, '') : scope;
  return path === normalized || path.startsWith(`${normalized}/`);
}

function mockPermissions(request: Request): Capabilities['permissions'] {
  const token = mockToken(request);
  const permissions = { ...MOCK_CAPABILITIES.permissions };
  if (token === 'mock-no-read') {
    return {
      ...permissions,
      read: false,
      search: false,
      write: false,
      delete: false,
      provider_read: false,
      provider_modify: false,
      workspace_export: false,
      workspace_import: false,
    };
  }
  if (token === 'mock-readonly') {
    return {
      ...permissions,
      write: false,
      delete: false,
      provider_modify: false,
      workspace_export: false,
      workspace_import: false,
    };
  }
  if (token === 'mock-no-delete') {
    return { ...permissions, delete: false };
  }
  if (token === 'mock-transfer-readonly') {
    return {
      ...permissions,
      workspace_export: false,
      workspace_import: false,
    };
  }
  return permissions;
}

function mockForbidden() {
  return HttpResponse.json(
    { error: 'forbidden', hint: 'token scope does not permit this memory operation' },
    { status: 403 },
  );
}

async function applyMockMemoryAction(
  request: Request,
  memoryID: string,
  action: string,
  mutate: (memory: AgentMemory, body: Record<string, unknown>) => void,
  options: {
    permission?: 'write' | 'delete';
    validate?: (body: Record<string, unknown>) => string | undefined;
    transition?: (memory: AgentMemory, body: Record<string, unknown>) => string | undefined;
    result?: (memory: AgentMemory) => Record<string, unknown>;
  } = {},
) {
  const permissions = mockPermissions(request);
  if (options.permission === 'delete') {
    if (!permissions.delete) return mockForbidden();
  } else if (!permissions.read || !permissions.write) {
    return mockForbidden();
  }
  const key = request.headers.get('Idempotency-Key')?.trim();
  if (!key) {
    return HttpResponse.json(
      { error: 'missing_idempotency_key', hint: 'Idempotency-Key is required' },
      { status: 400 },
    );
  }
  const body = (await request.json().catch(() => ({}))) as Record<string, unknown>;
  const validationError = options.validate?.(body);
  if (validationError) {
    return HttpResponse.json({ error: 'invalid_request', hint: validationError }, { status: 400 });
  }
  const signature = `${request.url}\n${JSON.stringify(body)}`;
  const replay = MEMORY_ACTION_REPLAYS.get(key);
  if (replay) {
    if (replay.signature !== signature) {
      return HttpResponse.json(
        { error: 'idempotency_conflict', hint: 'key was used with another request' },
        { status: 409 },
      );
    }
    return HttpResponse.json(
      { ...structuredClone(replay.result), replayed: true },
      { status: 200 },
    );
  }
  const memory = memoryAt(memoryID);
  if (FORGOTTEN_MEMORY_IDS.has(memoryID)) {
    return HttpResponse.json(
      { error: 'memory_forgotten', hint: 'the memory has been forgotten' },
      { status: 410 },
    );
  }
  if (!memory) {
    return HttpResponse.json({ error: 'not_found', hint: 'no such memory' }, { status: 404 });
  }
  if (Number(body.expected_version) !== memory.state_version) {
    return HttpResponse.json(
      {
        error: 'memory_version_conflict',
        hint: 'memory changed since it was read; reload it and retry with a new key',
      },
      { status: 409 },
    );
  }
  const transitionError = options.transition?.(memory, body);
  if (transitionError) {
    return HttpResponse.json(
      { error: 'invalid_memory_transition', hint: transitionError },
      { status: 409 },
    );
  }
  const expectedVersion = memory.state_version;
  mutate(memory, body);
  memory.state_version += 1;
  memory.updated_at = new Date().toISOString();
  const effectiveAction =
    action === 'feedback' ? (String(body.action) as MemoryFeedbackAction) : action;
  const event: MemoryEvent = {
    id: crypto.randomUUID(),
    workspace_id: memory.workspace_id,
    memory_id: memory.id,
    action: effectiveAction as MemoryEvent['action'],
    actor_user_id: memory.created_by_user_id,
    expected_version: expectedVersion,
    resulting_version: memory.state_version,
    ...(effectiveAction === 'forget' ? { reason: String(body.reason) as MemoryForgetReason } : {}),
    created_at: memory.updated_at,
  };
  const result = {
    ...(options.result?.(memory) ?? { memory: memoryRecord(memory) }),
    event,
    replayed: false,
  };
  MEMORY_ACTION_REPLAYS.set(key, { signature, result: structuredClone(result) });
  return HttpResponse.json(result, { status: 201 });
}

export const handlers = [
  // ----- Auth -----
  http.post(`${BASE}/auth/login`, async ({ request }) => {
    await jitter();
    const body = (await request.json().catch(() => ({}))) as { email?: string };
    return HttpResponse.json({
      token: 'mock-token-' + Date.now().toString(36),
      user: {
        id: 'user-1',
        email: body?.email ?? 'demo@mem.dev',
        created_at: '2024-01-01T00:00:00Z',
      },
    });
  }),

  // ----- Workspace / deployment capabilities -----
  http.get(`${BASE}/capabilities`, async ({ request }) => {
    await jitter(50, 120);
    if (mockToken(request) === 'mock-transfer-unsupported') {
      return HttpResponse.json({
        ...MOCK_CAPABILITIES,
        features: {
          ...MOCK_CAPABILITIES.features,
          workspace_export: false,
          workspace_import: false,
        },
        permissions: {
          ...mockPermissions(request),
          workspace_export: false,
          workspace_import: false,
        },
        workspace_restore_modes: [],
        workspace_bundle_schema_versions: [],
      });
    }
    return HttpResponse.json({
      ...MOCK_CAPABILITIES,
      features: { ...MOCK_CAPABILITIES.features },
      permissions: mockPermissions(request),
      workspace_restore_modes: [...MOCK_CAPABILITIES.workspace_restore_modes],
      workspace_bundle_schema_versions: [...MOCK_CAPABILITIES.workspace_bundle_schema_versions],
    });
  }),
  http.get(`${BASE}/workspaces`, async () => {
    await jitter(50, 120);
    return HttpResponse.json({ workspaces: [MOCK_WORKSPACE] });
  }),
  http.get(`${BASE}/workspaces/current`, async () => {
    await jitter(50, 120);
    return HttpResponse.json(MOCK_WORKSPACE);
  }),

  // ----- Portable workspace transfer -----
  http.get(`${BASE}/workspaces/current/export`, async ({ request }) => {
    await delay(80);
    if (!mockPermissions(request).workspace_export) {
      return HttpResponse.json(
        {
          error: 'forbidden',
          hint: 'workspace export requires an unrestricted owner/admin token',
        },
        { status: 403 },
      );
    }
    const token = mockToken(request);
    if (token === 'mock-transfer-export-500') {
      return HttpResponse.json(
        {
          error: 'workspace_transfer_failed',
          hint: 'workspace transfer failed; check server logs',
        },
        { status: 500 },
      );
    }
    const headers: Record<string, string> = {
      'Cache-Control': 'no-store',
      'Content-Disposition':
        token === 'mock-transfer-hostile-name'
          ? "attachment; filename*=UTF-8''..%2F..%2F%3Cscript%3Eworkspace%3C%2Fscript%3E.membundle"
          : 'attachment; filename="workspace-agent-drive-demo.membundle"',
      'Content-Type':
        token === 'mock-transfer-bad-mime' ? 'application/json' : WORKSPACE_BUNDLE_MEDIA_TYPE,
      'X-Content-Type-Options': 'nosniff',
    };
    if (token === 'mock-transfer-no-filename') {
      delete headers['Content-Disposition'];
    }
    return new HttpResponse(MOCK_WORKSPACE_BUNDLE_TEXT, { headers });
  }),

  http.post(`${BASE}/workspaces/current/import`, async ({ request }) => {
    await delay(100);
    if (!mockPermissions(request).workspace_import) {
      return HttpResponse.json(
        {
          error: 'forbidden',
          hint: 'workspace import requires an unrestricted owner/admin token',
        },
        { status: 403 },
      );
    }
    const url = new URL(request.url);
    if (url.searchParams.getAll('mode').length !== 1 || url.searchParams.get('mode') !== 'fresh') {
      return HttpResponse.json(
        { error: 'unsupported_restore_mode', hint: 'mode must be fresh' },
        { status: 422 },
      );
    }
    if (request.headers.get('Content-Type') !== WORKSPACE_BUNDLE_MEDIA_TYPE) {
      return HttpResponse.json(
        {
          error: 'unsupported_media_type',
          hint: `Content-Type must be ${WORKSPACE_BUNDLE_MEDIA_TYPE}`,
        },
        { status: 415 },
      );
    }
    const body = new TextDecoder().decode(await request.arrayBuffer());
    if (!body) {
      return HttpResponse.json(
        {
          error: 'invalid_workspace_bundle',
          hint: 'workspace bundle request body is empty',
        },
        { status: 400 },
      );
    }
    const key = `${request.headers.get('X-Workspace-ID') ?? MOCK_WORKSPACE.id}\n${body}`;
    if (body.includes('MOCK_IMPORT_NETWORK_ERROR')) {
      return HttpResponse.error();
    }
    if (body.includes('MOCK_IMPORT_TOO_LARGE')) {
      return HttpResponse.json(
        {
          error: 'workspace_bundle_too_large',
          hint: 'workspace bundle exceeds the server upload limit',
        },
        { status: 413 },
      );
    }
    if (body.includes('MOCK_IMPORT_INVALID')) {
      return HttpResponse.json(
        {
          error: 'invalid_workspace_bundle',
          hint: 'workspace bundle failed validation',
        },
        { status: 400 },
      );
    }
    if (body.includes('MOCK_IMPORT_UNSUPPORTED')) {
      return HttpResponse.json(
        {
          error: 'unsupported_workspace_bundle',
          hint: 'workspace bundle version or restore mode is not supported',
        },
        { status: 422 },
      );
    }
    if (body.includes('MOCK_IMPORT_RATE_LIMIT')) {
      return HttpResponse.json(
        {
          error: 'workspace_transfer_busy',
          hint: 'another transfer is already running; retry later',
        },
        { status: 429 },
      );
    }
    if (body.includes('MOCK_IMPORT_STORAGE_FULL')) {
      return HttpResponse.json(
        {
          error: 'workspace_storage_exhausted',
          hint: 'the target storage does not have enough capacity',
        },
        { status: 507 },
      );
    }
    if (body.includes('MOCK_IMPORT_SERVER_ERROR')) {
      return HttpResponse.json(
        {
          error: 'workspace_transfer_failed',
          hint: 'workspace transfer failed; check server logs',
        },
        { status: 500 },
      );
    }
    if (
      body.includes('MOCK_IMPORT_COMMIT_INDETERMINATE') &&
      !IMPORTED_WORKSPACE_BUNDLES.has(key)
    ) {
      // Simulate a commit that succeeded while its acknowledgement was lost.
      // Only an exact replay can prove the durable ledger entry.
      IMPORTED_WORKSPACE_BUNDLES.add(key);
      return HttpResponse.json(
        {
          error: 'workspace_import_commit_indeterminate',
          hint: 'uploaded objects were preserved; retry the exact same bundle',
        },
        { status: 503 },
      );
    }
    if (body.includes('MOCK_IMPORT_CONFLICT')) {
      return HttpResponse.json(
        {
          error: 'workspace_import_conflict',
          hint: 'target workspace conflicts with this fresh import',
          total: 202,
          truncated: true,
          conflicts: [
            {
              kind: 'existing_resource',
              resource: '<img src=x onerror=alert("conflict-xss")>',
              value: '/Projects/<script>alert("never")</script>',
            },
            {
              kind: 'workspace_not_empty',
              resource: 'files',
              value: '1',
            },
          ],
        },
        { status: 409 },
      );
    }

    const replayed = IMPORTED_WORKSPACE_BUNDLES.has(key);
    IMPORTED_WORKSPACE_BUNDLES.add(key);
    return HttpResponse.json({
      bundle_id: '77777777-7777-4777-8777-777777777777',
      archive_sha256: 'c'.repeat(64),
      source_workspace_id: '99999999-9999-4999-8999-999999999999',
      imported_at: '2026-07-28T07:08:09Z',
      counts: {
        folders: 4,
        files: 12,
        memories: 7,
        memory_events: 19,
        tasks: 2,
        checkpoints: 5,
        checkpoint_refs: 6,
        checkpoint_payloads: 5,
        blobs: 11,
        blob_bytes: 3145728,
      },
      replayed,
    });
  }),

  // ----- Structured Agent memory ledger -----
  http.get(`${BASE}/memories`, async ({ request }) => {
    await delay(60);
    if (!mockPermissions(request).read) return mockForbidden();
    const url = new URL(request.url);
    const scope = url.searchParams.get('scope') ?? '';
    const kinds = url.searchParams
      .getAll('kind')
      .flatMap((value) => value.split(','))
      .map((value) => value.trim())
      .filter(Boolean)
      .sort();
    const lifecycleParam = url.searchParams.get('lifecycle');
    const legacyLifecycle = url.searchParams.get('lifecycle_status');
    if (lifecycleParam && legacyLifecycle && lifecycleParam !== legacyLifecycle) {
      return HttpResponse.json(
        {
          error: 'bad_lifecycle',
          hint: 'lifecycle and lifecycle_status must not conflict',
        },
        { status: 400 },
      );
    }
    const lifecycle = lifecycleParam ?? legacyLifecycle ?? 'active';
    if (!['active', 'archived', 'all'].includes(lifecycle)) {
      return HttpResponse.json(
        {
          error: 'bad_lifecycle',
          hint: 'lifecycle must be active, archived, or all',
        },
        { status: 400 },
      );
    }
    const pinned = url.searchParams.get('pinned');
    if (pinned !== null && pinned !== 'true' && pinned !== 'false') {
      return HttpResponse.json(
        { error: 'bad_pinned', hint: 'pinned must be true or false' },
        { status: 400 },
      );
    }
    const recursiveParam = url.searchParams.get('recursive');
    if (recursiveParam !== null && recursiveParam !== 'true' && recursiveParam !== 'false') {
      return HttpResponse.json(
        { error: 'bad_recursive', hint: 'recursive must be true or false' },
        { status: 400 },
      );
    }
    const recursive = recursiveParam !== 'false';
    const rawLimit = Number(url.searchParams.get('limit') ?? 50);
    if (!Number.isInteger(rawLimit) || rawLimit < 1 || rawLimit > 100) {
      return HttpResponse.json(
        { error: 'bad_limit', hint: 'limit must be between 1 and 100' },
        { status: 400 },
      );
    }
    const filter = JSON.stringify({
      workspace: request.headers.get('X-Workspace-ID') ?? MOCK_WORKSPACE.id,
      scope: scope || '/',
      recursive,
      kinds,
      lifecycle,
      pinned: pinned ?? 'any',
    });
    const rawCursor = url.searchParams.get('cursor');
    const cursor = decodeMemoryCursor(rawCursor, filter);
    if (rawCursor && !cursor) {
      return HttpResponse.json(
        { error: 'invalid_memory_query', hint: 'invalid or filter-mismatched cursor' },
        { status: 400 },
      );
    }
    const filtered = MOCK_MEMORIES.filter((memory) => !FORGOTTEN_MEMORY_IDS.has(memory.id))
      .filter((memory) =>
        recursive ? pathInScope(memory.path, scope) : memory.path === (scope || '/'),
      )
      .filter((memory) => kinds.length === 0 || kinds.includes(memory.kind))
      .filter((memory) => lifecycle === 'all' || memory.lifecycle_status === lifecycle)
      .filter((memory) => pinned === null || memory.pinned === (pinned === 'true'))
      .sort((left, right) =>
        left.created_at === right.created_at
          ? right.id.localeCompare(left.id)
          : right.created_at.localeCompare(left.created_at),
      )
      .filter(
        (memory) =>
          !cursor ||
          memory.created_at < cursor.created_at ||
          (memory.created_at === cursor.created_at && memory.id < cursor.id),
      );
    const page = filtered.slice(0, rawLimit);
    const hasNextPage = filtered.length > rawLimit;
    const last = page.at(-1);
    return HttpResponse.json({
      memories: page.map(memorySummary),
      ...(hasNextPage && last ? { next_cursor: encodeMemoryCursor(last, filter) } : {}),
    });
  }),

  http.get(`${BASE}/memories/:id`, async ({ params, request }) => {
    await delay(40);
    if (!mockPermissions(request).read) return mockForbidden();
    const id = String(params.id);
    if (FORGOTTEN_MEMORY_IDS.has(id)) {
      return HttpResponse.json(
        { error: 'memory_forgotten', hint: 'memory payload was permanently forgotten' },
        { status: 410 },
      );
    }
    const memory = memoryAt(id);
    if (!memory) {
      return HttpResponse.json({ error: 'not_found', hint: 'no such memory' }, { status: 404 });
    }
    return HttpResponse.json(structuredClone(memory));
  }),

  http.post(`${BASE}/memories/:id/feedback`, async ({ params, request }) =>
    applyMockMemoryAction(
      request,
      String(params.id),
      'feedback',
      (memory, body) => {
        const action = String(body.action) as MemoryFeedbackAction;
        if (action === 'pin') {
          memory.pinned = true;
          memory.pinned_at = new Date().toISOString();
        } else if (action === 'unpin') {
          memory.pinned = false;
          memory.pinned_at = null;
        } else if (action === 'useful') {
          memory.useful_count += 1;
          memory.feedback_score += 1;
          memory.feedback_count += 1;
          memory.feedback_at = new Date().toISOString();
        } else if (action === 'not_useful') {
          memory.not_useful_count += 1;
          memory.feedback_score -= 1;
          memory.feedback_count += 1;
          memory.feedback_at = new Date().toISOString();
        }
      },
      {
        validate: (body) =>
          ['useful', 'not_useful', 'pin', 'unpin'].includes(String(body.action))
            ? undefined
            : 'action must be useful, not_useful, pin, or unpin',
        transition: (memory, body) => {
          if (body.action === 'pin' && memory.pinned) return 'memory is already pinned';
          if (body.action === 'unpin' && !memory.pinned) return 'memory is not pinned';
          return undefined;
        },
      },
    ),
  ),

  http.post(`${BASE}/memories/:id/archive`, async ({ params, request }) =>
    applyMockMemoryAction(
      request,
      String(params.id),
      'archive',
      (memory) => {
        memory.lifecycle_status = 'archived';
      },
      {
        transition: (memory) =>
          memory.lifecycle_status === 'active' ? undefined : 'only active memories can be archived',
      },
    ),
  ),

  http.post(`${BASE}/memories/:id/restore`, async ({ params, request }) =>
    applyMockMemoryAction(
      request,
      String(params.id),
      'restore',
      (memory) => {
        memory.lifecycle_status = 'active';
      },
      {
        transition: (memory) =>
          memory.lifecycle_status === 'archived'
            ? undefined
            : 'only archived memories can be restored',
      },
    ),
  ),

  http.post(`${BASE}/memories/:id/forget`, async ({ params, request }) => {
    const id = String(params.id);
    const allowedReasons: MemoryForgetReason[] = [
      'user_request',
      'incorrect',
      'sensitive',
      'expired',
      'other',
    ];
    return applyMockMemoryAction(
      request,
      id,
      'forget',
      (memory) => {
        FORGOTTEN_MEMORY_IDS.add(id);
        memory.content = '';
        memory.attributes = {};
        memory.event_at = undefined;
        memory.source_type = 'forgotten';
        memory.source_ref = '';
        memory.source_file_id = undefined;
        memory.source_file_sha256 = '';
        memory.source_locator = {};
        memory.content_sha256 = '0'.repeat(64);
        memory.pinned = false;
        memory.pinned_at = null;
        memory.useful_count = 0;
        memory.not_useful_count = 0;
        memory.feedback_score = 0;
        memory.feedback_count = 0;
        memory.feedback_at = null;
        memory.producer_agent = '';
        memory.producer_session = '';
        memory.producer_task = '';
        memory.provenance = {
          workspace_id: memory.workspace_id,
          source_type: 'forgotten',
          source_locator: {},
        };
      },
      {
        permission: 'delete',
        validate: (body) =>
          allowedReasons.includes(String(body.reason) as MemoryForgetReason)
            ? undefined
            : 'reason must be user_request, incorrect, sensitive, expired, or other',
        result: (memory) => ({
          memory_id: memory.id,
          state_version: memory.state_version,
          forgotten_at: memory.updated_at,
        }),
      },
    );
  }),

  // ----- Files: list (by path) -----
  http.get(`${BASE}/files`, async ({ request }) => {
    await jitter();
    const url = new URL(request.url);
    const path = url.searchParams.get('path');
    const limit = Number(url.searchParams.get('limit') ?? 500);
    if (path !== null) {
      const files = listByPath(path).slice(0, limit);
      return HttpResponse.json({ files });
    }
    // Default: latest N globally (unused in explorer, kept for compatibility).
    return HttpResponse.json({ files: FILES.slice(0, limit) });
  }),

  // ----- Folders: tree (memd returns a bare FolderNode) -----
  http.get(`${BASE}/folders/tree`, async () => {
    await jitter(80, 200);
    return HttpResponse.json(buildTree(FILES, Array.from(GHOST_FOLDERS)));
  }),

  // ----- Authenticated file bytes (used by thumbnails and detail previews) -----
  http.get(`${BASE}/files/:id/content`, async ({ params }) => {
    await jitter(30, 80);
    const file = findFile(String(params.id));
    if (!file) {
      return HttpResponse.json({ error: 'not_found', hint: 'no such file' }, { status: 404 });
    }
    if (file.kind === 'image') {
      const label = file.name.replace(/[<>&"']/g, '');
      const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="960" height="640" viewBox="0 0 960 640">
        <defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#1e2440"/><stop offset="1" stop-color="#0e1016"/></linearGradient></defs>
        <rect width="960" height="640" fill="url(#g)"/>
        <circle cx="690" cy="190" r="110" fill="#818cf8" opacity=".22"/>
        <path d="M0 510 220 310l150 145 150-180 440 365H0Z" fill="#818cf8" opacity=".28"/>
        <text x="48" y="574" fill="#e8eaf0" font-family="system-ui,sans-serif" font-size="24">${label}</text>
      </svg>`;
      return new HttpResponse(svg, {
        headers: { 'Content-Type': 'image/svg+xml; charset=utf-8' },
      });
    }
    const text = file.summary ?? file.caption ?? `Mock content for ${file.name}`;
    return new HttpResponse(text, {
      headers: { 'Content-Type': file.mime.startsWith('text/') ? file.mime : 'text/plain' },
    });
  }),

  // ----- Files: detail -----
  http.get(`${BASE}/files/:id`, async ({ params }) => {
    await jitter();
    const file = findFile(String(params.id));
    if (!file) {
      return HttpResponse.json(
        { error: 'file not found', hint: '检查 file_id 是否正确，或文件可能已删除' },
        { status: 404 },
      );
    }
    return HttpResponse.json(file);
  }),

  // ----- Files: related (memd: { related: [flat hit] }) -----
  http.get(`${BASE}/files/:id/related`, async ({ params }) => {
    await jitter();
    const related = relatedFor(String(params.id)).map((r) => ({
      file_id: r.file.id,
      name: r.file.name,
      path: r.file.path,
      mime: r.file.mime,
      type: r.relation,
      score: r.score,
      summary: r.file.summary,
    }));
    return HttpResponse.json({ related });
  }),

  // ----- Files: patch (move / rename) -----
  http.patch(`${BASE}/files/:id`, async ({ params, request }) => {
    await delay(150 + Math.random() * 150);
    const file = findFile(String(params.id));
    if (!file) {
      return HttpResponse.json({ error: 'file not found' }, { status: 404 });
    }
    const body = (await request.json().catch(() => ({}))) as { path?: string; name?: string };
    if (body.name) {
      file.name = body.name;
      file.path = joinPath(parentPath(file.path), body.name);
    }
    if (body.path) {
      // body.path is the new full path (folder path); preserve filename.
      const targetFolder = normalizePath(body.path);
      file.path = joinPath(targetFolder, file.name);
    }
    file.updated_at = new Date().toISOString();
    return HttpResponse.json(file);
  }),

  // ----- Files: delete -----
  http.delete(`${BASE}/files/:id`, async ({ params }) => {
    await jitter();
    const idx = FILES.findIndex((f) => f.id === String(params.id));
    if (idx >= 0) FILES.splice(idx, 1);
    return HttpResponse.json({ ok: true });
  }),

  // ----- Files: upload (multipart) -----
  http.post(`${BASE}/files`, async ({ request }) => {
    await delay(600 + Math.random() * 600);
    const form = await request.formData();
    const file = form.get('file');
    const nameOverride = String(form.get('name') ?? '');
    const targetPath = normalizePath(String(form.get('path') ?? ROOT_PATH));

    if (!(file instanceof File)) {
      return HttpResponse.json(
        { error: 'missing file', hint: '请通过 multipart 上传文件' },
        { status: 400 },
      );
    }

    const name = nameOverride || file.name;
    const mime = file.type || 'application/octet-stream';
    const kind = inferKind(mime, name);
    const id = 'upl-' + Math.random().toString(36).slice(2, 10);
    const now = new Date().toISOString();
    const previewUrl =
      kind === 'image' ? `https://picsum.photos/seed/${encodeURIComponent(id)}/1200/900` : null;
    const status: IndexStatus = 'processing';

    const created: MemFile = {
      id,
      user_id: 'user-1',
      name,
      path: joinPath(targetPath, name),
      size: file.size,
      sha256: id.padEnd(64, '0'),
      mime,
      storage_key: `s3://mem/inbox/${id}`,
      summary: null,
      caption: null,
      tags: [],
      timeline_at: now,
      geo: null,
      index_status: status,
      created_at: now,
      updated_at: now,
      kind,
      preview_url: previewUrl,
      thumbnail_url: previewUrl,
      download_url: previewUrl,
      entities: [],
    };
    FILES.unshift(created);

    // Simulate AI pipeline: flip to done.
    setTimeout(() => {
      const f = findFile(id);
      if (!f) return;
      f.index_status = 'done';
      if (f.kind === 'image') {
        f.caption = '刚上传的照片 — AI 已生成 caption 占位';
        f.tags = ['新上传'];
      } else {
        f.summary = '刚上传的文件 — AI 摘要占位';
        f.tags = ['新上传'];
      }
      f.updated_at = new Date().toISOString();
    }, 4000);

    return HttpResponse.json(created, { status: 201 });
  }),

  // ----- Folders: create -----
  http.post(`${BASE}/folders`, async ({ request }) => {
    await jitter(80, 200);
    const body = (await request.json().catch(() => ({}))) as { path?: string; name?: string };
    if (!body.path || !body.name) {
      return HttpResponse.json(
        { error: 'missing path or name', hint: '需要 path（父目录）和 name（新文件夹名）' },
        { status: 400 },
      );
    }
    const full = joinPath(normalizePath(body.path), body.name);
    GHOST_FOLDERS.add(full);
    return HttpResponse.json({ path: full, name: body.name });
  }),

  // ----- Folders: rename -----
  http.patch(`${BASE}/folders`, async ({ request }) => {
    await delay(150 + Math.random() * 150);
    const body = (await request.json().catch(() => ({}))) as { path?: string; name?: string };
    if (!body.path || !body.name) {
      return HttpResponse.json({ error: 'missing fields' }, { status: 400 });
    }
    const oldPath = normalizePath(body.path);
    const newPath = joinPath(parentPath(oldPath), body.name);
    // Rewrite descendants of old folder
    for (const f of FILES) {
      if (isDescendant(f.path, oldPath) && f.path !== oldPath) {
        f.path = newPath + f.path.slice(oldPath.length);
        f.updated_at = new Date().toISOString();
      }
    }
    // Move any ghost folders too
    const ghosts = Array.from(GHOST_FOLDERS);
    GHOST_FOLDERS.clear();
    for (const g of ghosts) {
      if (g === oldPath || isDescendant(g, oldPath)) {
        GHOST_FOLDERS.add(newPath + g.slice(oldPath.length));
      } else {
        GHOST_FOLDERS.add(g);
      }
    }
    return HttpResponse.json({ ok: true, old_path: oldPath, new_path: newPath });
  }),

  // ----- Folders: move (change parent) -----
  http.put(`${BASE}/folders`, async ({ request }) => {
    await delay(150 + Math.random() * 150);
    const body = (await request.json().catch(() => ({}))) as { path?: string; new_parent?: string };
    if (!body.path || !body.new_parent) {
      return HttpResponse.json({ error: 'missing fields' }, { status: 400 });
    }
    const oldPath = normalizePath(body.path);
    const newPath = joinPath(normalizePath(body.new_parent), basename(oldPath));
    if (isDescendant(newPath, oldPath)) {
      return HttpResponse.json(
        { error: 'cannot move a folder into itself', hint: '目标路径不能是源路径的子目录' },
        { status: 400 },
      );
    }
    for (const f of FILES) {
      if (isDescendant(f.path, oldPath)) {
        f.path = newPath + f.path.slice(oldPath.length);
        f.updated_at = new Date().toISOString();
      }
    }
    const ghosts = Array.from(GHOST_FOLDERS);
    GHOST_FOLDERS.clear();
    for (const g of ghosts) {
      if (g === oldPath || isDescendant(g, oldPath)) {
        GHOST_FOLDERS.add(newPath + g.slice(oldPath.length));
      } else {
        GHOST_FOLDERS.add(g);
      }
    }
    return HttpResponse.json({ ok: true, old_path: oldPath, new_path: newPath });
  }),

  // ----- Folders: delete -----
  http.delete(`${BASE}/folders`, async ({ request }) => {
    await jitter();
    const url = new URL(request.url);
    const path = normalizePath(url.searchParams.get('path') ?? '');
    if (path === ROOT_PATH) {
      return HttpResponse.json({ error: 'cannot delete root' }, { status: 400 });
    }
    // Remove all files inside
    for (let i = FILES.length - 1; i >= 0; i--) {
      if (isDescendant(FILES[i]!.path, path)) FILES.splice(i, 1);
    }
    const ghosts = Array.from(GHOST_FOLDERS);
    GHOST_FOLDERS.clear();
    for (const g of ghosts) {
      if (!(g === path || isDescendant(g, path))) GHOST_FOLDERS.add(g);
    }
    return HttpResponse.json({ ok: true });
  }),

  // ----- Search (memd: POST with JSON body, flat hit shape) -----
  http.post(`${BASE}/search`, async ({ request }) => {
    await jitter();
    const body = (await request.json().catch(() => ({}))) as {
      query?: string;
      type?: string;
      since?: string;
      until?: string;
      limit?: number;
    };
    const q = body.query ?? '';
    const { results } = searchFiles({
      q,
      type: body.type,
      since: body.since,
      until: body.until,
      limit: body.limit ?? 30,
    });
    return HttpResponse.json({
      results: results.map((r) => ({
        evidence_id: `${r.channel}:${r.file.id}`,
        file_id: r.file.id,
        name: r.file.name,
        path: r.file.path,
        mime: r.file.mime,
        score: r.score,
        snippet: r.snippet,
        source: r.channel === 'visual' ? 'visual' : 'text',
        content_sha256: r.file.sha256,
        chunk_index: r.channel === 'visual' ? -1 : 0,
        summary: r.file.summary,
        timeline_at: r.file.timeline_at,
        created_at: r.file.created_at,
      })),
      _meta: { latency_ms: Math.round(120 + Math.random() * 180) },
    });
  }),

  // ----- Portable Agent task ledger -----
  http.get(`${BASE}/tasks`, async ({ request }) => {
    await jitter(80, 180);
    const url = new URL(request.url);
    const scope = url.searchParams.get('scope');
    const limit = Number(url.searchParams.get('limit') ?? 50);
    const tasks = MOCK_TASKS.filter((task) => {
      if (!scope || scope === '/') return true;
      return task.scope_path === scope || task.scope_path.startsWith(`${scope}/`);
    }).slice(0, limit);
    return HttpResponse.json({ tasks });
  }),

  http.get(`${BASE}/tasks/:taskKey/checkpoints`, async ({ params, request }) => {
    await jitter(80, 180);
    const taskKey = String(params.taskKey);
    const checkpoints = MOCK_CHECKPOINTS_BY_TASK[taskKey];
    if (!checkpoints) {
      return HttpResponse.json(
        { error: 'not_found', hint: 'task checkpoint was not found' },
        { status: 404 },
      );
    }
    const url = new URL(request.url);
    const before = Number(url.searchParams.get('before') ?? Number.MAX_SAFE_INTEGER);
    const limit = Number(url.searchParams.get('limit') ?? 50);
    return HttpResponse.json({
      checkpoints: checkpoints
        .filter((candidate) => candidate.sequence < before)
        .slice(0, limit)
        .map((candidate) => {
          const progress = candidate.handoff.state.progress;
          const progressRunes = Array.from(progress.summary);
          return {
            id: candidate.id,
            workspace_id: candidate.workspace_id,
            task_id: candidate.task_id,
            task_key: candidate.task_key,
            sequence: candidate.sequence,
            checkpoint_kind: candidate.checkpoint_kind,
            contract: candidate.contract,
            schema_version: candidate.schema_version,
            base_checkpoint_id: candidate.base_checkpoint_id,
            scope_path: candidate.scope_path,
            status: candidate.handoff.state.status,
            progress_excerpt: progressRunes.slice(0, 500).join(''),
            progress_length: progressRunes.length,
            completed_count: progress.completed.length,
            reference_count: candidate.references.length,
            payload_sha256: candidate.payload_sha256,
            producer_agent: candidate.producer_agent,
            producer_session: candidate.producer_session,
            created_at: candidate.created_at,
          };
        }),
    });
  }),

  http.get(`${BASE}/tasks/:taskKey/checkpoints/:checkpointId`, async ({ params }) => {
    await jitter(80, 180);
    const taskKey = String(params.taskKey);
    const checkpointID = String(params.checkpointId);
    const checkpoint = (MOCK_CHECKPOINTS_BY_TASK[taskKey] ?? []).find(
      (candidate) => candidate.id === checkpointID,
    );
    if (!checkpoint) {
      return HttpResponse.json(
        { error: 'not_found', hint: 'task checkpoint was not found' },
        { status: 404 },
      );
    }
    return HttpResponse.json(checkpoint);
  }),

  http.post(`${BASE}/tasks/:taskKey/resume`, async ({ params, request }) => {
    await jitter(140, 260);
    const body = (await request.json().catch(() => ({}))) as { checkpoint_id?: string };
    const resumed = mockResume(String(params.taskKey), body.checkpoint_id);
    if (!resumed) {
      return HttpResponse.json(
        { error: 'not_found', hint: 'task checkpoint was not found' },
        { status: 404 },
      );
    }
    return HttpResponse.json(resumed);
  }),

  // ----- Entities / faces -----
  http.get(`${BASE}/faces`, async () => {
    await jitter(60, 140);
    return HttpResponse.json({ clusters: [] });
  }),
  http.get(`${BASE}/faces/:id/files`, async ({ params }) => {
    await jitter(60, 140);
    return HttpResponse.json({ cluster_id: String(params.id), files: [] });
  }),
  http.post(`${BASE}/faces/:id/name`, async () => {
    await jitter(60, 140);
    return HttpResponse.json({ ok: true });
  }),
  http.get(`${BASE}/entities`, async ({ request }) => {
    await jitter();
    const url = new URL(request.url);
    const type = url.searchParams.get('type');
    const items = type ? ENTITIES.filter((e) => e.type === type) : ENTITIES;
    return HttpResponse.json({ items });
  }),
];
