/** Persisted view mode in localStorage. */

export type ExplorerView = 'grid' | 'list';

const KEY = 'mem.explorer.view';

export function readView(): ExplorerView {
  try {
    const v = localStorage.getItem(KEY);
    if (v === 'grid' || v === 'list') return v;
  } catch {
    /* ignore */
  }
  return 'grid';
}

export function writeView(v: ExplorerView): void {
  try {
    localStorage.setItem(KEY, v);
  } catch {
    /* ignore */
  }
}
