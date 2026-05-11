import * as React from 'react';
import ReactDOM from 'react-dom';
import { cn } from '@/lib/cn';

export interface ContextMenuItem {
  key: string;
  label: string;
  shortcut?: string;
  destructive?: boolean;
  onSelect: () => void;
  separatorAbove?: boolean;
  disabled?: boolean;
}

export interface ContextMenuState {
  x: number;
  y: number;
  items: ContextMenuItem[];
}

/** Imperative hook — returns props to spread on a host + open() + the menu element. */
export function useContextMenu() {
  const [state, setState] = React.useState<ContextMenuState | null>(null);

  const open = React.useCallback((e: React.MouseEvent, items: ContextMenuItem[]) => {
    e.preventDefault();
    e.stopPropagation();
    setState({ x: e.clientX, y: e.clientY, items });
  }, []);

  const close = React.useCallback(() => setState(null), []);

  React.useEffect(() => {
    if (!state) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setState(null);
    }
    function onScroll() {
      setState(null);
    }
    window.addEventListener('keydown', onKey);
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onScroll);
    return () => {
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onScroll);
    };
  }, [state]);

  const menu = state
    ? ReactDOM.createPortal(
        <ContextMenuView state={state} onClose={close} />,
        document.body,
      )
    : null;

  return { open, close, menu, isOpen: !!state };
}

function ContextMenuView({ state, onClose }: { state: ContextMenuState; onClose: () => void }) {
  const ref = React.useRef<HTMLDivElement>(null);
  const [pos, setPos] = React.useState<{ x: number; y: number }>({ x: state.x, y: state.y });

  // Reposition if it would overflow the viewport.
  React.useLayoutEffect(() => {
    if (!ref.current) return;
    const rect = ref.current.getBoundingClientRect();
    let { x, y } = state;
    const pad = 8;
    if (x + rect.width + pad > window.innerWidth) x = window.innerWidth - rect.width - pad;
    if (y + rect.height + pad > window.innerHeight) y = window.innerHeight - rect.height - pad;
    setPos({ x: Math.max(pad, x), y: Math.max(pad, y) });
  }, [state]);

  return (
    <>
      <div
        className="fixed inset-0 z-[60]"
        onMouseDown={onClose}
        onContextMenu={(e) => {
          e.preventDefault();
          onClose();
        }}
      />
      <div
        ref={ref}
        role="menu"
        className="fixed z-[61] min-w-44 rounded-md border border-border bg-bg-panel py-1 shadow-soft animate-fade-in"
        style={{ left: pos.x, top: pos.y }}
      >
        {state.items.map((item) => (
          <React.Fragment key={item.key}>
            {item.separatorAbove && <div className="my-1 h-px bg-border" />}
            <button
              type="button"
              role="menuitem"
              disabled={item.disabled}
              onClick={() => {
                onClose();
                // defer the select so any close-driven blur/event-clean finishes
                requestAnimationFrame(() => item.onSelect());
              }}
              className={cn(
                'flex w-full items-center gap-2 px-2.5 py-1.5 text-sm rounded-sm mx-1 my-0.5',
                'transition-colors',
                item.disabled
                  ? 'text-fg-subtle cursor-not-allowed'
                  : item.destructive
                    ? 'text-danger hover:bg-danger/10'
                    : 'text-fg-muted hover:bg-bg-inset hover:text-fg',
              )}
            >
              <span className="flex-1 text-left">{item.label}</span>
              {item.shortcut && (
                <span className="text-2xs text-fg-subtle tabular-nums">{item.shortcut}</span>
              )}
            </button>
          </React.Fragment>
        ))}
      </div>
    </>
  );
}
