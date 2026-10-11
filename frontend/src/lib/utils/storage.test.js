import { describe, it, expect, beforeEach } from 'vitest';
import { safeStorage } from './storage.js';

describe('safeStorage utility', () => {
  beforeEach(() => {
    safeStorage.clear();
  });

  it('sets, gets and removes items properly', () => {
    safeStorage.setItem('key1', 'val1');
    expect(safeStorage.getItem('key1')).toBe('val1');
    expect(safeStorage.getItem('nonexistent')).toBe(null);

    safeStorage.removeItem('key1');
    expect(safeStorage.getItem('key1')).toBe(null);
  });

  it('handles clear() properly', () => {
    safeStorage.setItem('a', '1');
    safeStorage.setItem('b', '2');
    expect(safeStorage.getItem('a')).toBe('1');
    safeStorage.clear();
    expect(safeStorage.getItem('a')).toBe(null);
    expect(safeStorage.getItem('b')).toBe(null);
  });
});
