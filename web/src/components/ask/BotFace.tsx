/**
 * mem's assistant mascot — a friendly little robot, drawn inline so it scales
 * crisply and can react (blinking eyes, pulsing antenna). Replaces the generic
 * sparkle so the assistant has an identity, not just an "AI" glyph.
 */
export function BotFace({
  size = 24,
  className,
  awake = true,
}: {
  size?: number;
  className?: string;
  /** When true, eyes blink and the antenna pulses (use on the live bubble). */
  awake?: boolean;
}) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      fill="none"
      className={className}
      aria-hidden="true"
    >
      {/* antenna */}
      <line x1="16" y1="3.5" x2="16" y2="7.5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" opacity="0.9" />
      <circle cx="16" cy="2.6" r="1.7" fill="currentColor" className={awake ? 'bot-antenna' : ''} />

      {/* side ears */}
      <rect x="3.2" y="13" width="2.6" height="6.5" rx="1.3" fill="currentColor" opacity="0.85" />
      <rect x="26.2" y="13" width="2.6" height="6.5" rx="1.3" fill="currentColor" opacity="0.85" />

      {/* head */}
      <rect x="6" y="7.5" width="20" height="17" rx="6.5" fill="currentColor" />

      {/* face screen */}
      <rect x="8.5" y="10" width="15" height="12" rx="4.5" fill="#0b0e14" fillOpacity="0.92" />

      {/* eyes */}
      <g className={awake ? 'bot-eyes' : ''} style={{ transformOrigin: '16px 15px' }}>
        <circle cx="12.6" cy="15" r="1.9" fill="#a5b4fc" />
        <circle cx="19.4" cy="15" r="1.9" fill="#a5b4fc" />
      </g>

      {/* smile */}
      <path d="M12.4 18.4 Q16 20.6 19.6 18.4" stroke="#a5b4fc" strokeWidth="1.4" strokeLinecap="round" fill="none" opacity="0.85" />
    </svg>
  );
}
