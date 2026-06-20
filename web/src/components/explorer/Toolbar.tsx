import * as React from 'react';
import { FolderPlus, Upload, LayoutGrid, List } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { cn } from '@/lib/cn';
import type { ExplorerView } from './viewMode';
import { useT } from '@/i18n';

export interface ToolbarProps {
  onNewFolder: () => void;
  onUploadClick: () => void;
  view: ExplorerView;
  onViewChange: (v: ExplorerView) => void;
}

export function Toolbar({ onNewFolder, onUploadClick, view, onViewChange }: ToolbarProps) {
  const { t } = useT();
  return (
    <div className="flex items-center gap-2 px-4 h-11 border-b border-border bg-bg-subtle/40">
      <Button variant="ghost" size="sm" onClick={onNewFolder}>
        <FolderPlus className="h-3.5 w-3.5" />
        {t('drive.newFolder')}
      </Button>
      <Button variant="ghost" size="sm" onClick={onUploadClick}>
        <Upload className="h-3.5 w-3.5" />
        {t('drive.upload')}
      </Button>
      <div className="ml-auto flex items-center gap-1 rounded-md border border-border bg-bg-subtle p-0.5">
        <ViewButton
          active={view === 'grid'}
          onClick={() => onViewChange('grid')}
          icon={<LayoutGrid className="h-3.5 w-3.5" />}
          label={t('drive.grid')}
        />
        <ViewButton
          active={view === 'list'}
          onClick={() => onViewChange('list')}
          icon={<List className="h-3.5 w-3.5" />}
          label={t('drive.list')}
        />
      </div>
    </div>
  );
}

function ViewButton({
  active,
  onClick,
  icon,
  label,
}: {
  active: boolean;
  onClick: () => void;
  icon: React.ReactNode;
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      className={cn(
        'h-6 px-2 rounded text-xs inline-flex items-center gap-1 transition-colors',
        active ? 'bg-bg-panel text-fg shadow-soft' : 'text-fg-muted hover:text-fg',
      )}
    >
      {icon}
    </button>
  );
}
