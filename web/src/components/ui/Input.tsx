import * as React from 'react';
import { cn } from '@/lib/cn';

export interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {
  leadingIcon?: React.ReactNode;
  trailing?: React.ReactNode;
  invalid?: boolean;
}

export const Input = React.forwardRef<HTMLInputElement, InputProps>(
  ({ className, leadingIcon, trailing, invalid, ...rest }, ref) => {
    if (leadingIcon || trailing) {
      return (
        <div
          className={cn(
            'group flex h-9 items-center gap-2 rounded-md border bg-bg-inset px-3',
            'border-border focus-within:border-accent/60 focus-within:ring-2 focus-within:ring-accent/30',
            'transition-colors',
            invalid && 'border-danger/60 focus-within:border-danger focus-within:ring-danger/30',
            className,
          )}
        >
          {leadingIcon && (
            <span className="text-fg-subtle flex-none [&>svg]:h-4 [&>svg]:w-4">{leadingIcon}</span>
          )}
          <input
            ref={ref}
            className={cn(
              'w-full bg-transparent text-sm text-fg placeholder:text-fg-subtle outline-none',
            )}
            {...rest}
          />
          {trailing && <span className="text-fg-subtle flex-none">{trailing}</span>}
        </div>
      );
    }
    return (
      <input
        ref={ref}
        className={cn(
          'h-9 w-full rounded-md border bg-bg-inset px-3 text-sm text-fg placeholder:text-fg-subtle',
          'border-border outline-none transition-colors',
          'focus:border-accent/60 focus:ring-2 focus:ring-accent/30',
          invalid && 'border-danger/60 focus:border-danger focus:ring-danger/30',
          className,
        )}
        {...rest}
      />
    );
  },
);
Input.displayName = 'Input';
