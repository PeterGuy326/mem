import * as React from 'react';
import { cn } from '@/lib/cn';

export interface RenameInputProps {
  initial: string;
  onCommit: (next: string) => void;
  onCancel: () => void;
  /** If true (default), keep the extension intact when starting a rename. */
  preserveExtension?: boolean;
  className?: string;
  placeholder?: string;
}

/** Inline-editable name input. Auto-focused, selects basename without extension. */
export function RenameInput({
  initial,
  onCommit,
  onCancel,
  preserveExtension = true,
  className,
  placeholder,
}: RenameInputProps) {
  const ref = React.useRef<HTMLInputElement>(null);
  const [value, setValue] = React.useState(initial);

  React.useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.focus();
    if (preserveExtension) {
      const dot = initial.lastIndexOf('.');
      if (dot > 0) el.setSelectionRange(0, dot);
      else el.select();
    } else {
      el.select();
    }
  }, [initial, preserveExtension]);

  function commit() {
    const next = value.trim();
    if (!next || next === initial) {
      onCancel();
      return;
    }
    // Disallow slashes in filenames
    if (next.includes('/')) {
      onCancel();
      return;
    }
    onCommit(next);
  }

  return (
    <input
      ref={ref}
      type="text"
      value={value}
      placeholder={placeholder}
      onChange={(e) => setValue(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          commit();
        } else if (e.key === 'Escape') {
          e.preventDefault();
          onCancel();
        }
        e.stopPropagation();
      }}
      onBlur={() => commit()}
      onClick={(e) => e.stopPropagation()}
      onDoubleClick={(e) => e.stopPropagation()}
      className={cn(
        'w-full rounded border border-accent/60 bg-bg-inset px-1.5 py-0.5 text-xs text-fg outline-none',
        'ring-2 ring-accent/30',
        className,
      )}
    />
  );
}
