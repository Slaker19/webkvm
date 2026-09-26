import { describe, it, expect, beforeEach } from 'vitest';
import {
  terminalSettings,
  setTerminalTheme,
  setTerminalFontSize,
  setTerminalTuiBar,
  setTerminalFnKeys,
} from './terminalSettings.svelte.js';

beforeEach(() => {
  setTerminalTheme('webkvm');
  setTerminalFontSize(14);
  setTerminalTuiBar(false);
  setTerminalFnKeys(false);
});

describe('terminalSettings store', () => {
  it('updates theme correctly and ignores invalid themes', () => {
    expect(terminalSettings.theme).toBe('webkvm');
    setTerminalTheme('nord');
    expect(terminalSettings.theme).toBe('nord');
    setTerminalTheme('invalid-theme-xyz');
    expect(terminalSettings.theme).toBe('nord');
  });

  it('clamps font sizes between 10 and 24', () => {
    setTerminalFontSize(16);
    expect(terminalSettings.fontSize).toBe(16);
    setTerminalFontSize(5);
    expect(terminalSettings.fontSize).toBe(10);
    setTerminalFontSize(50);
    expect(terminalSettings.fontSize).toBe(24);
  });

  it('toggles tuiBar and fnKeys flags', () => {
    setTerminalTuiBar(true);
    expect(terminalSettings.showTuiBar).toBe(true);
    setTerminalTuiBar(false);
    expect(terminalSettings.showTuiBar).toBe(false);

    setTerminalFnKeys(true);
    expect(terminalSettings.showFnKeys).toBe(true);
    setTerminalFnKeys(false);
    expect(terminalSettings.showFnKeys).toBe(false);
  });
});
