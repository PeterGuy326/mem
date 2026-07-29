import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiException, getWorkspaceCacheKey } from '@/lib/api';
import type {
  FileAnnotation,
  FileAnnotationDecision,
  FileKind,
  ListFilesResponse,
  MemFile,
  RelatedResponse,
  SearchResponse,
  SearchResult,
  SearchTypeFilter,
} from '@/lib/types';
import type { FolderNode } from '@/lib/folder-tree';
import { ROOT_NAME } from '@/lib/folder-tree';

export const fileKeys = {
  all: () => ['workspace', getWorkspaceCacheKey(), 'files'] as const,
  list: (params: Record<string, unknown>) => [...fileKeys.all(), 'list', params] as const,
  byPath: (path: string) => [...fileKeys.all(), 'by-path', path] as const,
  detail: (id: string) => [...fileKeys.all(), 'detail', id] as const,
  related: (id: string) => [...fileKeys.all(), 'related', id] as const,
  tree: () => [...fileKeys.all(), 'tree'] as const,
};

export const searchKeys = {
  all: () => ['workspace', getWorkspaceCacheKey(), 'search'] as const,
  query: (params: Record<string, unknown>) => [...searchKeys.all(), params] as const,
};

/**
 * memd returns the raw `files` row — it has no derived `kind` / `*_url` fields,
 * so we backfill them client-side. `kind` (computed from mime) drives icons,
 * thumbnails, and preview routing across the UI.
 */
function normalizeFile(f: MemFile): MemFile {
  return {
    ...f,
    summary: f.summary ?? null,
    caption: f.caption ?? null,
    tags: f.tags ?? [],
    user_tags: f.user_tags ?? f.tags ?? [],
    timeline_at: f.timeline_at ?? null,
    geo: f.geo ?? null,
    source_metadata: f.source_metadata ?? {},
    processor_metadata: f.processor_metadata ?? {},
    annotations: f.annotations ?? [],
    annotations_truncated: f.annotations_truncated ?? false,
    index_status: f.index_status ?? 'pending',
    kind: f.kind ?? mimeToKind(f.mime),
    preview_url: f.preview_url ?? null,
    thumbnail_url: f.thumbnail_url ?? null,
    download_url: f.download_url ?? null,
  };
}

/** List direct children of a folder by absolute virtual path. */
export function useFilesByPath(path: string) {
  return useQuery({
    queryKey: fileKeys.byPath(path),
    queryFn: async () => {
      const raw = await api.get<ListFilesResponse>('/files', { query: { path, limit: 1000 } });
      return { ...raw, files: (raw.files ?? []).map(normalizeFile) };
    },
  });
}

/**
 * Raw folder-tree node as returned by memd `GET /v1/folders/tree`: snake_case
 * `file_count`, leaf nodes omit `children`, the root carries an empty `name`.
 */
interface RawFolderNode {
  id?: string;
  name: string;
  path: string;
  file_count?: number;
  fileCount?: number;
  children?: RawFolderNode[];
}

function toFolderNode(n: RawFolderNode): FolderNode {
  return {
    id: n.id,
    name: n.name || ROOT_NAME,
    path: n.path,
    fileCount: n.file_count ?? n.fileCount ?? 0,
    children: (n.children ?? []).map(toFolderNode),
  };
}

/** Whole folder tree. memd returns a bare FolderNode; we wrap it as `{ tree }`. */
export function useFolderTree() {
  return useQuery({
    queryKey: fileKeys.tree(),
    queryFn: async () => {
      const raw = await api.get<RawFolderNode>('/folders/tree');
      return { tree: toFolderNode(raw) };
    },
  });
}

export function useFile(id: string | undefined) {
  return useQuery({
    queryKey: fileKeys.detail(id ?? ''),
    queryFn: async () => normalizeFile(await api.get<MemFile>(`/files/${id}`)),
    enabled: !!id,
  });
}

function mimeToKind(mime: string): FileKind {
  const m = mime.toLowerCase();
  if (m.startsWith('image/')) return 'image';
  if (m.startsWith('audio/')) return 'audio';
  if (m.startsWith('video/')) return 'video';
  if (m === 'application/pdf') return 'pdf';
  if (m.startsWith('text/')) return 'text';
  return 'doc';
}

/**
 * Build a {@link MemFile} from the partial fields memd returns on flat search /
 * related hits. The unknown fields get harmless defaults — these synthetic
 * files are only used for list rendering, never for mutation.
 */
function makeMemFile(p: {
  id: string;
  name: string;
  path: string;
  mime: string;
  summary?: string | null;
  caption?: string | null;
  created_at?: string;
  timeline_at?: string | null;
}): MemFile {
  return {
    id: p.id,
    user_id: '',
    name: p.name,
    path: p.path,
    size: 0,
    sha256: '',
    mime: p.mime,
    storage_key: '',
    summary: p.summary ?? null,
    caption: p.caption ?? null,
    tags: [],
    user_tags: [],
    timeline_at: p.timeline_at ?? null,
    geo: null,
    source_metadata: {},
    processor_metadata: {},
    annotations: [],
    annotations_truncated: false,
    index_status: 'done',
    created_at: p.created_at ?? '',
    updated_at: p.created_at ?? '',
    kind: mimeToKind(p.mime),
    preview_url: null,
    thumbnail_url: null,
    download_url: null,
  };
}

/** A single related hit as returned by memd `GET /v1/files/{id}/related`. */
interface RawRelatedHit {
  file_id: string;
  name: string;
  path: string;
  mime: string;
  type: string;
  score: number;
  summary?: string | null;
}


