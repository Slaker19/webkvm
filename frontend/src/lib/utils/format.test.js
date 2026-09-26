import { describe, it, expect } from 'vitest';
import { formatRate, formatBytes, formatETA } from './format.js';

describe('formatRate', () => {
  it('handles null/undefined defensively', () => {
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

describe('formatBytes', () => {
  it('formats bytes into KB, MB, GB', () => {
    expect(formatBytes(null)).toBe('0 B');
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(1024)).toBe('1.0 KB');
    expect(formatBytes(50 * 1024 * 1024)).toBe('50.00 MB');
    expect(formatBytes(2.5 * 1024 * 1024 * 1024)).toBe('2.50 GB');
  });
});

describe('formatETA', () => {
  it('formats seconds into ETA string', () => {
    expect(formatETA(0)).toBe('');
    expect(formatETA(null)).toBe('');
    expect(formatETA(45)).toBe('45s');
    expect(formatETA(135)).toBe('2m 15s');
    expect(formatETA(3665)).toBe('1h 1m');
  });
});
