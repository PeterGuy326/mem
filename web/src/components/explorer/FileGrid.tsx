import * as React from 'react';
import {
  FolderClosed,
  Image as ImageIcon,
  FileText,
  Music,
  FileQuestion,
} from 'lucide-react';
import { cn } from '@/lib/cn';
import { AuthedImage } from '@/components/ui/AuthedImage';
import type { FileKind, IndexStatus, MemFile } from '@/lib/types';
import type { FolderNode } from '@/lib/folder-tree';
import { useExplorer } from './ExplorerContext';
import { RenameInput } from './RenameInput';
import type { ContextMenuItem } from './ContextMenu';
import { useT, tt } from '@/i18n';

export interface FileGridProps {
  folders: FolderNode[];
  files: MemFile[];
  selectedKeys: Set<string>;
  /** Optional "new folder" pending entry rendered first (inline rename). */
  pendingNewFolder: boolean;
  onCommitNewFolder: (name: string) => void;
  onCancelNewFolder: () => void;
  /** Inline rename mode. */
  renameKey: string | null;
  onCommitRename: (key: string, next: string) => void;
  onCancelRename: () => void;
  /** Item-level events. */
  onItemMouseDown: (key: string, e: React.MouseEvent) => void;
  onItemDoubleClick: (key: string) => void;
  onItemContextMenu: (key: string, e: React.MouseEvent, items: ContextMenuItem[]) => void;
  buildItemMenuItems: (key: string) => ContextMenuItem[];
  /** Drag for internal move. */
  onItemDragStart: (key: string, e: React.DragEvent) => void;
  onItemDragEnd: () => void;
  /** Drop external files onto folder card. */
  onExternalDropToFolder: (path: string, files: FileList) => void;
  /** Drop internal selection onto folder card. */
  onInternalDropToFolder: (path: string) => void;
  /** Pending-uploading placeholder cards. */
  uploading: { name: string }[];
}

export function FileGrid(props: FileGridProps) {
  const { t } = useT();
  void t;
  const {
    folders,
    files,
    selectedKeys,
    pendingNewFolder,
    onCommitNewFolder,
    onCancelNewFolder,
    renameKey,
    onCommitRename,
    onCancelRename,
    onItemMouseDown,
    onItemDoubleClick,
    onItemContextMenu,
    buildItemMenuItems,
    onItemDragStart,
    onItemDragEnd,
    onExternalDropToFolder,
    onInternalDropToFolder,
    uploading,
  } = props;

  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-4 px-6 py-5">
      {pendingNewFolder && (
        <div className="flex flex-col items-center text-center gap-2 p-2 rounded-lg border border-dashed border-accent/40 bg-accent/5">
          <FolderClosed className="h-12 w-12 text-fg-subtle" />
          <RenameInput
            initial={tt('drive.untitledFolder')}
            placeholder={tt('drive.folderName')}
            preserveExtension={false}
            onCommit={onCommitNewFolder}
            onCancel={onCancelNewFolder}
          />
        </div>
      )}

      {folders.map((f) => (
        <FolderCard
          key={f.path}
          folder={f}
          selected={selectedKeys.has(f.path)}
          renaming={renameKey === f.path}
          onMouseDown={(e) => onItemMouseDown(f.path, e)}
          onDoubleClick={() => onItemDoubleClick(f.path)}
          onContextMenu={(e) => onItemContextMenu(f.path, e, buildItemMenuItems(f.path))}
          onDragStart={(e) => onItemDragStart(f.path, e)}
          onDragEnd={onItemDragEnd}
          onCommitRename={(next) => onCommitRename(f.path, next)}
          onCancelRename={onCancelRename}
          onExternalDropToFolder={onExternalDropToFolder}
          onInternalDropToFolder={onInternalDropToFolder}
        />
      ))}

      {files.map((f) => (
        <FileCard
          key={f.id}
          file={f}
          selected={selectedKeys.has(f.id)}
          renaming={renameKey === f.id}
          onMouseDown={(e) => onItemMouseDown(f.id, e)}
          onDoubleClick={() => onItemDoubleClick(f.id)}
          onContextMenu={(e) => onItemContextMenu(f.id, e, buildItemMenuItems(f.id))}
          onDragStart={(e) => onItemDragStart(f.id, e)}
          onDragEnd={onItemDragEnd}
          onCommitRename={(next) => onCommitRename(f.id, next)}
          onCancelRename={onCancelRename}
        />
      ))}

      {uploading.map((u, i) => (
        <div
          key={`upl-${i}`}
          className="flex flex-col items-center text-center gap-2 p-2 rounded-lg border border-dashed border-border bg-bg-subtle/40"
        >
          <div className="h-20 w-full rounded-md bg-bg-inset grid place-items-center">
            <div className="h-5 w-5 rounded-full border-2 border-accent border-r-transparent animate-spin" />
          </div>
          <div className="text-xs text-fg-muted truncate w-full" title={u.name}>
            {u.name}
          </div>
          <div className="text-2xs text-fg-subtle">{tt('drive.uploading')}</div>
        </div>
      ))}
    </div>
  );
}

