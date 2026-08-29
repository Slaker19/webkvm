/**
 * Per-browser layout preferences for the VM detail page: which blocks
 * are minimized. Persisted to localStorage. (Blocks used to be freely
 * reorderable via drag & drop; that went away once the page moved to
 * tabs — with 1-5 cards per tab instead of 8 in one column, manual
 * ordering no longer earned its complexity.)
 */
import { browser } from '$lib/utils/browser.js';

const KEY = 'webkvm.vmDetail.layout.v2';

function initial() {
  if (!browser) return { collapsed: {} };
  try {
    const raw = JSON.parse(localStorage.getItem(KEY));
    if (raw && typeof raw.collapsed === 'object' && raw.collapsed) {
      return { collapsed: raw.collapsed };
    }
  } catch {
    /* corrupted → defaults */
  }
  return { collapsed: {} };
}

export const layout = $state(initial());

export function saveLayout() {
  if (!browser) return;
  localStorage.setItem(KEY, JSON.stringify({ collapsed: layout.collapsed }));
}

export function toggleCollapsed(id) {
  layout.collapsed[id] = !layout.collapsed[id];
  saveLayout();
}

export function resetLayout() {
  layout.collapsed = {};
  saveLayout();
}
