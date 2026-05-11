import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type {
  ListFilesResponse,
  MemFile,
  RelatedResponse,
  SearchResponse,
  SearchTypeFilter,
} from '@/lib/types';
import type { FolderNode } from '@/lib/folder-tree';

export const fileKeys = {
  all: ['files'] as const,
  list: (params: Record<string, unknown>) => ['files', 'list', params] as const,
  byPath: (path: string) => ['files', 'by-path', path] as const,
  detail: (id: string) => ['files', 'detail', id] as const,
  related: (id: string) => ['files', 'related', id] as const,
  tree: () => ['files', 'tree'] as const,
};

export const searchKeys = {
  query: (params: Record<string, unknown>) => ['search', params] as const,
};

/** List direct children of a folder by absolute virtual path. */
export function useFilesByPath(path: string) {
  return useQuery({
    queryKey: fileKeys.byPath(path),
    queryFn: () => api.get<ListFilesResponse>('/files', { query: { path, limit: 1000 } }),
  });
}

/** Whole folder tree. */
export function useFolderTree() {
  return useQuery({
    queryKey: fileKeys.tree(),
    queryFn: () => api.get<{ tree: FolderNode }>('/files/tree'),
  });
}

export function useFile(id: string | undefined) {
  return useQuery({
    queryKey: fileKeys.detail(id ?? ''),
    queryFn: () => api.get<MemFile>(`/files/${id}`),
    enabled: !!id,
  });
}

export function useRelated(id: string | undefined) {
  return useQuery({
    queryKey: fileKeys.related(id ?? ''),
    queryFn: () => api.get<RelatedResponse>(`/files/${id}/related`),
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

export function useSearch(params: SearchParams, opts?: { enabled?: boolean }) {
  return useQuery({
    queryKey: searchKeys.query({ ...params }),
    queryFn: () =>
      api.get<SearchResponse>('/search', {
        query: {
          q: params.q,
          type: params.type,
          since: params.since,
          until: params.until,
          face: params.face,
          limit: params.limit ?? 30,
        },
      }),
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
        const res = await api.upload<MemFile>('/files', fd);
        results.push(res);
      }
      return results;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all });
    },
  });
}

export function useDeleteFile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.del<{ ok: true }>(`/files/${id}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all });
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
      void qc.invalidateQueries({ queryKey: fileKeys.all });
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
      void qc.invalidateQueries({ queryKey: fileKeys.all });
    },
  });
}

/** Create a folder under a parent path. */
export function useCreateFolder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ path, name }: { path: string; name: string }) =>
      api.post<{ path: string; name: string }>('/folders', { path, name }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all });
    },
  });
}

/** Rename a folder. */
export function useRenameFolder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ path, name }: { path: string; name: string }) =>
      api.apiPatch<{ ok: true; new_path: string }>('/folders', { path, name }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all });
    },
  });
}

/** Move a folder (change parent). */
export function useMoveFolder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ path, newParent }: { path: string; newParent: string }) =>
      api.put<{ ok: true; new_path: string }>('/folders', { path, new_parent: newParent }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all });
    },
  });
}

/** Delete a folder (recursive). */
export function useDeleteFolder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ path }: { path: string }) =>
      api.del<{ ok: true }>(`/folders?path=${encodeURIComponent(path)}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fileKeys.all });
    },
  });
}
