import * as React from 'react';
import { cn } from '@/lib/cn';

export interface EmptyStateProps {
  icon?: React.ReactNode;
  title: string;
  description?: React.ReactNode;
  action?: React.ReactNode;
  className?: string;
}

export function EmptyState({ icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center text-center py-16 px-6',
        'border border-dashed border-border rounded-lg bg-bg-subtle/40',
        className,
      )}
    >
      {icon && (
        <div className="mb-4 text-fg-subtle [&>svg]:h-8 [&>svg]:w-8">{icon}</div>
      )}
      <div className="text-sm font-medium text-fg">{title}</div>
      {description && (
        <div className="mt-1.5 max-w-md text-sm text-fg-muted leading-relaxed">{description}</div>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}
