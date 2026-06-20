// Real-backend API client for the AI-side endpoints (search/ask/faces/providers/timeline/related).
// Stays separate from lib/api.ts hooks that still use the W1 MSW mock shape;
// new pages call these directly.

import { api } from './api';

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

// --- Faces ---

export interface FaceCluster {
  id: string;
  name: string;
  face_count: number;
  file_count: number;
}

export function listFaces(): Promise<{ clusters: FaceCluster[] }> {
  return api.get<{ clusters: FaceCluster[] }>('/faces');
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
