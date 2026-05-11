/**
 * Placeholder kept around for future use. The current Explorer page renders
 * its own sidebar (FolderTree) directly. When we re-introduce a generic
 * AppShell, this sidebar should hold only the FolderTree (no navigation
 * items — those were removed per product direction "做像普通网盘").
 */
import { Logo } from './Logo';

export function Sidebar() {
  return (
    <aside className="hidden md:flex h-screen w-60 flex-col border-r border-border bg-bg-subtle">
      <div className="px-4 h-12 flex items-center border-b border-border">
        <Logo />
      </div>
      <div className="flex-1 overflow-y-auto" />
    </aside>
  );
}
