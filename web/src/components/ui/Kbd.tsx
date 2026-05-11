import * as React from 'react';
import { cn } from '@/lib/cn';

/** Keyboard shortcut hint chip — Raycast / Linear style. */
export function Kbd({ className, children }: React.PropsWithChildren<{ className?: string }>) {
  return (
    <kbd
      className={cn(
        'inline-flex h-5 min-w-5 items-center justify-center rounded border border-border',
        'bg-bg-inset px-1 font-mono text-2xs text-fg-muted',
        className,
      )}
    >
      {children}
    </kbd>
  );
}
