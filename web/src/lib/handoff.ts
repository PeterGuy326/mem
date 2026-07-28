import { api } from './api';
import type {
  AgentTask,
  Capabilities,
  CheckpointRecord,
  CheckpointSummary,
  ResumeRequest,
  ResumeResponse,
  Workspace,
} from './types';

export interface ListTasksParams {
  scope?: string;
  limit?: number;
  after?: string;
}

export interface ListCheckpointsParams {
  scope?: string;
  limit?: number;
  before?: number;
}

function taskAPIPath(taskKey: string): string {
  return `/tasks/${encodeURIComponent(taskKey)}`;
}

export function taskPagePath(taskKey: string): string {
  return `/tasks/${encodeURIComponent(taskKey)}`;
}

export function checkpointPagePath(taskKey: string, checkpointID: string): string {
  return `${taskPagePath(taskKey)}/checkpoints/${encodeURIComponent(checkpointID)}`;
}

export function getCapabilities(): Promise<Capabilities> {
  return api.get<Capabilities>('/capabilities');
}

export function listWorkspaces(): Promise<{ workspaces: Workspace[] }> {
  return api.get<{ workspaces: Workspace[] }>('/workspaces');
}

export function getCurrentWorkspace(): Promise<Workspace> {
  return api.get<Workspace>('/workspaces/current');
}

export function listTasks(params: ListTasksParams = {}): Promise<{ tasks: AgentTask[] }> {
  return api.get<{ tasks: AgentTask[] }>('/tasks', {
    query: {
      scope: params.scope,
      limit: params.limit,
      after: params.after,
    },
  });
}

export function listCheckpoints(
  taskKey: string,
  params: ListCheckpointsParams = {},
): Promise<{ checkpoints: CheckpointSummary[] }> {
  return api.get<{ checkpoints: CheckpointSummary[] }>(
    `${taskAPIPath(taskKey)}/checkpoints`,
    {
      query: {
        scope: params.scope,
        limit: params.limit,
        before: params.before,
      },
    },
  );
}

export function getCheckpoint(
  taskKey: string,
  checkpointID: string,
  scope?: string,
): Promise<CheckpointRecord> {
  return api.get<CheckpointRecord>(
    `${taskAPIPath(taskKey)}/checkpoints/${encodeURIComponent(checkpointID)}`,
    { query: { scope } },
  );
}

export function resumeTask(taskKey: string, request: ResumeRequest = {}): Promise<ResumeResponse> {
  return api.post<ResumeResponse>(`${taskAPIPath(taskKey)}/resume`, request);
}
