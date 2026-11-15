import { describe, it, expect } from 'vitest';
import { ramIssue, ramIssueText, MIN_VM_RAM_MB, LOW_RAM_FLOOR_MB } from './ramValidation.js';
import { messages } from '../i18n.svelte.js';

describe('ramIssue (one rule for wizard, Edit and backend)', () => {
  it('needs 512 MB by default', () => {
    expect(MIN_VM_RAM_MB).toBe(512);
    expect(ramIssue({ ramMB: 512 })).toBe('');
    expect(ramIssue({ ramMB: 256 })).toBe('belowMin');
    expect(ramIssue({ ramMB: 0 })).toBe('belowMin');
  });
  it('the explicit override allows down to 128 MB, never below', () => {
    expect(LOW_RAM_FLOOR_MB).toBe(128);
    expect(ramIssue({ ramMB: 256, allowLow: true })).toBe('');
    expect(ramIssue({ ramMB: 128, allowLow: true })).toBe('');
    expect(ramIssue({ ramMB: 64, allowLow: true })).toBe('belowFloor');
  });
  it('the ballooning floor cannot exceed the RAM', () => {
    expect(ramIssue({ ramMB: 1024, minRamMB: 2048 })).toBe('floorAboveRam');
    expect(ramIssue({ ramMB: 256, minRamMB: 512, allowLow: true })).toBe('floorAboveRam');
    expect(ramIssue({ ramMB: 1024, minRamMB: 1024 })).toBe('');
    expect(ramIssue({ ramMB: 1024, minRamMB: 0 })).toBe('');
  });
  it('every issue has a message in en/es/ca naming the limit', () => {
    for (const loc of ['en', 'es', 'ca']) {
      for (const k of ['belowMin', 'belowFloor', 'floorAboveRam']) {
        expect(messages[loc].vmRam?.[k], `${loc}.${k}`).toBeTruthy();
      }
      expect(messages[loc].vmRam.belowMin).toContain('{min}');
    }
    const t = (k, v) => `${k}:${v.min}`;
    expect(ramIssueText(t, 'belowMin')).toBe('vmRam.belowMin:512');
    expect(ramIssueText(t, '')).toBe('');
  });
});
