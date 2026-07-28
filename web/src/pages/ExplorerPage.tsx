import * as React from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Upload, FolderOpen } from 'lucide-react';
import { toast } from 'sonner';
import { cn } from '@/lib/cn';
import {
  ROOT_PATH,
  buildCrumbs,
  driveRouteToPath,
  isDescendant,
  listChildren,
  pathToDriveRoute,
  type FolderNode,
} from '@/lib/folder-tree';
import {
  useCreateFolder,
  useDeleteFile,
  useDeleteFolder,
  useFolderTree,
  useMoveFile,
  useMoveFolder,
  useRenameFile,
  useRenameFolder,
  useUpload,
  useFilesByPath,
} from '@/hooks/useFiles';
import {
  ExplorerProvider,
  useExplorer,
  type InternalDragPayload,
} from '@/components/explorer/ExplorerContext';
import { useExplorerSelection } from '@/components/explorer/useExplorerSelection';
import { useContextMenu, type ContextMenuItem } from '@/components/explorer/ContextMenu';
import { FileGrid } from '@/components/explorer/FileGrid';
import { FileList } from '@/components/explorer/FileList';
import { Toolbar } from '@/components/explorer/Toolbar';
import { Breadcrumb } from '@/components/explorer/Breadcrumb';
import { FolderTree } from '@/components/explorer/FolderTree';
import { TopBar } from '@/components/layout/TopBar';
import { Skeleton } from '@/components/ui/Skeleton';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Button } from '@/components/ui/Button';
import { readView, writeView, type ExplorerView } from '@/components/explorer/viewMode';
import { downloadFile } from '@/lib/api';
import type { MemFile } from '@/lib/types';
import { useT, tt } from '@/i18n';

/** Outer page handles current path from URL. Inner page does the work. */
export function ExplorerPage() {
  const params = useParams<{ '*': string }>();
  const currentPath = driveRouteToPath(params['*']);
  return (
    <ExplorerProvider currentPath={currentPath}>
      <ExplorerLayout currentPath={currentPath} />
    </ExplorerProvider>
  );
}

