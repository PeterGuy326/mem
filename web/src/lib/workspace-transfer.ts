import { apiRawResponse } from './api';
import type {
  WorkspaceImportConflict,
  WorkspaceImportHistory,
  WorkspaceImportHistoryEntry,
  WorkspaceImportResult,
  WorkspaceObjectCounts,
} from './types';

export const WORKSPACE_BUNDLE_MEDIA_TYPE = 'application/vnd.mem.workspace-bundle+zip';
export const WORKSPACE_BUNDLE_EXTENSION = '.membundle';

export type WorkspaceTransferErrorKind =
  | 'network'
  | 'permission'
  | 'conflict'
  | 'unsupported'
  | 'too_large'
  | 'invalid'
  | 'server'
  | 'api';

export class WorkspaceTransferError extends Error {
  readonly kind: WorkspaceTransferErrorKind;
  readonly status?: number;
  readonly code?: string;
  readonly hint?: string;
  readonly conflicts: WorkspaceImportConflict[];
  readonly conflictTotal?: number;
  readonly conflictsTruncated: boolean;

  constructor(input: {
    kind: WorkspaceTransferErrorKind;
    message: string;
    status?: number;
    code?: string;
    hint?: string;
    conflicts?: WorkspaceImportConflict[];
    conflictTotal?: number;
    conflictsTruncated?: boolean;
  }) {
    super(input.message);
    this.name = 'WorkspaceTransferError';
    this.kind = input.kind;
    this.status = input.status;
    this.code = input.code;
    this.hint = input.hint;
    this.conflicts = input.conflicts ?? [];
    this.conflictTotal = input.conflictTotal;
    this.conflictsTruncated = input.conflictsTruncated ?? false;
  }
}

export interface WorkspaceBundleDownload {
  blob: Blob;
  filename: string;
  byteLength: number;
}

export type WorkspaceBundleFileIssue = 'extension' | 'mime' | 'empty';

const COUNT_KEYS = [
  'folders',
  'files',
  'memories',
  'memory_events',
  'tasks',
  'checkpoints',
  'checkpoint_refs',
  'checkpoint_payloads',
  'blobs',
  'blob_bytes',
] as const satisfies readonly (keyof WorkspaceObjectCounts)[];

interface ParsedMediaType {
  type: string;
  parameters: Map<string, string>;
}

function parseMediaType(value: string): ParsedMediaType | null {
  const segments = value.split(';');
  const type = segments.shift()?.trim().toLowerCase();
  if (!type || !type.includes('/')) return null;
  const parameters = new Map<string, string>();
  for (const segment of segments) {
    const separator = segment.indexOf('=');
    if (separator <= 0) return null;
    const key = segment.slice(0, separator).trim().toLowerCase();
    let parameter = segment.slice(separator + 1).trim();
    if (parameter.startsWith('"') && parameter.endsWith('"')) {
      parameter = parameter.slice(1, -1);
    }
    if (!key || parameters.has(key)) return null;
    parameters.set(key, parameter);
  }
  return { type, parameters };
}

/**
 * The bundle schema version lives in manifest.json. HTTP requests and
 * responses use the canonical media type without parameters.
 */
export function isWorkspaceBundleMediaType(value: string): boolean {
  const parsed = parseMediaType(value);
  return Boolean(
    parsed && parsed.type === WORKSPACE_BUNDLE_MEDIA_TYPE && parsed.parameters.size === 0,
  );
}

/**
 * File.type is supplied by the operating system and custom extensions often
 * arrive empty. The extension remains mandatory; when a MIME is present it
 * must match the canonical, parameter-free bundle media type.
 */
function isSupportedLocalFileMediaType(value: string): boolean {
  if (!value) return true;
  const parsed = parseMediaType(value);
  return Boolean(
    parsed && parsed.type === WORKSPACE_BUNDLE_MEDIA_TYPE && parsed.parameters.size === 0,
  );
}

