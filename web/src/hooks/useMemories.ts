import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  archiveMemory,
  feedbackMemory,
  forgetMemory,
  getMemory,
  listMemories,
  restoreMemory,
  type FeedbackMemoryInput,
  type ForgetMemoryInput,
  type ListMemoriesParams,
  type VersionedMemoryAction,
} from '@/lib/memories';
import { useAuth } from './useAuth';
import { useCapabilities, useWorkspaceQueryKey } from './useWorkspace';

export const memoryKeys = {
  all: (workspaceID: string) => ['workspace', workspaceID, 'memories'] as const,
  lists: (workspaceID: string) => [...memoryKeys.all(workspaceID), 'list'] as const,
  list: (workspaceID: string, params: Omit<ListMemoriesParams, 'cursor'>) =>
    [...memoryKeys.lists(workspaceID), params] as const,
  detail: (workspaceID: string, memoryID: string) =>
    [...memoryKeys.all(workspaceID), 'detail', memoryID] as const,
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
