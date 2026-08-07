import { describe, it, expect, vi } from 'vitest';

// Mock i18n before importing format
vi.mock('@/i18n', () => ({
  getLocale: () => 'en-US',
  tt: (key: string, vars?: Record<string, unknown>) => {
    if (key === 'time.justNow') return 'just now';
    if (key === 'time.minutesAgo') return `${vars?.n}m ago`;
    if (key === 'time.hoursAgo') return `${vars?.n}h ago`;
    if (key === 'time.daysAgo') return `${vars?.n}d ago`;
    return key;
  },
}));

import { formatBytes, formatDate, truncateMiddle, formatRelative } from './format';

describe('formatBytes', () => {
  it('formats bytes', () => {
    expect(formatBytes(500)).toBe('500 B');
  });
  it('formats KB', () => {
    const result = formatBytes(1536);
    expect(result).toContain('KB');
  });
  it('formats MB', () => {
    const result = formatBytes(5 * 1024 * 1024);
    expect(result).toContain('MB');
  });
  it('formats GB', () => {
    const result = formatBytes(2.5 * 1024 * 1024 * 1024);
    expect(result).toContain('GB');
  });
});

describe('formatDate', () => {
  it('returns dash for null', () => {
    expect(formatDate(null)).toBe('\u2014');
  });
  it('returns dash for undefined', () => {
    expect(formatDate(undefined)).toBe('\u2014');
  });
  it('returns dash for invalid date', () => {
    expect(formatDate('not-a-date')).toBe('\u2014');
  });
  it('formats valid ISO date', () => {
    const result = formatDate('2024-03-15T12:00:00Z');
    // Should contain year and date parts
    expect(result).toContain('2024');
  });
});

describe('truncateMiddle', () => {
  it('returns short strings unchanged', () => {
    expect(truncateMiddle('hello', 10)).toBe('hello');
  });
  it('truncates long strings with ellipsis in middle', () => {
    const result = truncateMiddle('abcdefghijklmnopqrstuvwxyz', 10);
    expect(result.length).toBeLessThanOrEqual(11); // half + ellipsis + half
    expect(result).toContain('\u2026');
  });
  it('uses default max of 24', () => {
    const short = 'short';
    expect(truncateMiddle(short)).toBe(short);
    const long = 'a'.repeat(30);
    expect(truncateMiddle(long)).toContain('\u2026');
  });
});

describe('formatRelative', () => {
  it('returns dash for null', () => {
    expect(formatRelative(null)).toBe('\u2014');
  });
  it('returns "just now" for recent timestamps', () => {
    const now = new Date().toISOString();
    expect(formatRelative(now)).toBe('just now');
  });
  it('returns minutes ago', () => {
    const fiveMinAgo = new Date(Date.now() - 5 * 60 * 1000).toISOString();
    expect(formatRelative(fiveMinAgo)).toBe('5m ago');
  });
  it('returns hours ago', () => {
    const twoHoursAgo = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString();
    expect(formatRelative(twoHoursAgo)).toBe('2h ago');
  });
});
