/**
 * terminalSettings.svelte.js
 *
 * Centralised store for WebKVM terminal user preferences:
 * - theme: 'webkvm' | 'oled' | 'nord' | 'dracula' | 'monokai'
 * - fontSize: 10 to 24 (default 14)
 * - showTuiBar: boolean (default false)
 * - showFnKeys: boolean (default false)
 *
 * Persisted in localStorage so both the Terminal panel and Account/Appearance
 * settings stay synchronized.
 */
import { browser } from '$lib/utils/browser.js';
import { safeStorage } from '$lib/utils/storage.js';
import { TERMINAL_THEMES } from '$lib/utils/terminalConfig.js';

const THEME_KEY = 'webkvm.term.theme';
const FONTSIZE_KEY = 'webkvm.term.fontsize';
const TUIBAR_KEY = 'webkvm.term.tuibar';
const FNKEYS_KEY = 'webkvm.term.fnkeys';

function getInitialTheme() {
  if (!browser) return 'webkvm';
  const saved = safeStorage.getItem(THEME_KEY);
  if (saved && TERMINAL_THEMES[saved]) return saved;
  return 'webkvm';
}

function getInitialFontSize() {
  if (!browser) return 14;
  const saved = parseInt(safeStorage.getItem(FONTSIZE_KEY) || '14', 10);
  if (saved >= 10 && saved <= 24) return saved;
  return 14;
}

function getInitialBool(key, def = false) {
  if (!browser) return def;
  const saved = safeStorage.getItem(key);
  if (saved !== null) return saved === 'true' || saved === '1';
  return def;
}

export const terminalSettings = $state({
  theme: getInitialTheme(),
  fontSize: getInitialFontSize(),
  showTuiBar: getInitialBool(TUIBAR_KEY, false),
  showFnKeys: getInitialBool(FNKEYS_KEY, false),
});

export function setTerminalTheme(themeId) {
  if (!TERMINAL_THEMES[themeId]) return;
  terminalSettings.theme = themeId;
  if (browser) {
    safeStorage.setItem(THEME_KEY, themeId);
  }
}

export function setTerminalFontSize(size) {
  const clamped = Math.max(10, Math.min(24, size));
  terminalSettings.fontSize = clamped;
  if (browser) {
    safeStorage.setItem(FONTSIZE_KEY, String(clamped));
  }
}

export function setTerminalTuiBar(show) {
  terminalSettings.showTuiBar = !!show;
  if (browser) {
    safeStorage.setItem(TUIBAR_KEY, terminalSettings.showTuiBar ? '1' : '0');
  }
}

export function setTerminalFnKeys(show) {
  terminalSettings.showFnKeys = !!show;
  if (browser) {
    safeStorage.setItem(FNKEYS_KEY, terminalSettings.showFnKeys ? '1' : '0');
  }
}
