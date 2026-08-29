/**
 * Per-browser desktop sidebar display mode: 'full' (labels visible),
 * 'rail' (icon-only, always narrow), or 'hover' (narrow until the
 * pointer/focus enters it, then expands and retracts on leave).
 * Persisted to localStorage. Mobile always uses the full-width
 * off-canvas drawer regardless of this setting — see Layout.svelte.
 */
import { browser } from '$lib/utils/browser.js';

const KEY = 'webkvm.sidebar.mode.v1';
const MODES = ['full', 'rail', 'hover'];

function initial() {
  if (!browser) return 'full';
  try {
    const raw = localStorage.getItem(KEY);
    if (MODES.includes(raw)) return raw;
  } catch {
    /* ignore */
  }
  return 'full';
}

export const sidebarMode = $state({ value: initial() });

export function cycleSidebarMode() {
  const i = MODES.indexOf(sidebarMode.value);
  sidebarMode.value = MODES[(i + 1) % MODES.length];
  if (browser) {
    try {
      localStorage.setItem(KEY, sidebarMode.value);
    } catch {
      /* ignore */
    }
  }
}
