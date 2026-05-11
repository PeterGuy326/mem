import * as React from 'react';
import { FolderClosed, FileText, Image as ImageIcon, Music, FileQuestion } from 'lucide-react';
import { cn } from '@/lib/cn';
import type { MemFile, FileKind } from '@/lib/types';
import type { FolderNode } from '@/lib/folder-tree';
import { formatBytes, formatRelative } from '@/lib/format';
import { useExplorer } from './ExplorerContext';
import { RenameInput } from './RenameInput';
import type { ContextMenuItem } from './ContextMenu';

export interface FileListProps {
  folders: FolderNode[];
  files: MemFile[];
  selectedKeys: Set<string>;
  pendingNewFolder: boolean;
  onCommitNewFolder: (name: string) => void;
  onCancelNewFolder: () => void;
  renameKey: string | null;
  onCommitRename: (key: string, next: string) => void;
  onCancelRename: () => void;
  onItemMouseDown: (key: string, e: React.MouseEvent) => void;
  onItemDoubleClick: (key: string) => void;
  onItemContextMenu: (key: string, e: React.MouseEvent, items: ContextMenuItem[]) => void;
  buildItemMenuItems: (key: string) => ContextMenuItem[];
  onItemDragStart: (key: string, e: React.DragEvent) => void;
  onItemDragEnd: () => void;
  onExternalDropToFolder: (path: string, files: FileList) => void;
  onInternalDropToFolder: (path: string) => void;
  uploading: { name: string }[];
}

export function FileList(props: FileListProps) {
  return (
    <div className="px-4 py-3">
      <div className="grid grid-cols-[1fr_120px_180px_120px] gap-3 px-3 py-2 text-2xs uppercase tracking-wider text-fg-subtle font-medium border-b border-border">
        <div>名称</div>
        <div>大小</div>
        <div>修改时间</div>
        <div>类型</div>
      </div>

      {props.pendingNewFolder && (
        <div className="grid grid-cols-[1fr_120px_180px_120px] gap-3 px-3 py-2 items-center bg-accent/5 rounded border border-dashed border-accent/40 my-1">
          <div className="flex items-center gap-2 min-w-0">
            <FolderClosed className="h-4 w-4 text-accent flex-none" />
            <div className="flex-1 min-w-0">
              <RenameInput
                initial="未命名文件夹"
                placeholder="文件夹名"
                preserveExtension={false}
                onCommit={props.onCommitNewFolder}
                onCancel={props.onCancelNewFolder}
              />
            </div>
          </div>
          <div className="text-xs text-fg-subtle">—</div>
          <div className="text-xs text-fg-subtle">—</div>
          <div className="text-xs text-fg-subtle">文件夹</div>
        </div>
      )}

      <div className="divide-y divide-border/60">
        {props.folders.map((f) => (
          <FolderRow
            key={f.path}
            folder={f}
            selected={props.selectedKeys.has(f.path)}
            renaming={props.renameKey === f.path}
            onMouseDown={(e) => props.onItemMouseDown(f.path, e)}
            onDoubleClick={() => props.onItemDoubleClick(f.path)}
            onContextMenu={(e) =>
              props.onItemContextMenu(f.path, e, props.buildItemMenuItems(f.path))
            }
            onDragStart={(e) => props.onItemDragStart(f.path, e)}
            onDragEnd={props.onItemDragEnd}
            onCommitRename={(next) => props.onCommitRename(f.path, next)}
            onCancelRename={props.onCancelRename}
            onExternalDropToFolder={props.onExternalDropToFolder}
            onInternalDropToFolder={props.onInternalDropToFolder}
          />
        ))}

        {props.files.map((f) => (
          <FileRow
            key={f.id}
            file={f}
            selected={props.selectedKeys.has(f.id)}
            renaming={props.renameKey === f.id}
            onMouseDown={(e) => props.onItemMouseDown(f.id, e)}
            onDoubleClick={() => props.onItemDoubleClick(f.id)}
            onContextMenu={(e) =>
              props.onItemContextMenu(f.id, e, props.buildItemMenuItems(f.id))
            }
            onDragStart={(e) => props.onItemDragStart(f.id, e)}
            onDragEnd={props.onItemDragEnd}
            onCommitRename={(next) => props.onCommitRename(f.id, next)}
            onCancelRename={props.onCancelRename}
          />
        ))}

        {props.uploading.map((u, i) => (
          <div
            key={`upl-${i}`}
            className="grid grid-cols-[1fr_120px_180px_120px] gap-3 px-3 py-2 items-center"
          >
            <div className="flex items-center gap-2 min-w-0">
              <div className="h-4 w-4 rounded-full border-2 border-accent border-r-transparent animate-spin flex-none" />
              <div className="text-xs text-fg-muted truncate">{u.name}</div>
            </div>
            <div className="text-xs text-fg-subtle">…</div>
            <div className="text-xs text-fg-subtle">现在</div>
            <div className="text-xs text-fg-subtle">上传中</div>
          </div>
        ))}
      </div>
    </div>
  );
}

