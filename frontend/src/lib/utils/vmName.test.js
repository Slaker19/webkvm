import { describe, it, expect } from 'vitest';
import { isValidVmName } from './vmName.js';

describe('isValidVmName', () => {
  it('accepts the backend-allowed charset', () => {
    for (const n of ['web01', 'db.prod', 'a_b-c', 'X', 'a'.repeat(64)]) {
      expect(isValidVmName(n)).toBe(true);
    }
  });
  it('rejects spaces, other symbols, empty and over-long names', () => {
    for (const n of ['', 'my vm', 'vm/1', 'vm@x', 'ñandú', 'a'.repeat(65), undefined]) {
      expect(isValidVmName(n)).toBe(false);
    }
  });
});
