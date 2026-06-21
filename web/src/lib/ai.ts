// Real-backend API client for the AI-side endpoints (search/ask/faces/providers/timeline/related).
// Stays separate from lib/api.ts hooks that still use the W1 MSW mock shape;
// new pages call these directly.

import { api, getToken } from './api';

// --- Search ---

export interface SearchHit {
  file_id: string;
  name: string;
  path: string;
  mime: string;
  score: number;
  snippet: string;
  source: 'text' | 'visual' | string;
  summary?: string | null;
  timeline_at?: string | null;
  created_at: string;
}

export interface SearchResponse {
  results: SearchHit[];
  _meta?: { latency_ms?: number };
}

export function searchFiles(params: {
  query: string;
  type?: string;
  route?: 'text' | 'visual' | 'auto';
  since?: string;
  until?: string;
  limit?: number;
}): Promise<SearchResponse> {
  return api.post<SearchResponse>('/search', params);
}

// --- Ask (RAG) ---

export interface AskSource {
  file_id: string;
  name: string;
  path: string;
  mime: string;
  excerpt: string;
  score: number;
}
export interface AskStep {
  name: string; // "retrieve" | "generate"
  label: string;
  detail: string;
  duration_ms: number;
}
export interface AskResponse {
  answer: string;
  sources: AskSource[];
  steps?: AskStep[];
  provider: string;
  latency_ms: number;
  asked_at: string;
}

export function askQuestion(params: {
  question: string;
  scope?: string;
  top_k?: number;
}): Promise<AskResponse> {
  return api.post<AskResponse>('/ask', params);
}

// --- Ask (streaming) ---

export interface AskStreamEvent {
  type: 'step' | 'thinking' | 'answer' | 'sources' | 'done' | 'error';
  step?: AskStep;
  delta?: string;
  sources?: AskSource[];
  error?: string;
}

/**
 * Stream a RAG answer over Server-Sent Events, invoking `onEvent` for each
 * chunk (retrieval step, thinking deltas, answer deltas, sources, done). Lets
 * the UI render the model's reasoning + answer token-by-token. Returns when the
 * stream ends; throws on transport errors so the caller can fall back to the
 * unary askQuestion().
 */
export async function streamAsk(
  params: { question: string; scope?: string; top_k?: number },
  onEvent: (ev: AskStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const token = getToken();
  const res = await fetch('/v1/ask/stream', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify(params),
    signal,
  });
  if (!res.ok || !res.body) {
    throw new Error(`stream failed: ${res.status}`);
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    // SSE frames are separated by a blank line.
    let idx: number;
    while ((idx = buf.indexOf('\n\n')) !== -1) {
      const frame = buf.slice(0, idx);
      buf = buf.slice(idx + 2);
      const line = frame.split('\n').find((l) => l.startsWith('data:'));
      if (!line) continue;
      const json = line.slice(5).trim();
      if (!json) continue;
      try {
        onEvent(JSON.parse(json) as AskStreamEvent);
      } catch {
        /* ignore malformed frame */
      }
    }
  }
}

// --- Faces ---

export interface FaceCluster {
  id: string;
  name: string;
  face_count: number;
  file_count: number;
  cover_file_id?: string;
}

export function listFaces(): Promise<{ clusters: FaceCluster[] }> {
  return api.get<{ clusters: FaceCluster[] }>('/faces');
}

export interface FaceFile {
  file_id: string;
  name: string;
  path: string;
  mime: string;
  caption?: string | null;
  index_status: string;
  created_at: string;
}

export function getFaceFiles(clusterId: string): Promise<{ cluster_id: string; files: FaceFile[] }> {
  return api.get(`/faces/${clusterId}/files`);
}

export function nameFace(id: string, name: string): Promise<{ ok: boolean }> {
  return api.post<{ ok: boolean }>(`/faces/${id}/name`, { name });
}

export function mergeFaces(keepId: string, mergeId: string): Promise<{ ok: boolean }> {
  return api.post<{ ok: boolean }>(`/faces/${keepId}/merge`, { into: mergeId });
}

// --- Providers ---

export interface ProviderSetting {
  kind: 'embedding' | 'llm' | 'vlm' | string;
  spec: string;
  dim?: number | null;
  updated_at: string;
}

export function listProviders(): Promise<{ settings: ProviderSetting[]; kinds: string[] }> {
  return api.get<{ settings: ProviderSetting[]; kinds: string[] }>('/providers');
}

export function setProvider(
  kind: string,
  spec: string,
): Promise<{
  setting: ProviderSetting;
  reindex_queued: boolean;
  reindex_files?: number;
  previous_dim?: number | null;
  dim_migration_ok: boolean;
}> {
  return api.put(`/providers/${kind}`, { spec });
}

export function testProvider(kind: string, spec?: string): Promise<Record<string, unknown>> {
  return api.post(`/providers/${kind}/test`, spec ? { spec } : {});
}

// --- Timeline ---

export interface TimelineEntry {
  id: string;
  name: string;
  path: string;
  mime: string;
  at: string;
  summary?: string | null;
  caption?: string | null;
}
export interface TimelineBucket {
  month: string;
  count: number;
  files: TimelineEntry[];
}
export interface TimelineResponse {
  from: string;
  until: string;
  months: TimelineBucket[];
}

export function getTimeline(year: string): Promise<TimelineResponse> {
  return api.get<TimelineResponse>('/timeline', { query: { year } });
}

// --- Related ---

export interface RelatedHit {
  file_id: string;
  name: string;
  path: string;
  mime: string;
  type: string;
  score: number;
  summary?: string | null;
}
export function getRelated(
  fileId: string,
  type?: string,
  limit?: number,
): Promise<{ file_id: string; related: RelatedHit[] }> {
  const qs: string[] = [];
  if (type) qs.push(`type=${encodeURIComponent(type)}`);
  if (limit) qs.push(`limit=${limit}`);
  const q = qs.length ? `?${qs.join('&')}` : '';
  return api.get(`/files/${fileId}/related${q}`);
}
