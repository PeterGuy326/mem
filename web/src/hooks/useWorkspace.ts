import { useQuery } from '@tanstack/react-query';
import { getWorkspaceCacheKey } from '@/lib/api';
import { getCapabilities, getCurrentWorkspace, listWorkspaces } from '@/lib/handoff';

export const workspaceKeys = {
  all: ['workspaces'] as const,
  list: () => [...workspaceKeys.all, 'list'] as const,
  current: (selection = getWorkspaceCacheKey()) =>
    [...workspaceKeys.all, selection, 'current'] as const,
  capabilities: (selection = getWorkspaceCacheKey()) =>
    [...workspaceKeys.all, selection, 'capabilities'] as const,
};

export function useCapabilities() {
  const selection = getWorkspaceCacheKey();
  return useQuery({
    queryKey: workspaceKeys.capabilities(selection),
    queryFn: getCapabilities,
  });
}

export function useWorkspaces() {
  return useQuery({
    queryKey: workspaceKeys.list(),
    queryFn: listWorkspaces,
  });
}

export function useCurrentWorkspace() {
  const selection = getWorkspaceCacheKey();
  return useQuery({
    queryKey: workspaceKeys.current(selection),
    queryFn: getCurrentWorkspace,
  });
}

export function useWorkspaceQueryKey(): string {
  const capabilities = useCapabilities();
  return capabilities.data?.workspace.id ?? getWorkspaceCacheKey();
}