function ExplorerLayout({ currentPath }: { currentPath: string }) {
  const { t } = useT();
  const navigate = useNavigate();
  const treeQ = useFolderTree();
  const { data: listData, isLoading: listLoading } = useFilesByPath(currentPath);

  // We get direct children of currentPath from /files?path=...; sub-folder list
  // comes from the global tree.
  const { folders, files } = React.useMemo(() => {
    const filesAll: MemFile[] = listData?.files ?? [];
    const tree = treeQ.data?.tree;
    if (!tree) return { folders: [] as FolderNode[], files: filesAll };
    const all = listChildren(tree, filesAll, currentPath);
    return { folders: all.folders, files: filesAll };
  }, [treeQ.data, listData, currentPath]);

  // selection — single set keyed by string (file id or folder path)
  const selection = useExplorerSelection<string>();
  const orderedKeys = React.useMemo<string[]>(
    () => [...folders.map((f) => f.path), ...files.map((f) => f.id)],
    [folders, files],
  );

  // Clear selection when entering a folder
  React.useEffect(() => {
    selection.clear();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentPath]);

  // view mode
  const [view, setView] = React.useState<ExplorerView>(() => readView());
  React.useEffect(() => writeView(view), [view]);

  // pending new-folder slot + rename
  const [pendingNewFolder, setPendingNewFolder] = React.useState(false);
  const [renameKey, setRenameKey] = React.useState<string | null>(null);

  // uploading placeholders (one per in-flight file, by name)
  const [uploading, setUploading] = React.useState<{ name: string }[]>([]);
  const [uploadProgress, setUploadProgress] = React.useState<number>(0); // 0..1

  // mutations
  const createFolder = useCreateFolder();
  const renameFolder = useRenameFolder();
  const moveFolder = useMoveFolder();
  const deleteFolder = useDeleteFolder();
  const renameFile = useRenameFile();
  const moveFile = useMoveFile();
  const deleteFile = useDeleteFile();
  const upload = useUpload();

  // delete confirm
  const [confirmDelete, setConfirmDelete] = React.useState<null | {
    files: string[];
    folders: string[];
  }>(null);

  // hidden file input for "upload" button
  const fileInputRef = React.useRef<HTMLInputElement>(null);

  // ---------- helpers ----------
  const isFileKey = React.useCallback(
    (key: string): MemFile | undefined => files.find((f) => f.id === key),
    [files],
  );
  const isFolderKey = React.useCallback(
    (key: string): FolderNode | undefined => folders.find((f) => f.path === key),
    [folders],
  );

  // ---------- external upload to a folder ----------
  const handleExternalDropTo = React.useCallback(
    async (targetPath: string, fl: FileList) => {
      const items = Array.from(fl);
      if (!items.length) return;
      setUploading((prev) => [...prev, ...items.map((f) => ({ name: f.name }))]);
      setUploadProgress(0);
      const total = items.length;
      let done = 0;
      try {
        // Upload one-by-one so progress feels real
        for (const f of items) {
          await upload.mutateAsync({ files: [f], path: targetPath });
          done += 1;
          setUploadProgress(done / total);
        }
        toast.success(
          tt('toast.uploaded', { n: total, path: pretty(targetPath) }),
          { description: tt('toast.uploadedDesc') },
        );
      } catch (err) {
        toast.error(tt('toast.uploadFailed'), {
          description: err instanceof Error ? err.message : tt('toast.retryLater'),
        });
      } finally {
        setUploading([]);
        setUploadProgress(0);
      }
    },
    [upload],
  );

  // ---------- click ----------
  const onItemMouseDown = React.useCallback(
    (key: string, e: React.MouseEvent) => {
      if (e.button === 2) {
        // right-click: select if not already
        if (!selection.isSelected(key)) {
          selection.select(key);
        }
        return;
      }
      // If clicking on a selected item with no modifiers, leave selection alone
      // (so drag works); otherwise update.
      if (!e.shiftKey && !e.metaKey && !e.ctrlKey && selection.isSelected(key)) {
        return;
      }
      selection.onItemClick(key, e, orderedKeys);
    },
    [orderedKeys, selection],
  );

  const onItemDoubleClick = React.useCallback(
    (key: string) => {
      const folder = isFolderKey(key);
      if (folder) {
        navigate(pathToDriveRoute(folder.path));
        return;
      }
      const file = isFileKey(key);
      if (file) {
        navigate(`/files/${file.id}`);
      }
    },
    [isFolderKey, isFileKey, navigate],
  );

  // ---------- internal drag ----------
  const { drag, setDrag } = useExplorer();

  const onItemDragStart = React.useCallback(
    (key: string, e: React.DragEvent) => {
      // Build payload from selection (or just this item)
      let keys = Array.from(selection.selected);
      if (!keys.includes(key)) {
        keys = [key];
        selection.select(key);
      }
      const fileIds: string[] = [];
      const folderPaths: string[] = [];
      const folderIds: Record<string, string> = {};
      for (const k of keys) {
        if (isFileKey(k)) fileIds.push(k);
        else {
          const folder = isFolderKey(k);
          if (folder) {
            folderPaths.push(k);
            if (folder.id) folderIds[k] = folder.id;
          }
        }
      }
      const label =
        keys.length === 1
          ? `${(isFolderKey(keys[0]!)?.name ?? isFileKey(keys[0]!)?.name) ?? ''}`
          : tt('drive.selectedN', { n: keys.length });
      const payload: InternalDragPayload = {
        fileIds,
        folderPaths,
        folderIds,
        label,
        sourceFolder: currentPath,
      };
      setDrag(payload);
      e.dataTransfer.effectAllowed = 'move';
      try {
        e.dataTransfer.setData('application/x-mem-internal', JSON.stringify(payload));
      } catch {
        /* some browsers complain — ignore */
      }
      // Use a transparent ghost; we render our own
      const ghost = document.createElement('canvas');
      ghost.width = 1;
      ghost.height = 1;
      try {
        e.dataTransfer.setDragImage(ghost, 0, 0);
      } catch {
        /* ignore */
      }
    },
    [currentPath, isFileKey, isFolderKey, selection, setDrag],
  );

  const onItemDragEnd = React.useCallback(() => {
    setDrag(null);
  }, [setDrag]);

  // ---------- internal drop ----------
  const handleInternalDropTo = React.useCallback(
    async (targetPath: string) => {
      if (!drag) return;
      const allowed = !drag.folderPaths.some((fp) => isDescendant(targetPath, fp));
      if (!allowed) {
        toast.error(tt('toast.noFolderIntoItself'));
        return;
      }
      if (drag.sourceFolder === targetPath && drag.folderPaths.length === 0) {
        return; // no-op
      }
      try {
        await Promise.all([
          ...drag.fileIds.map((id) => moveFile.mutateAsync({ id, targetPath })),
          ...drag.folderPaths.flatMap((p) => {
            const id = drag.folderIds[p];
            return id ? [moveFolder.mutateAsync({ id, newParent: targetPath })] : [];
          }),
        ]);
        toast.success(
          tt('toast.movedN', { n: drag.fileIds.length + drag.folderPaths.length, path: pretty(targetPath) }),
        );
      } catch (err) {
        toast.error(tt('toast.moveFailed'), {
          description: err instanceof Error ? err.message : undefined,
        });
      } finally {
        setDrag(null);
      }
    },
    [drag, moveFile, moveFolder, setDrag],
  );

  // ---------- context menu ----------
  const ctx = useContextMenu();

  const buildItemMenuItems = React.useCallback(
    (key: string): ContextMenuItem[] => {
      const folder = isFolderKey(key);
      const file = isFileKey(key);
      if (folder) {
        return [
          {
            key: 'open',
            label: tt('action.open'),
            onSelect: () => navigate(pathToDriveRoute(folder.path)),
          },
          {
            key: 'rename',
            label: tt('action.rename'),
            shortcut: 'F2',
            onSelect: () => setRenameKey(folder.path),
          },
          {
            key: 'delete',
            label: tt('common.delete'),
            shortcut: 'Del',
            destructive: true,
            separatorAbove: true,
            onSelect: () => setConfirmDelete({ files: [], folders: [folder.path] }),
          },
        ];
      }
      if (file) {
        return [
          {
            key: 'open',
            label: tt('action.open'),
            onSelect: () => navigate(`/files/${file.id}`),
          },
          {
            key: 'download',
            label: tt('common.download'),
            onSelect: () => {
              downloadFile(file.id, file.name).catch(() => toast.error(tt('toast.downloadFailed')));
            },
          },
          {
            key: 'rename',
            label: tt('action.rename'),
            shortcut: 'F2',
            onSelect: () => setRenameKey(file.id),
          },
          {
            key: 'delete',
            label: tt('common.delete'),
            shortcut: 'Del',
            destructive: true,
            separatorAbove: true,
            onSelect: () => setConfirmDelete({ files: [file.id], folders: [] }),
          },
        ];
      }
      return [];
    },
    [isFileKey, isFolderKey, navigate],
  );

  const emptyAreaMenuItems = React.useCallback(
    (): ContextMenuItem[] => [
      {
        key: 'new-folder',
        label: tt('drive.newFolder'),
        onSelect: () => setPendingNewFolder(true),
      },
      {
        key: 'upload',
        label: tt('drive.upload'),
        onSelect: () => fileInputRef.current?.click(),
      },
    ],
    [],
  );

  // ---------- rename commits ----------
  const onCommitRename = React.useCallback(
    async (key: string, next: string) => {
      const folder = isFolderKey(key);
      const file = isFileKey(key);
      setRenameKey(null);
      try {
        if (folder?.id) {
          await renameFolder.mutateAsync({ id: folder.id, name: next });
          toast.success(tt('toast.renamedFolder'));
        } else if (file) {
          await renameFile.mutateAsync({ id: file.id, name: next });
          toast.success(tt('toast.renamed'));
        }
      } catch (err) {
        toast.error(tt('toast.renameFailed'), {
          description: err instanceof Error ? err.message : undefined,
        });
      }
    },
    [isFolderKey, isFileKey, renameFolder, renameFile],
  );

  const onCommitNewFolder = React.useCallback(
    async (name: string) => {
      setPendingNewFolder(false);
      try {
        await createFolder.mutateAsync({ path: currentPath, name });
        toast.success(tt('toast.createdFolder', { name }));
      } catch (err) {
        toast.error(tt('toast.createFailed'), {
          description: err instanceof Error ? err.message : undefined,
        });
      }
    },
    [createFolder, currentPath],
  );

  // ---------- delete commit ----------
  const performDelete = React.useCallback(async () => {
    if (!confirmDelete) return;
    try {
      await Promise.all([
        ...confirmDelete.files.map((id) => deleteFile.mutateAsync(id)),
        ...confirmDelete.folders.flatMap((p) => {
          const id = isFolderKey(p)?.id;
          return id ? [deleteFolder.mutateAsync({ id })] : [];
        }),
      ]);
      toast.success(tt('toast.deleted'));
      selection.clear();
    } catch (err) {
      toast.error(tt('toast.deleteFailed'), {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setConfirmDelete(null);
    }
  }, [confirmDelete, deleteFile, deleteFolder, isFolderKey, selection]);

  // ---------- keyboard ----------
  React.useEffect(() => {
    function onKey(e: KeyboardEvent) {
      // Skip if focus is in an editable element (inputs etc.)
      const ae = document.activeElement as HTMLElement | null;
      if (ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA' || ae.isContentEditable)) {
        return;
      }
      if (e.key === 'Escape') {
        if (renameKey) {
          setRenameKey(null);
          return;
        }
        if (pendingNewFolder) {
          setPendingNewFolder(false);
          return;
        }
        if (selection.selected.size > 0) {
          selection.clear();
        }
        return;
      }
      if (e.key === 'Delete' || e.key === 'Backspace') {
        if (selection.selected.size === 0) return;
        const fileIds: string[] = [];
        const folderPaths: string[] = [];
        for (const k of selection.selected) {
          if (isFileKey(k)) fileIds.push(k);
          else if (isFolderKey(k)) folderPaths.push(k);
        }
        if (fileIds.length || folderPaths.length) {
          setConfirmDelete({ files: fileIds, folders: folderPaths });
          e.preventDefault();
        }
        return;
      }
      if (e.key === 'F2') {
        if (selection.selected.size === 1) {
          const only = Array.from(selection.selected)[0]!;
          setRenameKey(only);
          e.preventDefault();
        }
      }
    }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [isFileKey, isFolderKey, pendingNewFolder, renameKey, selection]);

  // ---------- main content drop zone (drop on empty area = upload to current) ----------
  const [contentDropHover, setContentDropHover] = React.useState(false);
  function onContentDragOver(e: React.DragEvent) {
    if (e.dataTransfer.types.includes('Files')) {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
      setContentDropHover(true);
    }
  }
  function onContentDragLeave(e: React.DragEvent) {
    // Only clear when leaving the host (not child elements)
    if (e.currentTarget === e.target) setContentDropHover(false);
    // safer: count of nested enter/leaves is annoying — use coords:
    const rect = (e.currentTarget as HTMLDivElement).getBoundingClientRect();
    if (
      e.clientX < rect.left ||
      e.clientX > rect.right ||
      e.clientY < rect.top ||
      e.clientY > rect.bottom
    ) {
      setContentDropHover(false);
    }
  }
  function onContentDrop(e: React.DragEvent) {
    setContentDropHover(false);
    if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
      e.preventDefault();
      void handleExternalDropTo(currentPath, e.dataTransfer.files);
    }
  }
  function onContentClickEmpty(e: React.MouseEvent) {
    if (e.target === e.currentTarget) {
      selection.clear();
    }
  }
  function onContentContextMenu(e: React.MouseEvent) {
    // Only when right-clicking the empty area itself
    if (e.target === e.currentTarget) {
      ctx.open(e, emptyAreaMenuItems());
    }
  }

  const showEmpty =
    !listLoading &&
    folders.length === 0 &&
    files.length === 0 &&
    !pendingNewFolder &&
    uploading.length === 0;

  return (
    <div className="h-screen flex flex-col bg-bg text-fg">
      <TopBar>
        <div className="h-5 w-px bg-border" aria-hidden />
        <Breadcrumb path={currentPath} onInternalDropToFolder={handleInternalDropTo} />
      </TopBar>

      {/* Two-pane */}
      <div className="flex-1 min-h-0 flex">
        <aside className="hidden md:block w-60 flex-none border-r border-border bg-bg-subtle overflow-y-auto">
          <FolderTree
            tree={treeQ.data?.tree}
            loading={treeQ.isLoading}
            onExternalDropToFolder={handleExternalDropTo}
            onInternalDropToFolder={handleInternalDropTo}
          />
        </aside>

        <main className="flex-1 min-w-0 flex flex-col overflow-hidden">
          <Toolbar
            onNewFolder={() => setPendingNewFolder(true)}
            onUploadClick={() => fileInputRef.current?.click()}
            view={view}
            onViewChange={setView}
          />

          {uploading.length > 0 && (
            <div className="h-1 w-full bg-bg-inset relative overflow-hidden">
              <div
                className="absolute inset-y-0 left-0 bg-accent transition-all"
                style={{ width: `${Math.max(8, uploadProgress * 100)}%` }}
              />
            </div>
          )}

          <div
            onDragOver={onContentDragOver}
            onDragLeave={onContentDragLeave}
            onDrop={onContentDrop}
            onClick={onContentClickEmpty}
            onContextMenu={onContentContextMenu}
            className={cn(
              'flex-1 min-h-0 overflow-y-auto relative',
              contentDropHover && 'ring-2 ring-dashed ring-accent/60 ring-inset bg-accent/5',
            )}
          >
            {contentDropHover && (
              <div className="pointer-events-none sticky top-0 z-10 mx-6 mt-4 rounded-md border border-dashed border-accent bg-bg-panel/80 backdrop-blur-sm px-4 py-2 text-sm text-accent text-center">
                {t('drive.uploadTo', { path: pretty(currentPath) })}
              </div>
            )}

            {listLoading || treeQ.isLoading ? (
              <ContentSkeleton view={view} />
            ) : showEmpty ? (
              <EmptyFolder
                path={currentPath}
                onUploadClick={() => fileInputRef.current?.click()}
                onNewFolder={() => setPendingNewFolder(true)}
              />
            ) : view === 'grid' ? (
              <FileGrid
                folders={folders}
                files={files}
                selectedKeys={selection.selected}
                pendingNewFolder={pendingNewFolder}
                onCommitNewFolder={onCommitNewFolder}
                onCancelNewFolder={() => setPendingNewFolder(false)}
                renameKey={renameKey}
                onCommitRename={onCommitRename}
                onCancelRename={() => setRenameKey(null)}
                onItemMouseDown={onItemMouseDown}
                onItemDoubleClick={onItemDoubleClick}
                onItemContextMenu={(_key, e, items) => ctx.open(e, items)}
                buildItemMenuItems={buildItemMenuItems}
                onItemDragStart={onItemDragStart}
                onItemDragEnd={onItemDragEnd}
                onExternalDropToFolder={handleExternalDropTo}
                onInternalDropToFolder={handleInternalDropTo}
                uploading={uploading}
              />
            ) : (
              <FileList
                folders={folders}
                files={files}
                selectedKeys={selection.selected}
                pendingNewFolder={pendingNewFolder}
                onCommitNewFolder={onCommitNewFolder}
                onCancelNewFolder={() => setPendingNewFolder(false)}
                renameKey={renameKey}
                onCommitRename={onCommitRename}
                onCancelRename={() => setRenameKey(null)}
                onItemMouseDown={onItemMouseDown}
                onItemDoubleClick={onItemDoubleClick}
                onItemContextMenu={(_key, e, items) => ctx.open(e, items)}
                buildItemMenuItems={buildItemMenuItems}
                onItemDragStart={onItemDragStart}
                onItemDragEnd={onItemDragEnd}
                onExternalDropToFolder={handleExternalDropTo}
                onInternalDropToFolder={handleInternalDropTo}
                uploading={uploading}
              />
            )}
          </div>
        </main>
      </div>

      {/* Floating drag badge (multi-select hint follows cursor) */}
      {drag && drag.fileIds.length + drag.folderPaths.length > 1 && (
        <DragBadge label={drag.label} />
      )}

      {/* Hidden file input */}
      <input
        ref={fileInputRef}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          if (e.target.files && e.target.files.length > 0) {
            void handleExternalDropTo(currentPath, e.target.files);
          }
          e.target.value = '';
        }}
      />

      {/* Confirm delete */}
      <ConfirmDialog
        open={!!confirmDelete}
        onOpenChange={(o) => !o && setConfirmDelete(null)}
        title={t('drive.deleteNTitle', { n: (confirmDelete?.files.length ?? 0) + (confirmDelete?.folders.length ?? 0) })}
        description={
          confirmDelete?.folders.length
            ? t('drive.deleteFolderDesc')
            : t('drive.deleteFilesDesc')
        }
        destructive
        confirmText={t('common.delete')}
        onConfirm={performDelete}
      />

      {/* Context menu portal */}
      {ctx.menu}
    </div>
  );
}

