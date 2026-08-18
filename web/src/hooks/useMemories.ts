import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  archiveMemory,
  createMemoryRelation,
  feedbackMemory,
  forgetMemory,
  getMemory,
  listMemories,
  listMemoryRelations,
  restoreMemory,
  type FeedbackMemoryInput,
  type ForgetMemoryInput,
  type ListMemoriesParams,
  type ListMemoryRelationsParams,
  type VersionedMemoryAction,
} from '@/lib/memories';
import type { CreateMemoryRelationRequest } from '@/lib/types';
import { useAuth } from './useAuth';
import { useCapabilities, useWorkspaceQueryKey } from './useWorkspace';

export const memoryKeys = {
  all: (workspaceID: string) => ['workspace', workspaceID, 'memories'] as const,
  lists: (workspaceID: string) => [...memoryKeys.all(workspaceID), 'list'] as const,
  list: (workspaceID: string, params: Omit<ListMemoriesParams, 'cursor'>) =>
    [...memoryKeys.lists(workspaceID), params] as const,
  detail: (workspaceID: string, memoryID: string) =>
    [...memoryKeys.all(workspaceID), 'detail', memoryID] as const,
  relationsAll: (workspaceID: string) => [...memoryKeys.all(workspaceID), 'relations'] as const,
  relations: (workspaceID: string, memoryID: string, params: ListMemoryRelationsParams) =>
    [...memoryKeys.relationsAll(workspaceID), memoryID, params] as const,
};

function useMemoryReadEnabled(): boolean {
  const capabilities = useCapabilities();
  return (
    capabilities.isSuccess &&
    capabilities.data.permissions.read &&
    capabilities.data.features.memory !== false
  );
}

export function useMemories(params: Omit<ListMemoriesParams, 'cursor'> = {}) {
  const workspaceID = useWorkspaceQueryKey();
  const enabled = useMemoryReadEnabled();
  return useInfiniteQuery({
    queryKey: memoryKeys.list(workspaceID, params),
    queryFn: ({ pageParam }) =>
      listMemories({
        ...params,
        cursor: pageParam || undefined,
      }),
    initialPageParam: '',
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    enabled,
  });
}

export function useMemory(memoryID: string | undefined) {
  const workspaceID = useWorkspaceQueryKey();
  const enabled = useMemoryReadEnabled();
  return useQuery({
    queryKey: memoryKeys.detail(workspaceID, memoryID ?? ''),
    queryFn: () => getMemory(memoryID!),
    enabled: enabled && !!memoryID,
  });
}

function useInvalidateMemory() {
  const workspaceID = useWorkspaceQueryKey();
  const queryClient = useQueryClient();
  return async (memoryID: string) => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: memoryKeys.lists(workspaceID) }),
      queryClient.invalidateQueries({
        queryKey: memoryKeys.detail(workspaceID, memoryID),
      }),
    ]);
  };
}

function useMemoryActorID(): string | undefined {
  return useAuth().user?.id;
}

function requireActorID(actorID: string | undefined): string {
  if (!actorID) throw new Error('authenticated user is required for memory actions');
  return actorID;
}

export function useMemoryFeedback() {
  const workspaceID = useWorkspaceQueryKey();
  const actorID = useMemoryActorID();
  const invalidate = useInvalidateMemory();
  return useMutation({
    mutationKey: [...memoryKeys.all(workspaceID), 'feedback'],
    mutationFn: (input: FeedbackMemoryInput) =>
      feedbackMemory({ ...input, actorID: requireActorID(actorID) }),
    onSuccess: (_response, input) => invalidate(input.memoryID),
  });
}

export function useArchiveMemory() {
  const workspaceID = useWorkspaceQueryKey();
  const actorID = useMemoryActorID();
  const invalidate = useInvalidateMemory();
  return useMutation({
    mutationKey: [...memoryKeys.all(workspaceID), 'archive'],
    mutationFn: (input: VersionedMemoryAction) =>
      archiveMemory({ ...input, actorID: requireActorID(actorID) }),
    onSuccess: (_response, input) => invalidate(input.memoryID),
  });
}

export function useRestoreMemory() {
  const workspaceID = useWorkspaceQueryKey();
  const actorID = useMemoryActorID();
  const invalidate = useInvalidateMemory();
  return useMutation({
    mutationKey: [...memoryKeys.all(workspaceID), 'restore'],
    mutationFn: (input: VersionedMemoryAction) =>
      restoreMemory({ ...input, actorID: requireActorID(actorID) }),
    onSuccess: (_response, input) => invalidate(input.memoryID),
  });
}

export function useForgetMemory() {
  const workspaceID = useWorkspaceQueryKey();
  const actorID = useMemoryActorID();
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: [...memoryKeys.all(workspaceID), 'forget'],
    mutationFn: (input: ForgetMemoryInput) =>
      forgetMemory({ ...input, actorID: requireActorID(actorID) }),
    onSuccess: async (_response, input) => {
      queryClient.removeQueries({
        queryKey: memoryKeys.detail(workspaceID, input.memoryID),
      });
      await queryClient.invalidateQueries({ queryKey: memoryKeys.lists(workspaceID) });
    },
  });
}

export function useMemoryRelations(
  memoryID: string | undefined,
  params: ListMemoryRelationsParams = {},
) {
  const workspaceID = useWorkspaceQueryKey();
  const enabled = useMemoryReadEnabled();
  return useQuery({
    queryKey: memoryKeys.relations(workspaceID, memoryID ?? '', params),
    queryFn: () => listMemoryRelations(memoryID!, params),
    enabled: enabled && !!memoryID,
  });
}

export function useCreateMemoryRelation() {
  const workspaceID = useWorkspaceQueryKey();
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: [...memoryKeys.all(workspaceID), 'create-relation'],
    mutationFn: (request: CreateMemoryRelationRequest) => createMemoryRelation(request),
    onSuccess: async (_relation, request) => {
      // A new edge changes the superseded markers in lists and the relation
      // panels of both endpoints, so refresh every affected projection.
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: memoryKeys.lists(workspaceID) }),
        queryClient.invalidateQueries({
          queryKey: memoryKeys.detail(workspaceID, request.source_id),
        }),
        queryClient.invalidateQueries({
          queryKey: memoryKeys.detail(workspaceID, request.target_id),
        }),
        queryClient.invalidateQueries({
          queryKey: memoryKeys.relationsAll(workspaceID),
        }),
      ]);
    },
  });
}
