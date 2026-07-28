import { useMutation, useQuery } from '@tanstack/react-query';
import {
  getCheckpoint,
  listCheckpoints,
  listTasks,
  resumeTask,
  type ListCheckpointsParams,
  type ListTasksParams,
} from '@/lib/handoff';
import type { ResumeRequest } from '@/lib/types';
import { useCapabilities, useWorkspaceQueryKey } from './useWorkspace';

export const taskKeys = {
  all: (workspaceID: string) => ['workspace', workspaceID, 'tasks'] as const,
  list: (workspaceID: string, params: ListTasksParams) =>
    [...taskKeys.all(workspaceID), 'list', params] as const,
  checkpoints: (
    workspaceID: string,
    taskKey: string,
    params: ListCheckpointsParams,
  ) => [...taskKeys.all(workspaceID), taskKey, 'checkpoints', params] as const,
  checkpoint: (workspaceID: string, taskKey: string, checkpointID: string) =>
    [...taskKeys.all(workspaceID), taskKey, 'checkpoint', checkpointID] as const,
};

function useHandoffReadEnabled(): boolean {
  const capabilities = useCapabilities();
  return (
    capabilities.isSuccess &&
    capabilities.data.features.handoff &&
    capabilities.data.permissions.read
  );
}

export function useTasks(params: ListTasksParams = {}) {
  const workspaceID = useWorkspaceQueryKey();
  const enabled = useHandoffReadEnabled();
  return useQuery({
    queryKey: taskKeys.list(workspaceID, params),
    queryFn: () => listTasks(params),
    enabled,
  });
}

export function useTaskCheckpoints(
  taskKey: string | undefined,
  params: ListCheckpointsParams = {},
) {
  const workspaceID = useWorkspaceQueryKey();
  const enabled = useHandoffReadEnabled();
  return useQuery({
    queryKey: taskKeys.checkpoints(workspaceID, taskKey ?? '', params),
    queryFn: () => listCheckpoints(taskKey!, params),
    enabled: enabled && !!taskKey,
  });
}

export function useCheckpoint(
  taskKey: string | undefined,
  checkpointID: string | undefined,
  scope?: string,
) {
  const workspaceID = useWorkspaceQueryKey();
  const enabled = useHandoffReadEnabled();
  return useQuery({
    queryKey: taskKeys.checkpoint(workspaceID, taskKey ?? '', checkpointID ?? ''),
    queryFn: () => getCheckpoint(taskKey!, checkpointID!, scope),
    enabled: enabled && !!taskKey && !!checkpointID,
  });
}

export function useResumeTask(taskKey: string) {
  const workspaceID = useWorkspaceQueryKey();
  return useMutation({
    mutationKey: [...taskKeys.all(workspaceID), taskKey, 'resume'],
    mutationFn: (request: ResumeRequest) => resumeTask(taskKey, request),
  });
}
