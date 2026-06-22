/**
 * mem's assistant avatar — a living "AI orb" (Siri / Copilot vibe): a layered
 * gradient sphere with a slowly rotating energy swirl, a drifting specular
 * highlight, and a soft halo. Pure CSS, no deps. Replaces the hand-drawn bot.
 */
import { cn } from '@/lib/cn';

export function Orb({
  size = 28,
  className,
  active = true,
}: {
  size?: number;
  /** When true the swirl rotates + highlight drifts (use on the live avatar). */
  active?: boolean;
  className?: string;
}) {
  return (
    <span
      className={cn('mem-orb', active && 'mem-orb--active', className)}
      style={{ width: size, height: size }}
      aria-hidden="true"
    >
      <span className="mem-orb__core" />
      <span className="mem-orb__swirl" />
      <span className="mem-orb__highlight" />
    </span>
  );
}
