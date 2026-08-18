import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { WorkspaceImportHistory, WorkspaceImportResult } from '@/lib/types';
import {
  exportWorkspaceBundle,
  importWorkspaceBundle,
  listImportHistory,
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
  importHistory: (workspaceID: string) =>
    [...workspaceTransferKeys.workspace(workspaceID), 'import-history'] as const,
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
  const queryClient = useQueryClient();
  return useMutation<WorkspaceImportResult, WorkspaceTransferError, File>({
    mutationKey: workspaceTransferKeys.import(workspaceID),
    mutationFn: importWorkspaceBundle,
    onSuccess: () => {
      // A committed import appends a workspace_imports ledger row, so the
      // history list must refresh without waiting for staleness.
      void queryClient.invalidateQueries({
        queryKey: workspaceTransferKeys.importHistory(workspaceID),
      });
    },
  });
}

/**
 * Read-only import history for one workspace, newest first. Entries are
 * disabled until the caller opts in (the TransferPage only lists history when
 * the import feature and permission gates are satisfied).
 */
export function useImportHistory(workspaceID: string, enabled: boolean) {
  return useQuery<WorkspaceImportHistory, WorkspaceTransferError>({
    queryKey: workspaceTransferKeys.importHistory(workspaceID),
    queryFn: ({ signal }) => listImportHistory({ signal }),
    enabled,
    staleTime: 30_000,
    retry: false,
  });
}
