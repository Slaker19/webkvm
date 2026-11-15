import { describe, it, expect, beforeEach } from 'vitest';
import {
  theme,
  THEMES,
  ACCENTS,
  setTheme,
  setAccent,
  cycleTheme,
  resolveTheme,
} from './theme.svelte.js';

// 8.26 minor: with no stored choice the default follows the system.
describe('resolveTheme', () => {
  it('follows prefers-color-scheme when nothing is stored', () => {
    expect(resolveTheme(null, true)).toBe('light');
    expect(resolveTheme(null, false)).toBe('zinc');
    expect(resolveTheme('bogus', true)).toBe('light');
  });
  it('a stored choice always wins', () => {
    expect(resolveTheme('zinc', true)).toBe('zinc');
    expect(resolveTheme('oled', true)).toBe('oled');
    expect(resolveTheme('light', false)).toBe('light');
  });
});

describe('theme store (node env)', () => {
  beforeEach(() => {
    setTheme('zinc');
    setAccent('indigo');
  });

  it('exposes defined themes and accents', () => {
    expect(THEMES).toEqual(['zinc', 'oled', 'light']);
    expect(ACCENTS).toEqual(['indigo', 'emerald', 'blue', 'violet', 'amber', 'rose']);
  });

  it('updates valid theme mode', () => {
    expect(theme.mode).toBe('zinc');
    setTheme('oled');
    expect(theme.mode).toBe('oled');
    setTheme('light');
    expect(theme.mode).toBe('light');
  });

  it('ignores invalid theme mode (allowlist security)', () => {
    setTheme('zinc');
    setTheme('malicious_theme');
    expect(theme.mode).toBe('zinc');
    setTheme('<script>alert(1)</script>');
    expect(theme.mode).toBe('zinc');
  });

  it('updates valid accent colors', () => {
    expect(theme.accent).toBe('indigo');
    setAccent('emerald');
    expect(theme.accent).toBe('emerald');
    setAccent('rose');
    expect(theme.accent).toBe('rose');
  });

  it('ignores invalid accent colors', () => {
    setAccent('indigo');
    setAccent('neon_red');
    expect(theme.accent).toBe('indigo');
  });

  it('cycles theme through allowlist', () => {
    setTheme('zinc');
    cycleTheme();
    expect(theme.mode).toBe('oled');
    cycleTheme();
    expect(theme.mode).toBe('light');
    cycleTheme();
    expect(theme.mode).toBe('zinc');
  });
});
