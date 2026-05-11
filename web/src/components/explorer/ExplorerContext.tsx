import * as React from 'react';

/**
 * Shared state for an in-flight internal drag (selecting files/folders and
 * dragging them onto a drop target). Persists across mount/unmount of drop
 * targets via a single provider at the page root.
 */
export interface InternalDragPayload {
  // What's being dragged
  fileIds: string[];
  folderPaths: string[];
  // For UI hint
  label: string;
  // Folder this drag originated from (so we can disable dropping onto self)
  sourceFolder: string;
}

interface ExplorerCtxValue {
  // Current folder being viewed (absolute virtual path)
  currentPath: string;
  // Internal drag (move) payload
  drag: InternalDragPayload | null;
  setDrag: (p: InternalDragPayload | null) => void;
}

const Ctx = React.createContext<ExplorerCtxValue | null>(null);

export function ExplorerProvider({
  currentPath,
  children,
}: {
  currentPath: string;
  children: React.ReactNode;
}) {
  const [drag, setDrag] = React.useState<InternalDragPayload | null>(null);

  // Cancel on Esc
  React.useEffect(() => {
    if (!drag) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setDrag(null);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [drag]);

  const value = React.useMemo<ExplorerCtxValue>(
    () => ({ currentPath, drag, setDrag }),
    [currentPath, drag],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useExplorer(): ExplorerCtxValue {
  const v = React.useContext(Ctx);
  if (!v) throw new Error('useExplorer must be used inside <ExplorerProvider>');
  return v;
}
