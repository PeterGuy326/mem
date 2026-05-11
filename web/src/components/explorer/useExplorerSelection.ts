import * as React from 'react';

/**
 * Generic multi-select hook with anchor-based shift-range support.
 * Items are referenced by stable string keys (id for files, path for folders).
 */
export function useExplorerSelection<TKey extends string>() {
  const [selected, setSelected] = React.useState<Set<TKey>>(new Set());
  const anchorRef = React.useRef<TKey | null>(null);

  const isSelected = React.useCallback((key: TKey) => selected.has(key), [selected]);

  const clear = React.useCallback(() => {
    setSelected(new Set());
    anchorRef.current = null;
  }, []);

  const select = React.useCallback((key: TKey) => {
    setSelected(new Set([key]));
    anchorRef.current = key;
  }, []);

  const toggle = React.useCallback((key: TKey) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
    anchorRef.current = key;
  }, []);

  /** Anchor-based range. orderedKeys is the visible order. */
  const rangeTo = React.useCallback((key: TKey, orderedKeys: TKey[]) => {
    setSelected((prev) => {
      const anchor = anchorRef.current ?? key;
      const i = orderedKeys.indexOf(anchor);
      const j = orderedKeys.indexOf(key);
      if (i < 0 || j < 0) {
        return new Set([...prev, key]);
      }
      const [lo, hi] = i < j ? [i, j] : [j, i];
      const next = new Set<TKey>(prev);
      for (let k = lo; k <= hi; k++) next.add(orderedKeys[k]!);
      return next;
    });
  }, []);

  /**
   * Unified handler — call from onMouseDown/onClick of an item.
   * - plain click: select only this
   * - meta/ctrl click: toggle
   * - shift click: extend range from anchor to here
   */
  const onItemClick = React.useCallback(
    (key: TKey, e: React.MouseEvent, orderedKeys: TKey[]) => {
      if (e.shiftKey) {
        rangeTo(key, orderedKeys);
        return;
      }
      if (e.metaKey || e.ctrlKey) {
        toggle(key);
        return;
      }
      select(key);
    },
    [rangeTo, select, toggle],
  );

  return {
    selected,
    isSelected,
    clear,
    select,
    toggle,
    rangeTo,
    onItemClick,
    setSelected,
  };
}
