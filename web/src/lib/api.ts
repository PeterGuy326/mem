import type { ApiError } from './types';

const TOKEN_KEY = 'mem.token';
const WORKSPACE_KEY = 'mem.workspace.current';
const API_BASE = (import.meta.env.VITE_API_BASE ?? '').replace(/\/$/, '') + '/v1';

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY);
}

export function getCurrentWorkspaceID(): string | null {
  return localStorage.getItem(WORKSPACE_KEY);
}

export function setCurrentWorkspaceID(id: string | null): void {
  if (id) localStorage.setItem(WORKSPACE_KEY, id);
  else localStorage.removeItem(WORKSPACE_KEY);
}

/**
 * Stable namespace for React Query caches.
 *
 * The API resolves an implicit default workspace when no header is present, so
 * keep that state separate from every explicitly selected workspace.
 */
export function getWorkspaceCacheKey(): string {
  return getCurrentWorkspaceID() ?? 'default';
}

/**
 * The stored token is missing/invalid/expired server-side. Drop the whole
 * session and land on /login — otherwise every page renders eternal loading
 * skeletons on top of 401s.
 */
function forceLogout(): void {
  clearToken();
  localStorage.removeItem('mem.user');
  localStorage.removeItem(WORKSPACE_KEY);
  if (window.location.pathname !== '/login') {
    window.location.assign('/login');
  }
}

export class ApiException extends Error {
  status: number;
  hint?: string;

  constructor(err: ApiError) {
    super(err.error);
    this.name = 'ApiException';
    this.status = err.status;
    this.hint = err.hint;
  }
}

interface RequestOptions extends Omit<RequestInit, 'body'> {
  body?: unknown;
  /** When `body` is FormData, leave Content-Type alone. */
  formData?: FormData;
  query?: Record<string, string | number | boolean | undefined | null>;
}

export interface RawRequestOptions extends Omit<RequestInit, 'body' | 'headers'> {
  body?: BodyInit | null;
  headers?: HeadersInit;
  query?: Record<string, string | number | boolean | undefined | null>;
}

function buildQuery(query: RequestOptions['query']): string {
  if (!query) return '';
  const usp = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '') continue;
    usp.set(k, String(v));
  }
  const s = usp.toString();
  return s ? `?${s}` : '';
}

export async function apiFetch<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { body, formData, query, headers, ...rest } = opts;

  const finalHeaders: Record<string, string> = {
    Accept: 'application/json',
    ...(headers as Record<string, string> | undefined),
  };

  const token = getToken();
  if (token) {
    finalHeaders['Authorization'] = `Bearer ${token}`;
  }
  const workspaceID = getCurrentWorkspaceID();
  if (workspaceID) finalHeaders['X-Workspace-ID'] = workspaceID;

  let payload: BodyInit | undefined;
  if (formData) {
    payload = formData;
  } else if (body !== undefined) {
    finalHeaders['Content-Type'] = 'application/json';
    payload = JSON.stringify(body);
  }

  const url = `${API_BASE}${path}${buildQuery(query)}`;
  const res = await fetch(url, {
    ...rest,
    headers: finalHeaders,
    body: payload,
  });

  const text = await res.text();
  const data = text ? safeParse(text) : null;

  if (!res.ok) {
    const err: ApiError = {
      error:
        (data && typeof data === 'object' && 'error' in data
          ? String(data.error)
          : res.statusText) || 'request failed',
      hint: data && typeof data === 'object' && 'hint' in data ? String(data.hint) : undefined,
      status: res.status,
    };
    // invalid_credentials is a wrong-password 401 from /auth/login — the form
    // shows it inline; every other 401 means the stored token is dead.
    if (res.status === 401 && err.error !== 'invalid_credentials') {
      forceLogout();
    }
    throw new ApiException(err);
  }

  return data as T;
}

/**
 * Authenticated response-level fetch for contracts that must inspect headers
 * before consuming a binary body. Callers own status/body parsing; 401 still
 * follows the same session-invalidating behavior as the JSON helpers.
 */
export async function apiRawResponse(
  path: string,
  opts: RawRequestOptions = {},
): Promise<Response> {
  const { query, headers, ...rest } = opts;
  const finalHeaders = new Headers(headers);
  if (!finalHeaders.has('Accept')) finalHeaders.set('Accept', 'application/json');

  const token = getToken();
  if (token) finalHeaders.set('Authorization', `Bearer ${token}`);
  const workspaceID = getCurrentWorkspaceID();
  if (workspaceID) finalHeaders.set('X-Workspace-ID', workspaceID);

  const url = `${API_BASE}${path}${buildQuery(query)}`;
  const response = await fetch(url, { ...rest, headers: finalHeaders });
  if (response.status === 401) forceLogout();
  return response;
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

/**
 * Fetch a raw binary body (image / audio / any blob) with the bearer token
 * attached. `<img src>` / `<audio src>` can't carry an Authorization header,
 * so we pull the bytes here and the caller turns them into an object URL.
 */
export async function apiBlob(path: string, opts: RequestOptions = {}): Promise<Blob> {
  // Drop body/formData — a binary GET never carries them; keeping them would
  // leak `body: unknown` into fetch's RequestInit.
  const { query, headers, body: _body, formData: _formData, ...rest } = opts;
  void _body;
  void _formData;
  const finalHeaders: Record<string, string> = {
    ...(headers as Record<string, string> | undefined),
  };
  const token = getToken();
  if (token) finalHeaders['Authorization'] = `Bearer ${token}`;
  const workspaceID = getCurrentWorkspaceID();
  if (workspaceID) finalHeaders['X-Workspace-ID'] = workspaceID;

  const url = `${API_BASE}${path}${buildQuery(query)}`;
  const res = await fetch(url, { ...rest, headers: finalHeaders });
  if (!res.ok) {
    if (res.status === 401) forceLogout();
    throw new ApiException({ error: res.statusText || 'blob fetch failed', status: res.status });
  }
  return res.blob();
}

/** Download a file's bytes (with auth) and trigger a browser save dialog. */
export async function downloadFile(fileId: string, name: string): Promise<void> {
  const blob = await apiBlob(`/files/${fileId}/content`);
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/* Convenience verbs */
export const api = {
  get: <T>(path: string, opts?: RequestOptions) => apiFetch<T>(path, { ...opts, method: 'GET' }),
  blob: (path: string, opts?: RequestOptions) => apiBlob(path, { ...opts, method: 'GET' }),
  post: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    apiFetch<T>(path, { ...opts, method: 'POST', body }),
  put: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    apiFetch<T>(path, { ...opts, method: 'PUT', body }),
  apiPatch: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    apiFetch<T>(path, { ...opts, method: 'PATCH', body }),
  del: <T>(path: string, opts?: RequestOptions) => apiFetch<T>(path, { ...opts, method: 'DELETE' }),
  upload: <T>(path: string, formData: FormData) => apiFetch<T>(path, { method: 'POST', formData }),
};
