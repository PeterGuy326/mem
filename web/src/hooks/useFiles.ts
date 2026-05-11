import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type {
  ListFilesResponse,
  MemFile,
  RelatedResponse,
  SearchResponse,
  SearchTypeFilter,
} from '@/lib/types';

export const fileKeys = {
  all: ['files'] as const,
  list: (params: Record<string, unknown>) => ['files', 'list', params] as const,
  detail: (id: string) => ['files', 'detail', id] as const,
  related: (id: string) => ['files', 'related', id] as const,
};

export const searchKeys = {
  query: (params: Record<string, unknown>) => ['search', params] as const,
};

export function useRecentFiles(limit = 10) {
  return useQuery({
    queryKey: fileKeys.list({ limit }),
    queryFn: () => api.get<ListFilesResponse>('/files', { query: { limit } }),
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

export function useUpload() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (files: File[]) => {
      const results: MemFile[] = [];
      for (const file of files) {
        const fd = new FormData();
        fd.append('file', file);
        fd.append('name', file.name);
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