export function validateWorkspaceBundleFile(file: File): WorkspaceBundleFileIssue | null {
  if (!file.name.toLowerCase().endsWith(WORKSPACE_BUNDLE_EXTENSION)) {
    return 'extension';
  }
  if (!isSupportedLocalFileMediaType(file.type)) return 'mime';
  if (file.size === 0) return 'empty';
  return null;
}

function decodeDispositionValue(value: string): string {
  const trimmed = value.trim().replace(/^"(.*)"$/, '$1');
  try {
    return decodeURIComponent(trimmed);
  } catch {
    return trimmed;
  }
}

function filenameFromDisposition(value: string | null): string | null {
  if (!value || !/^\s*attachment(?:\s*;|$)/i.test(value)) return null;
  const extended = value.match(/(?:^|;)\s*filename\*\s*=\s*(?:UTF-8'')?([^;]+)/i);
  if (extended?.[1]) return decodeDispositionValue(extended[1]);
  const quoted = value.match(/(?:^|;)\s*filename\s*=\s*"((?:\\.|[^"])*)"/i);
  if (quoted?.[1]) {
    return quoted[1].replace(/\\(["\\])/g, '$1');
  }
  const plain = value.match(/(?:^|;)\s*filename\s*=\s*([^;]+)/i);
  return plain?.[1] ? decodeDispositionValue(plain[1]) : null;
}

function cleanFilenameCandidate(candidate: string): string {
  const basename = candidate.split(/[\\/]/).at(-1) ?? '';
  const normalized = basename.normalize('NFKC');
  const stripped = Array.from(normalized)
    .filter((character) => {
      const codePoint = character.codePointAt(0) ?? 0;
      return !(
        codePoint <= 0x1f ||
        (codePoint >= 0x7f && codePoint <= 0x9f) ||
        (codePoint >= 0x202a && codePoint <= 0x202e) ||
        (codePoint >= 0x2066 && codePoint <= 0x2069)
      );
    })
    .join('');
  const withoutControls = stripped
    .replace(/[<>:"|?*]/g, '-')
    .replace(/\s+/g, ' ')
    .replace(/^[.\s]+|[.\s]+$/g, '');
  const withoutExtension = withoutControls.toLowerCase().endsWith(WORKSPACE_BUNDLE_EXTENSION)
    ? withoutControls.slice(0, -WORKSPACE_BUNDLE_EXTENSION.length)
    : withoutControls;
  const safeStem = withoutExtension.replace(/^[.\s]+|[.\s]+$/g, '').slice(0, 110);
  if (!safeStem || /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/i.test(safeStem)) {
    return '';
  }
  return `${safeStem}${WORKSPACE_BUNDLE_EXTENSION}`;
}

export function safeWorkspaceBundleFilename(
  contentDisposition: string | null,
  workspaceID: string,
): string {
  const headerCandidate = filenameFromDisposition(contentDisposition);
  const safeHeaderName = headerCandidate ? cleanFilenameCandidate(headerCandidate) : '';
  if (safeHeaderName) return safeHeaderName;

  const workspaceStem =
    workspaceID
      .normalize('NFKC')
      .replace(/[^a-zA-Z0-9_-]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 48) || 'current';
  const date = new Date().toISOString().slice(0, 10);
  return `workspace-${workspaceStem}-${date}${WORKSPACE_BUNDLE_EXTENSION}`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

async function readErrorPayload(response: Response): Promise<Record<string, unknown>> {
  const text = await response.text().catch(() => '');
  if (!text) return {};
  try {
    const parsed = JSON.parse(text) as unknown;
    return isRecord(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

function parseConflicts(value: unknown): WorkspaceImportConflict[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((candidate) => {
    if (!isRecord(candidate) || typeof candidate.kind !== 'string') return [];
    return [
      {
        kind: candidate.kind,
        ...(typeof candidate.resource === 'string' ? { resource: candidate.resource } : {}),
        ...(typeof candidate.value === 'string' ? { value: candidate.value } : {}),
      },
    ];
  });
}

async function errorFromResponse(response: Response): Promise<WorkspaceTransferError> {
  const payload = await readErrorPayload(response);
  const code = typeof payload.error === 'string' ? payload.error : `http_${response.status}`;
  const hint = typeof payload.hint === 'string' ? payload.hint : undefined;
  const base = {
    message: code,
    status: response.status,
    code,
    hint,
  };
  if (response.status === 409 && code === 'workspace_import_conflict') {
    const conflictTotal =
      typeof payload.total === 'number' && Number.isSafeInteger(payload.total) && payload.total >= 0
        ? payload.total
        : undefined;
    return new WorkspaceTransferError({
      ...base,
      kind: 'conflict',
      conflicts: parseConflicts(payload.conflicts),
      conflictTotal,
      conflictsTruncated: payload.truncated === true,
    });
  }
  if (response.status === 401 || response.status === 403) {
    return new WorkspaceTransferError({ ...base, kind: 'permission' });
  }
  if (response.status === 413) {
    return new WorkspaceTransferError({ ...base, kind: 'too_large' });
  }
  if (response.status === 400 || response.status === 415) {
    return new WorkspaceTransferError({ ...base, kind: 'invalid' });
  }
  if (response.status === 422) {
    return new WorkspaceTransferError({ ...base, kind: 'unsupported' });
  }
  if (response.status >= 500) {
    return new WorkspaceTransferError({ ...base, kind: 'server' });
  }
  return new WorkspaceTransferError({ ...base, kind: 'api' });
}

function networkError(error: unknown): WorkspaceTransferError {
  if (error instanceof WorkspaceTransferError) return error;
  return new WorkspaceTransferError({
    kind: 'network',
    message: error instanceof Error ? error.message : 'network request failed',
  });
}

export async function exportWorkspaceBundle(workspaceID: string): Promise<WorkspaceBundleDownload> {
  try {
    const response = await apiRawResponse('/workspaces/current/export', {
      method: 'GET',
      headers: { Accept: WORKSPACE_BUNDLE_MEDIA_TYPE },
    });
    if (!response.ok) throw await errorFromResponse(response);
    if (!isWorkspaceBundleMediaType(response.headers.get('Content-Type') ?? '')) {
      throw new WorkspaceTransferError({
        kind: 'unsupported',
        message: 'workspace export returned an unsupported media type',
        status: response.status,
        code: 'unsupported_workspace_bundle_response',
      });
    }
    const blob = await response.blob();
    if (blob.size === 0) {
      throw new WorkspaceTransferError({
        kind: 'unsupported',
        message: 'workspace export returned an empty archive',
        status: response.status,
        code: 'empty_workspace_bundle_response',
      });
    }
    return {
      blob,
      byteLength: blob.size,
      filename: safeWorkspaceBundleFilename(
        response.headers.get('Content-Disposition'),
        workspaceID,
      ),
    };
  } catch (error) {
    throw networkError(error);
  }
}

export function saveWorkspaceBundle(download: WorkspaceBundleDownload): void {
  const url = URL.createObjectURL(download.blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = download.filename;
  anchor.rel = 'noopener';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1_000);
}

function parseCounts(value: unknown): WorkspaceObjectCounts | null {
  if (!isRecord(value)) return null;
  const counts = {} as WorkspaceObjectCounts;
  for (const key of COUNT_KEYS) {
    const count = value[key];
    if (typeof count !== 'number' || !Number.isSafeInteger(count) || count < 0) {
      return null;
    }
    counts[key] = count;
  }
  return counts;
}

function parseImportResult(value: unknown): WorkspaceImportResult | null {
  if (!isRecord(value)) return null;
  const counts = parseCounts(value.counts);
  if (
    typeof value.bundle_id !== 'string' ||
    typeof value.archive_sha256 !== 'string' ||
    typeof value.source_workspace_id !== 'string' ||
    typeof value.imported_at !== 'string' ||
    typeof value.replayed !== 'boolean' ||
    !counts
  ) {
    return null;
  }
  return {
    bundle_id: value.bundle_id,
    archive_sha256: value.archive_sha256,
    source_workspace_id: value.source_workspace_id,
    imported_at: value.imported_at,
    counts,
    replayed: value.replayed,
  };
}

export async function importWorkspaceBundle(file: File): Promise<WorkspaceImportResult> {
  const issue = validateWorkspaceBundleFile(file);
  if (issue) {
    throw new WorkspaceTransferError({
      kind: 'invalid',
      message: `invalid local workspace bundle: ${issue}`,
      code: `invalid_local_bundle_${issue}`,
    });
  }

  try {
    const response = await apiRawResponse('/workspaces/current/import', {
      method: 'POST',
      query: { mode: 'fresh' },
      headers: {
        Accept: 'application/json',
        'Content-Type': WORKSPACE_BUNDLE_MEDIA_TYPE,
      },
      body: file,
    });
    if (!response.ok) throw await errorFromResponse(response);
    const result = parseImportResult(await response.json().catch(() => undefined));
    if (!result) {
      throw new WorkspaceTransferError({
        kind: 'unsupported',
        message: 'workspace import returned an unsupported response',
        status: response.status,
        code: 'unsupported_workspace_import_response',
      });
    }
    return result;
  } catch (error) {
    throw networkError(error);
  }
}

function isNonNegativeSafeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}

/**
 * The ledger only ever contains fully committed imports, so every entry is
 * structurally succeeded. Fresh entries carry zero conflicts and zero skips;
 * merge_conservative entries carry their recorded counts. Entries that do not
 * match the contract are dropped rather than rendered half-typed.
 */
function parseImportHistoryEntry(value: unknown): WorkspaceImportHistoryEntry | null {
  if (!isRecord(value)) return null;
  if (
    typeof value.bundle_id !== 'string' ||
    typeof value.archive_sha256 !== 'string' ||
    typeof value.source_workspace_id !== 'string' ||
    typeof value.imported_at !== 'string' ||
    typeof value.restore_mode !== 'string' ||
    !isNonNegativeSafeInteger(value.schema_version) ||
    !isNonNegativeSafeInteger(value.conflict_count) ||
    !isNonNegativeSafeInteger(value.skipped_count)
  ) {
    return null;
  }
  return {
    bundle_id: value.bundle_id,
    archive_sha256: value.archive_sha256,
    source_workspace_id: value.source_workspace_id,
    schema_version: value.schema_version,
    restore_mode: value.restore_mode as WorkspaceImportHistoryEntry['restore_mode'],
    result_status: 'succeeded',
    conflict_count: value.conflict_count,
    skipped_count: value.skipped_count,
    imported_at: value.imported_at,
  };
}

function parseImportHistory(value: unknown): WorkspaceImportHistory | null {
  if (!isRecord(value) || !Array.isArray(value.items)) return null;
  const items = value.items
    .map(parseImportHistoryEntry)
    .filter((entry): entry is WorkspaceImportHistoryEntry => entry !== null);
  return { items, count: items.length };
}

/**
 * Lists committed bundle imports for the current workspace, newest first.
 * Read-only projection of the workspace_imports idempotency ledger.
 */
export async function listImportHistory(options?: {
  limit?: number;
  signal?: AbortSignal;
}): Promise<WorkspaceImportHistory> {
  try {
    const response = await apiRawResponse('/workspaces/current/imports', {
      method: 'GET',
      headers: { Accept: 'application/json' },
      query: options?.limit !== undefined ? { limit: options.limit } : undefined,
      signal: options?.signal,
    });
    if (!response.ok) throw await errorFromResponse(response);
    const history = parseImportHistory(await response.json().catch(() => undefined));
    if (!history) {
      throw new WorkspaceTransferError({
        kind: 'unsupported',
        message: 'workspace import history returned an unsupported response',
        status: response.status,
        code: 'unsupported_workspace_import_history_response',
      });
    }
    return history;
  } catch (error) {
    throw networkError(error);
  }
}
