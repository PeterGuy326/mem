import * as React from 'react';
import { cn } from '@/lib/cn';
import type { IndexStatus } from '@/lib/types';
import { useT } from '@/i18n';

type Tone = 'neutral' | 'accent' | 'success' | 'warn' | 'danger' | 'muted';

const TONE: Record<Tone, string> = {
  neutral: 'bg-bg-inset text-fg-muted border-border',
  accent: 'bg-accent/10 text-accent border-accent/30',
  success: 'bg-success/10 text-success border-success/30',
  warn: 'bg-warn/10 text-warn border-warn/30',
  danger: 'bg-danger/10 text-danger border-danger/30',
  muted: 'bg-bg-inset text-fg-subtle border-border',
};

export interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement> {
  tone?: Tone;
  dot?: boolean;
}

export function Badge({ className, tone = 'neutral', dot, children, ...rest }: BadgeProps) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-2xs font-medium',
        'tracking-wide whitespace-nowrap',
        TONE[tone],
        className,
      )}
      {...rest}
    >
      {dot && (
        <span
          aria-hidden="true"
          className="inline-block h-1.5 w-1.5 rounded-full bg-current"
        />
      )}
      {children}
    </span>
  );
}

const STATUS_TONE: Record<IndexStatus, Tone> = {
  pending: 'muted',
  processing: 'accent',
  done: 'success',
  failed: 'danger',
};

export function StatusBadge({ status }: { status: IndexStatus }) {
  const { t } = useT();
  return (
    <Badge tone={STATUS_TONE[status]} dot>
      {t(`status.${status}`)}
    </Badge>
  );
}