function pretty(path: string): string {
  return path === ROOT_PATH ? tt('drive.root') : path;
}

function ContentSkeleton({ view }: { view: ExplorerView }) {
  if (view === 'list') {
    return (
      <div className="px-4 py-3 space-y-2">
        {Array.from({ length: 10 }).map((_, i) => (
          <Skeleton key={i} className="h-9 w-full" />
        ))}
      </div>
    );
  }
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-4 px-6 py-5">
      {Array.from({ length: 12 }).map((_, i) => (
        <div key={i} className="flex flex-col items-center gap-2">
          <Skeleton className="aspect-square w-full rounded-md" />
          <Skeleton className="h-3 w-3/4" />
        </div>
      ))}
    </div>
  );
}

function EmptyFolder({
  path,
  onUploadClick,
  onNewFolder,
}: {
  path: string;
  onUploadClick: () => void;
  onNewFolder: () => void;
}) {
  const { t } = useT();
  const crumbs = buildCrumbs(path);
  const inside = crumbs[crumbs.length - 1]?.name ?? tt('drive.root');
  return (
    <div className="flex flex-col items-center justify-center text-center py-24 px-6 text-fg-muted">
      <FolderOpen className="h-12 w-12 text-fg-subtle mb-4" strokeWidth={1.3} />
      <div className="text-sm text-fg">{t('drive.emptyTitle', { name: inside })}</div>
      <div className="mt-1 text-xs text-fg-subtle">
        {t('drive.emptyHint')}
      </div>
      <div className="mt-5 flex items-center gap-2">
        <Button variant="secondary" size="sm" onClick={onUploadClick}>
          <Upload className="h-3.5 w-3.5" />
          {t('drive.upload')}
        </Button>
        <Button variant="ghost" size="sm" onClick={onNewFolder}>
          {t('drive.newFolder')}
        </Button>
      </div>
    </div>
  );
}

function DragBadge({ label }: { label: string }) {
  const [pos, setPos] = React.useState<{ x: number; y: number } | null>(null);
  React.useEffect(() => {
    function onMove(e: MouseEvent | DragEvent) {
      setPos({ x: e.clientX + 12, y: e.clientY + 12 });
    }
    window.addEventListener('dragover', onMove);
    return () => window.removeEventListener('dragover', onMove);
  }, []);
  if (!pos) return null;
  return (
    <div
      className="pointer-events-none fixed z-[70] rounded-md border border-accent/60 bg-bg-panel/95 px-2.5 py-1 text-xs text-accent shadow-soft"
      style={{ left: pos.x, top: pos.y }}
    >
      {label}
    </div>
  );
}
