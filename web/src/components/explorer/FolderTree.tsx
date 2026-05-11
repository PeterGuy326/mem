import * as React from 'react';
import { useNavigate } from 'react-router-dom';
import { ChevronRight, FolderClosed, HardDrive } from 'lucide-react';
import { cn } from '@/lib/cn';
import {
  ROOT_PATH,
  isDescendant,
  pathToDriveRoute,
  type FolderNode,
} from '@/lib/folder-tree';
import { useExplorer } from './ExplorerContext';
import { Skeleton } from '@/components/ui/Skeleton';

export interface FolderTreeProps {
  tree: FolderNode | undefined;
  loading?: boolean;
  /** External (OS-file) drop target — uploads to this folder. */
  onExternalDropToFolder: (path: string, files: FileList) => void;
  /** Internal drop target — move selection here. */
  onInternalDropToFolder: (path: string) => void;
}

export function FolderTree({
  tree,
  loading,
  onExternalDropToFolder,
  onInternalDropToFolder,
}: FolderTreeProps) {
  const { currentPath } = useExplorer();
  // Expanded paths persisted in localStorage (small QoL)
  const [expanded, setExpanded] = React.useState<Set<string>>(() => {
    try {
      const raw = localStorage.getItem('mem.tree.expanded');
      if (raw) return new Set<string>(JSON.parse(raw) as string[]);
    } catch {
      /* ignore */
    }
    // Default: root expanded
    return new Set<string>([ROOT_PATH]);
  });

  // Auto-expand ancestors of currentPath
  React.useEffect(() => {
    if (!currentPath) return;
    setExpanded((prev) => {
      const next = new Set(prev);
      next.add(ROOT_PATH);
      const parts = currentPath.split('/').filter(Boolean);
      let acc = '';
      for (const p of parts) {
        acc = acc + '/' + p;
        next.add(acc);
      }
      return next;
    });
  }, [currentPath]);

  React.useEffect(() => {
    try {
      localStorage.setItem('mem.tree.expanded', JSON.stringify(Array.from(expanded)));
    } catch {
      /* ignore */
    }
  }, [expanded]);

  function toggle(path: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  }

  if (loading || !tree) {
    return (
      <div className="p-3 space-y-2">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-6 w-full" />
        ))}
      </div>
    );
  }

  return (
    <div className="py-2 px-1.5 text-sm">
      <TreeNode
        node={tree}
        depth={0}
        expanded={expanded}
        onToggle={toggle}
        currentPath={currentPath}
        onExternalDropToFolder={onExternalDropToFolder}
        onInternalDropToFolder={onInternalDropToFolder}
        isRoot
      />
    </div>
  );
}

function TreeNode({
  node,
  depth,
  expanded,
  onToggle,
  currentPath,
  onExternalDropToFolder,
  onInternalDropToFolder,
  isRoot,
}: {
  node: FolderNode;
  depth: number;
  expanded: Set<string>;
  onToggle: (path: string) => void;
  currentPath: string;
  onExternalDropToFolder: (path: string, files: FileList) => void;
  onInternalDropToFolder: (path: string) => void;
  isRoot?: boolean;
}) {
  const navigate = useNavigate();
  const { drag, setDrag } = useExplorer();
  const isOpen = expanded.has(node.path);
  const isActive = currentPath === node.path;
  const hasChildren = node.children.length > 0;
  const [hovered, setHovered] = React.useState(false);

  // Determine if this node is a valid internal drop target
  const internalDropAllowed = (() => {
    if (!drag) return false;
    if (drag.sourceFolder === node.path) return false; // already here
    // Disallow dropping a folder into itself or descendant
    for (const fp of drag.folderPaths) {
      if (isDescendant(node.path, fp)) return false;
    }
    return true;
  })();

  function onDragOver(e: React.DragEvent) {
    // External (OS) file
    if (e.dataTransfer.types.includes('Files')) {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
      setHovered(true);
      return;
    }
    // Internal move
    if (drag) {
      if (!internalDropAllowed) {
        e.dataTransfer.dropEffect = 'none';
        return;
      }
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      setHovered(true);
    }
  }

  function onDragLeave() {
    setHovered(false);
  }

  function onDrop(e: React.DragEvent) {
    setHovered(false);
    if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
      e.preventDefault();
      onExternalDropToFolder(node.path, e.dataTransfer.files);
      return;
    }
    if (drag && internalDropAllowed) {
      e.preventDefault();
      onInternalDropToFolder(node.path);
      setDrag(null);
    }
  }

  const Icon = isRoot ? HardDrive : FolderClosed;

  return (
    <div>
      <div
        onClick={() => navigate(pathToDriveRoute(node.path))}
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
        className={cn(
          'group flex items-center gap-1 rounded-md px-1.5 h-7 cursor-pointer select-none',
          'transition-colors',
          isActive ? 'bg-accent/10 text-accent' : 'text-fg-muted hover:bg-bg-inset hover:text-fg',
          hovered &&
            (drag
              ? internalDropAllowed
                ? 'bg-accent/15 ring-1 ring-accent/50'
                : ''
              : 'bg-accent/10 ring-1 ring-dashed ring-accent/60'),
        )}
        style={{ paddingLeft: 6 + depth * 12 }}
      >
        <button
          type="button"
          aria-label={isOpen ? '收起' : '展开'}
          onClick={(e) => {
            e.stopPropagation();
            if (hasChildren) onToggle(node.path);
          }}
          className={cn(
            'flex-none h-4 w-4 grid place-items-center rounded',
            hasChildren ? 'text-fg-subtle hover:text-fg' : 'invisible',
          )}
        >
          <ChevronRight
            className={cn('h-3 w-3 transition-transform', isOpen && 'rotate-90')}
          />
        </button>
        <Icon className={cn('h-3.5 w-3.5 flex-none', isActive ? 'text-accent' : 'text-fg-subtle')} />
        <span className="truncate flex-1 text-xs">{node.name}</span>
        {node.fileCount > 0 && (
          <span className="text-2xs text-fg-subtle tabular-nums">{node.fileCount}</span>
        )}
      </div>
      {isOpen && hasChildren && (
        <div>
          {node.children.map((c) => (
            <TreeNode
              key={c.path}
              node={c}
              depth={depth + 1}
              expanded={expanded}
              onToggle={onToggle}
              currentPath={currentPath}
              onExternalDropToFolder={onExternalDropToFolder}
              onInternalDropToFolder={onInternalDropToFolder}
            />
          ))}
        </div>
      )}
    </div>
  );
}