function FolderRow({
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
        'grid grid-cols-[1fr_120px_180px_120px] gap-3 px-3 py-2 rounded items-center select-none cursor-default',
        'transition-colors',
        selected
          ? 'bg-accent/10 ring-1 ring-accent/40'
          : 'hover:bg-bg-inset/60',
        dropHover && 'ring-2 ring-dashed ring-accent/70 bg-accent/10',
      )}
    >
      <div className="flex items-center gap-2 min-w-0">
        <FolderClosed className="h-4 w-4 text-accent flex-none" />
        {renaming ? (
          <div className="flex-1 min-w-0">
            <RenameInput
              initial={folder.name}
              preserveExtension={false}
              onCommit={onCommitRename}
              onCancel={onCancelRename}
            />
          </div>
        ) : (
          <div className="text-sm text-fg truncate" title={folder.name}>
            {folder.name}
          </div>
        )}
      </div>
      <div className="text-xs text-fg-subtle tabular-nums">{folder.fileCount} 项</div>
      <div className="text-xs text-fg-subtle">—</div>
      <div className="text-xs text-fg-subtle">文件夹</div>
    </div>
  );
}

function FileRow({
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
        'grid grid-cols-[1fr_120px_180px_120px] gap-3 px-3 py-2 rounded items-center select-none cursor-default',
        'transition-colors',
        selected ? 'bg-accent/10 ring-1 ring-accent/40' : 'hover:bg-bg-inset/60',
      )}
    >
      <div className="flex items-center gap-2 min-w-0">
        <KindIconSmall kind={file.kind} />
        {renaming ? (
          <div className="flex-1 min-w-0">
            <RenameInput initial={file.name} onCommit={onCommitRename} onCancel={onCancelRename} />
          </div>
        ) : (
          <div className="text-sm text-fg truncate" title={file.name}>
            {file.name}
          </div>
        )}
      </div>
      <div className="text-xs text-fg-muted tabular-nums">{formatBytes(file.size)}</div>
      <div className="text-xs text-fg-muted">{formatRelative(file.updated_at)}</div>
      <div className="text-xs text-fg-subtle">{kindLabel(file.kind)}</div>
    </div>
  );
}

function KindIconSmall({ kind }: { kind: FileKind }) {
  const cls = 'h-4 w-4 text-fg-subtle flex-none';
  if (kind === 'image') return <ImageIcon className={cls} />;
  if (kind === 'audio') return <Music className={cls} />;
  if (kind === 'pdf' || kind === 'doc' || kind === 'text') return <FileText className={cls} />;
  return <FileQuestion className={cls} />;
}

function kindLabel(kind: FileKind): string {
  if (kind === 'image') return '图片';
  if (kind === 'audio') return '音频';
  if (kind === 'pdf') return 'PDF';
  if (kind === 'doc') return '文档';
  if (kind === 'text') return '文本';
  if (kind === 'video') return '视频';
  return '其它';
}
