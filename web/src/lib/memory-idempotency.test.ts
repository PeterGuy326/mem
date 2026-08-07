import { describe, it, expect } from 'vitest';
import { memoryActionKey } from './memory-idempotency';

describe('memoryActionKey', () => {
  it('produces deterministic key for same inputs', () => {
    const a = memoryActionKey('user-1', 'mem-abc', 3, 'archive');
    const b = memoryActionKey('user-1', 'mem-abc', 3, 'archive');
    expect(a).toBe(b);
  });

  it('includes actor, memory, version, and action', () => {
    const key = memoryActionKey('actor@test', 'mem-123', 1, 'delete');
    expect(key).toContain('actor%40test');
    expect(key).toContain('mem-123');
    expect(key).toContain('v1');
    expect(key).toContain('delete');
  });

  it('different actors produce different keys', () => {
    const a = memoryActionKey('user-1', 'mem-abc', 1, 'archive');
    const b = memoryActionKey('user-2', 'mem-abc', 1, 'archive');
    expect(a).not.toBe(b);
  });

  it('different versions produce different keys', () => {
    const a = memoryActionKey('user-1', 'mem-abc', 1, 'archive');
    const b = memoryActionKey('user-1', 'mem-abc', 2, 'archive');
    expect(a).not.toBe(b);
  });

  it('throws on empty actor', () => {
    expect(() => memoryActionKey('', 'mem-abc', 1, 'archive')).toThrow(
      'authenticated actor id is required',
    );
  });

  it('throws on whitespace-only actor', () => {
    expect(() => memoryActionKey('   ', 'mem-abc', 1, 'archive')).toThrow(
      'authenticated actor id is required',
    );
  });
});
