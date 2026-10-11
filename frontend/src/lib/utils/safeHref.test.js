import { describe, it, expect } from 'vitest';
import { safeHref } from './safeHref.js';

describe('safeHref', () => {
  it('keeps http and https URLs', () => {
    expect(safeHref('https://pi-hole.net')).toBe('https://pi-hole.net');
    expect(safeHref(' http://example.org/a?b=1 ')).toBe('http://example.org/a?b=1');
  });
  it('drops dangerous or relative values', () => {
    for (const v of [
      'javascript:alert(1)',
      'JaVaScRiPt:alert(1)',
      '  javascript:alert(1)',
      'data:text/html,<script>1</script>',
      'vbscript:x',
      '//evil.example',
      '/relative',
      'https://',
      '',
      null,
      undefined,
      42,
    ]) {
      expect(safeHref(v)).toBeUndefined();
    }
  });
});