function FolderCard({
  folder,
  selected,
  renaming,
  onMouseDown,
  onDoubleClick,
  onContextMenu,
  onDragStart,
  onDragEnd,
  onCommitRename,
  onCancelRename,
  onExternalDropToFolder,
  onInternalDropToFolder,
}: {
  folder: FolderNode;
  selected: boolean;
  renaming: boolean;
  onMouseDown: (e: React.MouseEvent) => void;
  onDoubleClick: () => void;
  onContextMenu: (e: React.MouseEvent) => void;
  onDragStart: (e: React.DragEvent) => void;
  onDragEnd: () => void;
  onCommitRename: (next: string) => void;
  onCancelRename: () => void;
  onExternalDropToFolder: (path: string, files: FileList) => void;
  onInternalDropToFolder: (path: string) => void;
}) {
  const { drag } = useExplorer();
  const [dropHover, setDropHover] = React.useState(false);
  const internalAllowed = !!drag && drag.sourceFolder !== folder.path;

  function onDragOver(e: React.DragEvent) {
    if (e.dataTransfer.types.includes('Files')) {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
      setDropHover(true);
      return;
    }
    if (drag && internalAllowed) {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      setDropHover(true);
    }
  }

  function onDrop(e: React.DragEvent) {
    setDropHover(false);
    if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
      e.preventDefault();
      e.stopPropagation();
      onExternalDropToFolder(folder.path, e.dataTransfer.files);
      return;
    }
    if (drag && internalAllowed) {
      e.preventDefault();
      e.stopPropagation();
      onInternalDropToFolder(folder.path);
    }
  }

  return (
    <div
      draggable={!renaming}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onMouseDown={onMouseDown}
      onDoubleClick={onDoubleClick}
      onContextMenu={onContextMenu}
      onDragOver={onDragOver}
      onDragLeave={() => setDropHover(false)}
      onDrop={onDrop}
      className={cn(
        'flex flex-col items-center text-center gap-2 p-2 rounded-lg cursor-default select-none',
        'border transition-colors',
        selected
          ? 'border-accent/60 bg-accent/10'
          : 'border-transparent hover:bg-bg-inset/60 hover:border-border',
        dropHover && 'ring-2 ring-dashed ring-accent/70 bg-accent/10',
      )}
      title={`${folder.name} · ${tt('drive.itemsN', { n: folder.fileCount })}`}
    >
      <FolderClosed className="h-14 w-14 text-accent/80" strokeWidth={1.4} />
      {renaming ? (
        <RenameInput
          initial={folder.name}
          preserveExtension={false}
          onCommit={onCommitRename}
          onCancel={onCancelRename}
        />
      ) : (
        <div className="text-xs text-fg truncate w-full" title={folder.name}>
          {folder.name}
        </div>
      )}
      <div className="text-2xs text-fg-subtle">{tt('drive.itemsN', { n: folder.fileCount })}</div>
    </div>
  );
}

function FileCard({
  file,
  selected,
  renaming,
  onMouseDown,
  onDoubleClick,
  onContextMenu,
  onDragStart,
  onDragEnd,
  onCommitRename,
  onCancelRename,
}: {
  file: MemFile;
  selected: boolean;
  renaming: boolean;
  onMouseDown: (e: React.MouseEvent) => void;
  onDoubleClick: () => void;
  onContextMenu: (e: React.MouseEvent) => void;
  onDragStart: (e: React.DragEvent) => void;
  onDragEnd: () => void;
  onCommitRename: (next: string) => void;
  onCancelRename: () => void;
}) {
  return (
    <div
      draggable={!renaming}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onMouseDown={onMouseDown}
      onDoubleClick={onDoubleClick}
      onContextMenu={onContextMenu}
      className={cn(
        'flex flex-col items-center text-center gap-2 p-2 rounded-lg cursor-default select-none',
        'border transition-colors',
        selected
          ? 'border-accent/60 bg-accent/10'
          : 'border-transparent hover:bg-bg-inset/60 hover:border-border',
      )}
      title={file.name}
    >
      <div className="relative w-full aspect-square overflow-hidden rounded-md bg-bg-inset grid place-items-center">
        {file.kind === 'image' ? (
          <AuthedImage
            fileId={file.id}
            alt={file.name}
            fallback={<KindIcon kind={file.kind} />}
          />
        ) : (
          <KindIcon kind={file.kind} />
        )}
        {file.index_status !== 'done' && <StatusOverlay status={file.index_status} />}
      </div>
      {renaming ? (
        <RenameInput initial={file.name} onCommit={onCommitRename} onCancel={onCancelRename} />
      ) : (
        <div className="text-xs text-fg truncate w-full" title={file.name}>
          {file.name}
        </div>
      )}
    </div>
  );
}

function KindIcon({ kind }: { kind: FileKind }) {
  const cls = 'h-12 w-12 text-fg-subtle';
  if (kind === 'image') return <ImageIcon className={cls} strokeWidth={1.4} />;
  if (kind === 'audio') return <Music className={cls} strokeWidth={1.4} />;
  if (kind === 'pdf' || kind === 'doc' || kind === 'text')
    return <FileText className={cls} strokeWidth={1.4} />;
  return <FileQuestion className={cls} strokeWidth={1.4} />;
}

function StatusOverlay({ status }: { status: IndexStatus }) {
  const text = tt(`status.${status}`);
  const tone = status === 'failed' ? 'bg-danger/80' : 'bg-bg/70';
  return (
    <div
      className={cn('absolute bottom-1 right-1 rounded px-1.5 py-0.5 text-2xs text-fg', tone)}
    >
      {text}
    </div>
  );
}
