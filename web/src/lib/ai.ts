// Real-backend API client for search, faces, indexing providers, timeline, and related files.
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
  return api.post<SearchResponse>('/search', params, {
    headers: { 'Idempotency-Key': `web-search-${crypto.randomUUID()}` },
  });
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

export const INDEX_PROVIDER_KINDS = [
  'embedding',
  'vlm',
  'asr',
  'ocr',
] as const;

export type IndexProviderKind = (typeof INDEX_PROVIDER_KINDS)[number];

export function isIndexProviderKind(kind: string): kind is IndexProviderKind {
  return INDEX_PROVIDER_KINDS.some((candidate) => candidate === kind);
}

export interface ProviderSetting {
  // The API may advertise roles the web product intentionally does not expose.
  // Consumers should narrow this with isIndexProviderKind before rendering.
  kind: string;
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
  reindex_required?: boolean;
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
