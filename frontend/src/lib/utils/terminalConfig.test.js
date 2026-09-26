import { describe, it, expect } from 'vitest';
import { TERMINAL_THEMES, TUI_KEYS } from './terminalConfig.js';

describe('terminalConfig', () => {
  it('has all core themes with valid hex colors', () => {
    const requiredThemes = ['webkvm', 'oled', 'nord', 'dracula', 'monokai'];
    for (const id of requiredThemes) {
      const theme = TERMINAL_THEMES[id];
      expect(theme).toBeDefined();
      expect(theme.name).toBeTypeOf('string');
      expect(theme.background).toMatch(/^#[0-9a-fA-F]{6}$/);
      expect(theme.foreground).toMatch(/^#[0-9a-fA-F]{6}$/);
      expect(theme.cursor).toMatch(/^#[0-9a-fA-F]{6}$/);
    }
  });

  it('defines standard ANSI escape sequences for TUI keys', () => {
    expect(TUI_KEYS.ESC).toBe('\x1b');
    expect(TUI_KEYS.CTRL_C).toBe('\x03');
    expect(TUI_KEYS.CTRL_Z).toBe('\x1a');
    expect(TUI_KEYS.CTRL_D).toBe('\x04');
    expect(TUI_KEYS.CTRL_L).toBe('\x0c');
    expect(TUI_KEYS.F1).toBe('\x1bOP');
    expect(TUI_KEYS.F10).toBe('\x1b[21~');
  });
});
