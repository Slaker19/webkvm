/**
 * theme.svelte.js — Theme & Accent color store.
 *
 * Supports 3 themes:
 *   - 'zinc': Default sophisticated dark slate palette (#0a0a0c background)
 *   - 'oled': True black (#000000) for OLED screens and high contrast
 *   - 'light': Clean, high-contrast light theme
 *
 * Supports 6 accent colors:
 *   - 'indigo' (default), 'emerald', 'blue', 'violet', 'amber', 'rose'
 *
 * Persisted in localStorage with strict allowlist validation.
 */
import { browser } from '$lib/utils/browser.js';
import { safeStorage } from '$lib/utils/storage.js';

export const THEMES = ['zinc', 'oled', 'light'];
export const ACCENTS = ['indigo', 'emerald', 'blue', 'violet', 'amber', 'rose'];

export const ACCENT_COLORS = {
  indigo: '#6366f1',
  emerald: '#10b981',
  blue: '#3b82f6',
  violet: '#8b5cf6',
  amber: '#f59e0b',
  rose: '#f43f5e',
};

const THEME_KEY = 'webkvm.theme.mode.v1';
const ACCENT_KEY = 'webkvm.theme.accent.v1';

// resolveTheme: a stored choice wins; with none, follow the system
// preference (light when the OS asks for it, the dark default otherwise).
// index.html applies the same rule before the app loads (no flash).
export function resolveTheme(saved, prefersLight) {
  if (THEMES.includes(saved)) return saved;
  return prefersLight ? 'light' : 'zinc';
}

function initialTheme() {
  if (!browser) return 'zinc';
  let saved = null;
  try {
    saved = safeStorage.getItem(THEME_KEY);
  } catch {
    /* ignore */
  }
  let prefersLight = false;
  try {
    prefersLight = !!window.matchMedia?.('(prefers-color-scheme: light)')?.matches;
  } catch {
    /* ignore */
  }
  return resolveTheme(saved, prefersLight);
}

function initialAccent() {
  if (!browser) return 'indigo';
  try {
    const saved = safeStorage.getItem(ACCENT_KEY);
    if (ACCENTS.includes(saved)) return saved;
  } catch {
    /* ignore */
  }
  return 'indigo';
}

function applyDOM(themeMode, accentColor) {
  if (!browser) return;
  const root = document.documentElement;
  root.setAttribute('data-theme', themeMode);
  root.setAttribute('data-accent', accentColor);

  if (themeMode === 'light') {
    root.classList.remove('dark');
    root.classList.add('light');
  } else {
    root.classList.remove('light');
    root.classList.add('dark');
  }
}

export const theme = $state({
  mode: initialTheme(),
  accent: initialAccent(),
});

// Apply initially upon module load if in browser
if (browser) {
  applyDOM(theme.mode, theme.accent);
}

export function setTheme(mode) {
  if (!THEMES.includes(mode)) return;
  theme.mode = mode;
  if (browser) {
    try {
      safeStorage.setItem(THEME_KEY, mode);
    } catch {
      /* ignore */
    }
    applyDOM(theme.mode, theme.accent);
  }
}

export function setAccent(accent) {
  if (!ACCENTS.includes(accent)) return;
  theme.accent = accent;
  if (browser) {
    try {
      safeStorage.setItem(ACCENT_KEY, accent);
    } catch {
      /* ignore */
    }
    applyDOM(theme.mode, theme.accent);
  }
}

export function cycleTheme() {
  const i = THEMES.indexOf(theme.mode);
  const next = THEMES[(i + 1) % THEMES.length];
  setTheme(next);
}