export function useRelated(id: string | undefined) {
  return useQuery({
    queryKey: fileKeys.related(id ?? ''),
    queryFn: async () => {
      const raw = await api.get<{ related: RawRelatedHit[] }>(`/files/${id}/related`);
      const results = (raw.related ?? []).map((h) => ({
        file: makeMemFile({
          id: h.file_id,
          name: h.name,
          path: h.path,
          mime: h.mime,
          summary: h.summary,
        }),
        relation: h.type,
        score: h.score,
      }));
      const out: RelatedResponse = { results };
      return out;
    },
    enabled: !!id,
  });
}

export interface SearchParams {
  q: string;
  type?: SearchTypeFilter;
  since?: string;
  until?: string;
  face?: string;
  limit?: number;
}

/** A single hit as returned by memd `POST /v1/search`. */
interface RawSearchHit {
  evidence_id?: string;
  file_id: string;
  name: string;
  path: string;
  mime: string;
  score: number;
  snippet: string | null;
  source: 'text' | 'visual' | string;
  content_sha256?: string;
  chunk_index?: number;
  summary?: string | null;
  timeline_at?: string | null;
  created_at: string;
}

/** Adapt a flat memd search hit into the {@link SearchResult} shape the UI renders. */
function hitToResult(h: RawSearchHit): SearchResult {
  return {
    file: makeMemFile({
      id: h.file_id,
      name: h.name,
      path: h.path,
      mime: h.mime,
      summary: h.summary,
      caption: h.source === 'visual' ? h.snippet : null,
      created_at: h.created_at,
      timeline_at: h.timeline_at,
    }),
    score: h.score,
    snippet: h.snippet,
    channel: h.source === 'visual' ? 'visual' : 'text',
    evidence_id: h.evidence_id,
    content_sha256: h.content_sha256,
    chunk_index: h.chunk_index,
  };
}

export function useSearch(params: SearchParams, opts?: { enabled?: boolean }) {
  return useQuery({
    queryKey: searchKeys.query({ ...params }),
    queryFn: async () => {
      const raw = await api.post<{ results: RawSearchHit[]; _meta?: { latency_ms?: number } }>(
        '/search',
        {
          query: params.q,
          type: params.type && params.type !== 'any' ? params.type : undefined,
          since: params.since || undefined,
          until: params.until || undefined,
          limit: params.limit ?? 30,
        },
      );
      const results = (raw.results ?? []).map(hitToResult);
      const out: SearchResponse = { results, total: results.length, _meta: raw._meta };
      return out;
    },
    enabled: (opts?.enabled ?? true) && params.q.trim().length > 0,
  });
}

/** Upload one or more files into a target folder path. */
export function useUpload() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ files, path }: { files: File[]; path: string }) => {
      const results: MemFile[] = [];
      for (const file of files) {
        const fd = new FormData();
        fd.append('file', file);
        fd.append('name', file.name);
        fd.append('path', path);
        fd.append('source_metadata', JSON.stringify({ source_kind: 'web' }));
        const res = await api.upload<{ file: MemFile; deduped: boolean }>('/files', fd);
        results.push(normalizeFile(res.file));
      }
      return results;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all() });
    },
  });
}

export interface DecideFileAnnotationInput {
  annotationID: string;
  decision: FileAnnotationDecision;
  expectedVersion: number;
}

export interface FileAnnotationDecisionResult {
  annotation: FileAnnotation;
  replayed: boolean;
}

/**
 * Review a model suggestion through the canonical optimistic-locking API.
 *
 * A 409 means another reviewer or retry won the race. Refetch the active file
 * immediately so the caller can explain the conflict against current state.
 */
export function useDecideFileAnnotation(fileID: string) {
  const qc = useQueryClient();
  return useMutation<FileAnnotationDecisionResult, ApiException, DecideFileAnnotationInput>({
    mutationFn: ({ annotationID, decision, expectedVersion }) =>
      api.put<FileAnnotationDecisionResult>(`/files/${fileID}/annotations/${annotationID}`, {
        decision,
        expected_version: expectedVersion,
      }),
    onSuccess: async () => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: fileKeys.all() }),
        qc.invalidateQueries({ queryKey: searchKeys.all() }),
      ]);
    },
    onError: async (error) => {
      if (error.status === 409) {
        await Promise.all([
          qc.invalidateQueries({ queryKey: fileKeys.all() }),
          qc.invalidateQueries({ queryKey: searchKeys.all() }),
        ]);
      }
    },
  });
}

export function useDeleteFile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.del<{ ok: true }>(`/files/${id}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all() });
    },
  });
}

/** Move a file to a new folder. */
export function useMoveFile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, targetPath }: { id: string; targetPath: string }) =>
      api.apiPatch<MemFile>(`/files/${id}`, { path: targetPath }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all() });
    },
  });
}

/** Rename a file. */
export function useRenameFile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) =>
      api.apiPatch<MemFile>(`/files/${id}`, { name }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all() });
    },
  });
}

/** Create a folder under a parent path. */
export function useCreateFolder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ path, name }: { path: string; name: string }) =>
      api.post<FolderNode>('/folders', { path: path === '/' ? `/${name}` : `${path}/${name}` }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all() });
    },
  });
}

/** Rename a folder. */
export function useRenameFolder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) =>
      api.apiPatch<FolderNode>(`/folders/${id}`, { name }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all() });
    },
  });
}

/** Move a folder (change parent). */
export function useMoveFolder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, newParent }: { id: string; newParent: string }) =>
      api.apiPatch<FolderNode>(`/folders/${id}`, { parent_path: newParent }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all() });
    },
  });
}

/** Delete a folder recursively. */
export function useDeleteFolder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id }: { id: string }) =>
      api.del<void>(`/folders/${id}`, { query: { recursive: true } }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all() });
    },
  });
}
