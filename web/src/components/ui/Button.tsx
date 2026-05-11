import * as React from 'react';
import { cn } from '@/lib/cn';

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'outline';
type Size = 'sm' | 'md' | 'lg' | 'icon';

const VARIANT: Record<Variant, string> = {
  primary:
    'bg-accent text-bg hover:bg-accent-hover shadow-soft active:translate-y-px disabled:bg-accent/40 disabled:text-bg/60',
  secondary:
    'bg-bg-inset text-fg border border-border hover:border-border-strong hover:bg-bg-panel',
  ghost: 'text-fg-muted hover:text-fg hover:bg-bg-inset',
  danger:
    'bg-danger/10 text-danger border border-danger/30 hover:bg-danger/20 hover:border-danger/60',
  outline:
    'bg-transparent text-fg border border-border hover:border-border-strong hover:bg-bg-inset',
};

const SIZE: Record<Size, string> = {
  sm: 'h-7 px-2.5 text-xs gap-1.5',
  md: 'h-9 px-3.5 text-sm gap-2',
  lg: 'h-11 px-5 text-sm gap-2',
  icon: 'h-9 w-9 p-0',
};

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant = 'secondary', size = 'md', loading, disabled, children, ...rest }, ref) => {
    return (
      <button
        ref={ref}
        disabled={disabled || loading}
        className={cn(
          'inline-flex items-center justify-center rounded-md font-medium',
          'transition-colors duration-150 select-none whitespace-nowrap',
          'disabled:cursor-not-allowed disabled:opacity-60',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/60 focus-visible:ring-offset-2 focus-visible:ring-offset-bg',
          SIZE[size],
          VARIANT[variant],
          className,
        )}
        {...rest}
      >
        {loading && (
          <span
            aria-hidden="true"
            className="inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-r-transparent"
          />
        )}
        {children}
      </button>
    );
  },
);
Button.displayName = 'Button';
