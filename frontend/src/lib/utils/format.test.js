import { describe, it, expect } from 'vitest';
import { formatRate } from './format.js';

describe('formatRate', () => {
  it('handles null/undefined defensively (V12-FE-04)', () => {
    expect(formatRate(null)).toBe('0 B/s');
    expect(formatRate(undefined)).toBe('0 B/s');
  });

  it('formats bytes per second across units', () => {
    expect(formatRate(0)).toBe('0 B/s');
    expect(formatRate(512)).toBe('512 B/s');
    expect(formatRate(1024)).toBe('1.0 KB/s');
    expect(formatRate(1536)).toBe('1.5 KB/s');
    expect(formatRate(1024 * 1024)).toBe('1.00 MB/s');
    expect(formatRate(3.5 * 1024 * 1024)).toBe('3.50 MB/s');
    expect(formatRate(2 * 1024 * 1024 * 1024)).toBe('2.00 GB/s');
  });

  it('never produces NaN for negative input', () => {
    const out = formatRate(-5);
    expect(out).not.toContain('NaN');
  });
});
