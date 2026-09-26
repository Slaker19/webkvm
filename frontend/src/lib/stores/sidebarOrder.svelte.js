/**
 * Per-browser sidebar navigation customization: item ordering (within
 * each group) and visibility.
 *
 * Modes:
 *   - 'default' — the built-in order defined in Sidebar.svelte.
 *   - 'alpha'   — items within each group sorted alphabetically by label.
 *   - 'custom'  — explicit drag & drop order per item ID.
 *
 * `hidden` lists item IDs the operator chose to hide from the sidebar
 * (still reachable by URL — this only affects the nav list). Some core
 * items (vms, account) can never be hidden; that's enforced by the
 * caller, not this store.
 *
 * Persisted to localStorage.
 */
import { SvelteMap } from 'svelte/reactivity';
import { browser } from '$lib/utils/browser.js';

const KEY = 'webkvm.sidebar.order.v1';

function initial() {
  if (!browser) return { mode: 'default', order: [], hidden: [] };
  try {
    const raw = JSON.parse(localStorage.getItem(KEY));
    if (raw && typeof raw === 'object') {
      return {
        mode: typeof raw.mode === 'string' ? raw.mode : 'default',
        order: Array.isArray(raw.order) ? raw.order : [],
        hidden: Array.isArray(raw.hidden) ? raw.hidden : [],
      };
    }
  } catch {
    /* corrupted → defaults */
  }
  return { mode: 'default', order: [], hidden: [] };
}

export const sidebarOrder = $state(initial());

function save() {
  if (!browser) return;
  localStorage.setItem(
    KEY,
    JSON.stringify({
      mode: sidebarOrder.mode,
      order: sidebarOrder.order,
      hidden: sidebarOrder.hidden,
    })
  );
}

export function setSidebarMode(mode) {
  sidebarOrder.mode = mode;
  save();
}

export function toggleSidebarHidden(id) {
  const idx = sidebarOrder.hidden.indexOf(id);
  if (idx >= 0) sidebarOrder.hidden = sidebarOrder.hidden.filter((x) => x !== id);
  else sidebarOrder.hidden = [...sidebarOrder.hidden, id];
  save();
}

export function isSidebarHidden(id) {
  return sidebarOrder.hidden.includes(id);
}

export function resetSidebarOrder() {
  sidebarOrder.mode = 'default';
  sidebarOrder.order = [];
  sidebarOrder.hidden = [];
  save();
}

// Moves draggedId to just before/after targetId within the global
// custom order list, switching mode to 'custom' as a side effect.
export function moveSidebarItem(allIds, draggedId, targetId, after) {
  const base = sidebarOrder.order.length
    ? [...sidebarOrder.order.filter((id) => allIds.includes(id))]
    : [...allIds];
  for (const id of allIds) {
    if (!base.includes(id)) base.push(id);
  }
  const withoutDragged = base.filter((id) => id !== draggedId);
  const targetIdx = withoutDragged.indexOf(targetId);
  if (targetIdx === -1) {
    withoutDragged.push(draggedId);
  } else {
    withoutDragged.splice(after ? targetIdx + 1 : targetIdx, 0, draggedId);
  }
  sidebarOrder.order = withoutDragged;
  sidebarOrder.mode = 'custom';
  save();
}

// Applies mode + hidden filtering to a list of items ({id, label, ...}).
// Returns a new array; does not mutate the input.
export function applySidebarOrder(items) {
  let out = items.filter((it) => !sidebarOrder.hidden.includes(it.id));
  if (sidebarOrder.mode === 'alpha') {
    out = [...out].sort((a, b) => (a.label || '').localeCompare(b.label || ''));
  } else if (sidebarOrder.mode === 'custom' && sidebarOrder.order.length) {
    const idx = new SvelteMap(sidebarOrder.order.map((id, i) => [id, i]));
    out = [...out].sort((a, b) => {
      const ai = idx.has(a.id) ? idx.get(a.id) : Number.MAX_SAFE_INTEGER;
      const bi = idx.has(b.id) ? idx.get(b.id) : Number.MAX_SAFE_INTEGER;
      return ai - bi;
    });
  }
  return out;
}
