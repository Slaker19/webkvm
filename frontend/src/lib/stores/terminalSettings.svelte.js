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
import { TERMINAL_THEMES } from '$lib/utils/terminalConfig.js';

const THEME_KEY = 'webkvm.term.theme';
const FONTSIZE_KEY = 'webkvm.term.fontsize';
const TUIBAR_KEY = 'webkvm.term.tuibar';
const FNKEYS_KEY = 'webkvm.term.fnkeys';

function getInitialTheme() {
  if (!browser) return 'webkvm';
  try {
    const saved = localStorage.getItem(THEME_KEY);
    if (saved && TERMINAL_THEMES[saved]) return saved;
  } catch {
    /* ignore */
  }
  return 'webkvm';
}

function getInitialFontSize() {
  if (!browser) return 14;
  try {
    const saved = parseInt(localStorage.getItem(FONTSIZE_KEY) || '14', 10);
    if (saved >= 10 && saved <= 24) return saved;
  } catch {
    /* ignore */
  }
  return 14;
}

function getInitialBool(key, def = false) {
  if (!browser) return def;
  try {
    const saved = localStorage.getItem(key);
    if (saved !== null) return saved === 'true' || saved === '1';
  } catch {
    /* ignore */
  }
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
    try {
      localStorage.setItem(THEME_KEY, themeId);
    } catch {
      /* ignore */
    }
  }
}

export function setTerminalFontSize(size) {
  const clamped = Math.max(10, Math.min(24, size));
  terminalSettings.fontSize = clamped;
  if (browser) {
    try {
      localStorage.setItem(FONTSIZE_KEY, String(clamped));
    } catch {
      /* ignore */
    }
  }
}

export function setTerminalTuiBar(show) {
  terminalSettings.showTuiBar = !!show;
  if (browser) {
    try {
      localStorage.setItem(TUIBAR_KEY, terminalSettings.showTuiBar ? '1' : '0');
    } catch {
      /* ignore */
    }
  }
}

export function setTerminalFnKeys(show) {
  terminalSettings.showFnKeys = !!show;
  if (browser) {
    try {
      localStorage.setItem(FNKEYS_KEY, terminalSettings.showFnKeys ? '1' : '0');
    } catch {
      /* ignore */
    }
  }
}
