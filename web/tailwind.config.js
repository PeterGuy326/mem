/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        // Surface tokens — driven by CSS variables so we can swap themes (W2)
        bg: {
          DEFAULT: 'rgb(var(--bg) / <alpha-value>)',
          subtle: 'rgb(var(--bg-subtle) / <alpha-value>)',
          panel: 'rgb(var(--bg-panel) / <alpha-value>)',
          inset: 'rgb(var(--bg-inset) / <alpha-value>)',
        },
        fg: {
          DEFAULT: 'rgb(var(--fg) / <alpha-value>)',
          muted: 'rgb(var(--fg-muted) / <alpha-value>)',
          subtle: 'rgb(var(--fg-subtle) / <alpha-value>)',
        },
        border: {
          DEFAULT: 'rgb(var(--border) / <alpha-value>)',
          strong: 'rgb(var(--border-strong) / <alpha-value>)',
        },
        accent: {
          DEFAULT: 'rgb(var(--accent) / <alpha-value>)',
          hover: 'rgb(var(--accent-hover) / <alpha-value>)',
          muted: 'rgb(var(--accent-muted) / <alpha-value>)',
        },
        success: 'rgb(var(--success) / <alpha-value>)',
        warn: 'rgb(var(--warn) / <alpha-value>)',
        danger: 'rgb(var(--danger) / <alpha-value>)',
      },
      fontFamily: {
        sans: [
          'Inter',
          'ui-sans-serif',
          'system-ui',
          '-apple-system',
          'BlinkMacSystemFont',
          '"PingFang SC"',
          '"Hiragino Sans GB"',
          '"Microsoft YaHei"',
          'sans-serif',
        ],
        mono: ['"JetBrains Mono"', 'ui-monospace', 'SFMono-Regular', 'monospace'],
      },
      fontSize: {
        '2xs': ['0.6875rem', { lineHeight: '0.875rem' }],
      },
      boxShadow: {
        soft: '0 1px 0 0 rgb(255 255 255 / 0.03) inset, 0 1px 2px 0 rgb(0 0 0 / 0.4)',
        glow: '0 0 0 1px rgb(var(--accent) / 0.35), 0 8px 24px -8px rgb(var(--accent) / 0.45)',
      },
      backgroundImage: {
        'grid-fade':
          'linear-gradient(to bottom, rgb(var(--bg)) 0%, transparent 30%, transparent 70%, rgb(var(--bg)) 100%)',
        'dot-grid':
          'radial-gradient(circle at 1px 1px, rgb(var(--border-strong) / 0.5) 1px, transparent 0)',
      },
      keyframes: {
        'fade-in': {
          from: { opacity: '0', transform: 'translateY(4px)' },
          to: { opacity: '1', transform: 'translateY(0)' },
        },
        shimmer: {
          '100%': { transform: 'translateX(100%)' },
        },
        'slide-up': {
          from: { opacity: '0', transform: 'translateY(12px) scale(0.98)' },
          to: { opacity: '1', transform: 'translateY(0) scale(1)' },
        },
        breathe: {
          '0%, 100%': { boxShadow: '0 0 0 0 rgb(var(--accent) / 0.45), 0 10px 30px -8px rgb(var(--accent) / 0.5)' },
          '50%': { boxShadow: '0 0 0 8px rgb(var(--accent) / 0), 0 10px 30px -8px rgb(var(--accent) / 0.5)' },
        },
      },
      animation: {
        'fade-in': 'fade-in 200ms ease-out',
        shimmer: 'shimmer 1.6s infinite',
        'slide-up': 'slide-up 240ms cubic-bezier(0.22, 1, 0.36, 1)',
        breathe: 'breathe 3.5s ease-in-out infinite',
      },
    },
  },
  plugins: [],
};
