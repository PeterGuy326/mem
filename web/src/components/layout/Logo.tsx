import { cn } from '@/lib/cn';

/**
 * mem mark — geometric monogram. Lowercase "m" formed from a 3-column block
 * with a small accent dot on the bottom-right. Designed to read at 16px.
 */
export function Logo({ className }: { className?: string }) {
  return (
    <div className={cn('flex items-center gap-2', className)}>
      <svg
        width="22"
        height="22"
        viewBox="0 0 32 32"
        fill="none"
        aria-hidden="true"
        className="text-accent"
      >
        <rect x="0.5" y="0.5" width="31" height="31" rx="7.5" className="stroke-border" />
        <path
          d="M7 23V9h3.2l3.4 7.2L17 9h3.2v14h-2.6V13.6L14.5 21h-1.8L9.6 13.6V23H7z"
          fill="currentColor"
        />
        <circle cx="24" cy="22" r="2" fill="currentColor" />
      </svg>
      <span className="font-semibold tracking-tight text-fg">mem</span>
    </div>
  );
}
