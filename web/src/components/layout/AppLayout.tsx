/**
 * Layout for the non-Explorer pages (ask / faces / providers / search / file
 * detail): a global TopBar plus a scrollable outlet. ExplorerPage manages its
 * own full-height two-pane layout and renders <TopBar> directly instead.
 */
import { Outlet } from 'react-router-dom';
import { TopBar } from './TopBar';
import { AskWidget } from '@/components/ask/AskWidget';

export function AppLayout() {
  return (
    <div className="h-screen flex flex-col bg-bg text-fg">
      <TopBar />
      <div className="flex-1 min-h-0 overflow-y-auto">
        <Outlet />
      </div>
      <AskWidget />
    </div>
  );
}
