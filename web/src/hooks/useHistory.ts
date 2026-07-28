/**
 * localStorage-backed recent search history.
 * Most-recent-first, de-duplicated (case-insensitive), capped. Survives reloads
 * and is shared across tabs via the `storage` event.
 */
import * as React from 'react';

const MAX = 12;

function read(key: string): string[] {
  try {
    const raw = localStorage.getItem(key);
    const arr = raw ? (JSON.parse(raw) as unknown) : [];
    return Array.isArray(arr) ? arr.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

export function useHistory(key: string, max = MAX) {
  const [items, setItems] = React.useState<string[]>(() => read(key));

  // Keep in sync if another tab updates the same key.
  React.useEffect(() => {
    const onStorage = (e: StorageEvent) => {
      if (e.key === key) setItems(read(key));
    };
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
  }, [key]);

  const push = React.useCallback(
    (raw: string) => {
      const v = raw.trim();
      if (!v) return;
      setItems((prev) => {
        const next = [v, ...prev.filter((x) => x.toLowerCase() !== v.toLowerCase())].slice(0, max);
        try {
          localStorage.setItem(key, JSON.stringify(next));
        } catch {
          /* quota / private mode — history is best-effort */
        }
        return next;
      });
    },
    [key, max],
  );

  const remove = React.useCallback(
    (raw: string) => {
      setItems((prev) => {
        const next = prev.filter((x) => x !== raw);
        try {
          localStorage.setItem(key, JSON.stringify(next));
        } catch {
          /* ignore */
        }
        return next;
      });
    },
    [key],
  );

  const clear = React.useCallback(() => {
    setItems([]);
    try {
      localStorage.removeItem(key);
    } catch {
      /* ignore */
    }
  }, [key]);

  return { items, push, remove, clear };
}
