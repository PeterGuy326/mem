/**
 * Domain types — mirror SPEC.md §6 (data model) and §7/§8 (API surface).
 * Backend will eventually generate these from OpenAPI / protobuf; for now we
 * hand-write them and keep them in sync with the spec.
 */

export type IndexStatus = 'pending' | 'processing' | 'done' | 'failed';

export type FileKind = 'image' | 'doc' | 'audio' | 'video' | 'pdf' | 'text' | 'other';

/** Coarse type filter exposed in /v1/search (see SPEC §8.1 mem_search). */
export type SearchTypeFilter = 'image' | 'doc' | 'audio' | 'any';

export type TokenScope = 'search' | 'read' | 'write' | 'delete' | 'admin';

export interface User {
  id: string;
  email: string;
  created_at: string;
}

export interface Quota {
  calls_per_day?: number;
  storage_bytes?: number;
  ai_tokens_per_day?: number;
}

export interface Token {
  id: string;
  user_id: string;
  name: string;
  scopes: TokenScope[];
  quota: Quota;
  paths: string[];
  expires_at: string | null;
  redact_pii: boolean;
  created_at: string;
}

/** SPEC §6.1 `files` row + a few derived URLs the API will return. */
export interface MemFile {
  id: string;
  user_id: string;
  name: string;
  path: string;
  size: number;
  sha256: string;
  mime: string;
  storage_key: string;

  // AI fields
  summary: string | null;
  caption: string | null;
  tags: string[];
  timeline_at: string | null;
  geo: { lat: number; lon: number } | null;

  // Status
  index_status: IndexStatus;

  // Timestamps
  created_at: string;
  updated_at: string;

  // Derived (API-only)
  kind: FileKind;
  preview_url: string | null;
  thumbnail_url: string | null;
  download_url: string | null;
  entities?: Entity[];
}

export interface Entity {
  id: string;
  user_id?: string;
  type: 'person' | 'place' | 'org' | 'event';
  name: string;
  metadata?: Record<string, unknown>;
  created_at?: string;
}

/** Single search hit — shape matches SPEC §8.1 mem_search output. */
export interface SearchResult {
  file: MemFile;
  score: number;
  snippet: string | null;
  /** Which retrieval channel produced this hit (for tinting / debugging). */
  channel?: 'visual' | 'text' | 'metadata' | 'fused';
}

export interface SearchResponse {
  results: SearchResult[];
  total: number;
  query_plan?: {
    entities: string[];
    semantic_query: string;
  };
  _meta?: {
    quota_remaining?: number;
    latency_ms?: number;
  };
}

export interface RelatedResponse {
  results: Array<{
    file: MemFile;
    relation: string;
    score: number;
  }>;
}

export interface ListFilesResponse {
  files: MemFile[];
  limit?: number;
  page?: number;
}

export interface AuthLoginResponse {
  token: string;
  user: User;
}

/** Standardized error envelope. SPEC §8.2 says `{ error, hint }`. */
export interface ApiError {
  error: string;
  hint?: string;
  status: number;
}
