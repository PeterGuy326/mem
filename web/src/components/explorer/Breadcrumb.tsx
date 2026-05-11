import * as React from 'react';
import { useNavigate } from 'react-router-dom';
import { ChevronRight } from 'lucide-react';
import { cn } from '@/lib/cn';
import { buildCrumbs, isDescendant, pathToDriveRoute } from '@/lib/folder-tree';
import { useExplorer } from './ExplorerContext';

export interface BreadcrumbProps {
  path: string;
  onInternalDropToFolder?: (path: string) => void;
}

export function Breadcrumb({ path, onInternalDropToFolder }: BreadcrumbProps) {
  const navigate = useNavigate();
  const crumbs = buildCrumbs(path);
  const { drag, setDrag } = useExplorer();

  return (
    <nav aria-label="面包屑" className="flex items-center gap-1 min-w-0 flex-1">
      {crumbs.map((c, i) => {
        const isLast = i === crumbs.length - 1;
        return (
          <React.Fragment key={c.path}>
            <Crumb
              name={c.name}
              path={c.path}
              isLast={isLast}
              navigate={navigate}
              onInternalDropToFolder={onInternalDropToFolder}
              dragAllowed={
                !!drag &&
                drag.sourceFolder !== c.path &&
                !drag.folderPaths.some((fp) => isDescendant(c.path, fp))
              }
              onDropEnd={() => setDrag(null)}
            />
            {!isLast && (
              <ChevronRight className="h-3.5 w-3.5 text-fg-subtle flex-none" aria-hidden />
            )}
          </React.Fragment>
        );
      })}
    </nav>
  );
}

function Crumb({
  name,
  path,
  isLast,
  navigate,
  onInternalDropToFolder,
  dragAllowed,
  onDropEnd,
}: {
  name: string;
  path: string;
  isLast: boolean;
  navigate: (to: string) => void;
  onInternalDropToFolder?: (path: string) => void;
  dragAllowed: boolean;
  onDropEnd: () => void;
}) {
  const [hovered, setHovered] = React.useState(false);

  function onDragOver(e: React.DragEvent) {
    if (!dragAllowed || !onInternalDropToFolder) return;
    if (e.dataTransfer.types.includes('Files')) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    setHovered(true);
  }

  function onDrop(e: React.DragEvent) {
    setHovered(false);
    if (!dragAllowed || !onInternalDropToFolder) return;
    e.preventDefault();
    onInternalDropToFolder(path);
    onDropEnd();
  }

  return (
    <button
      type="button"
      onClick={() => navigate(pathToDriveRoute(path))}
      onDragOver={onDragOver}
      onDragLeave={() => setHovered(false)}
      onDrop={onDrop}
      className={cn(
        'truncate max-w-[200px] text-sm rounded-md px-1.5 h-7 inline-flex items-center transition-colors',
        isLast
          ? 'text-fg font-medium'
          : 'text-fg-muted hover:text-fg hover:bg-bg-inset',
        hovered && 'bg-accent/15 ring-1 ring-accent/60 text-accent',
      )}
      aria-current={isLast ? 'page' : undefined}
      title={path}
    >
      {name}
    </button>
  );
}
