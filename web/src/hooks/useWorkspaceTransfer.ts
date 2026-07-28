import { useMutation } from '@tanstack/react-query';
import type { WorkspaceImportResult } from '@/lib/types';
import {
  exportWorkspaceBundle,
  importWorkspaceBundle,
  saveWorkspaceBundle,
  WorkspaceTransferError,
  type WorkspaceBundleDownload,
} from '@/lib/workspace-transfer';

export const workspaceTransferKeys = {
  all: ['workspace-transfer'] as const,
  workspace: (workspaceID: string) => [...workspaceTransferKeys.all, workspaceID] as const,
  export: (workspaceID: string) =>
    [...workspaceTransferKeys.workspace(workspaceID), 'export'] as const,
  import: (workspaceID: string) =>
    [...workspaceTransferKeys.workspace(workspaceID), 'import'] as const,
};

export function useWorkspaceExport(workspaceID: string) {
  return useMutation<WorkspaceBundleDownload, WorkspaceTransferError, void>({
    mutationKey: workspaceTransferKeys.export(workspaceID),
    mutationFn: async () => {
      const download = await exportWorkspaceBundle(workspaceID);
      try {
        saveWorkspaceBundle(download);
      } catch (error) {
        throw new WorkspaceTransferError({
          kind: 'api',
          message:
            error instanceof Error ? error.message : 'browser could not save workspace bundle',
          code: 'workspace_bundle_save_failed',
        });
      }
      return download;
    },
  });
}

export function useWorkspaceImport(workspaceID: string) {
  return useMutation<WorkspaceImportResult, WorkspaceTransferError, File>({
    mutationKey: workspaceTransferKeys.import(workspaceID),
    mutationFn: importWorkspaceBundle,
  });
}
