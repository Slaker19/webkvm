/**
 * Per-browser VM card ordering & pinning preferences for VmList.svelte.
 *
 * Supports:
 *   - sortMode: 'custom' | 'alpha' | 'alpha-desc' | 'state' | 'cpu' | 'ram'
 *   - customOrder: explicit array of VM IDs (drag & drop), used only when
 *     sortMode === 'custom'. IDs not present in customOrder are appended
 *     at the end in their natural (API) order — so a newly created VM
 *     always shows up without needing to be dragged first.
 *   - pinned: array of VM IDs always shown first, regardless of sortMode.
 *
 * Persisted to localStorage so the layout survives reloads/navigation.
 */
import { SvelteMap, SvelteSet } from 'svelte/reactivity';
import { browser } from '$lib/utils/browser.js';

const KEY = 'webkvm.vmList.order.v1';

// Allowlist so a stale/typed value in localStorage (e.g. the removed
// 'created' mode, or arbitrary junk) can never leave the list unsorted.
const VALID_SORT_MODES = ['custom', 'alpha', 'alpha-desc', 'state', 'cpu', 'ram'];

function normalizeSortMode(mode) {
  return VALID_SORT_MODES.includes(mode) ? mode : 'custom';
}

function initial() {
  if (!browser) return { sortMode: 'custom', customOrder: [], pinned: [] };
  try {
    const raw = JSON.parse(localStorage.getItem(KEY));
    if (raw && typeof raw === 'object') {
      return {
        sortMode: normalizeSortMode(typeof raw.sortMode === 'string' ? raw.sortMode : 'custom'),
        customOrder: Array.isArray(raw.customOrder) ? raw.customOrder : [],
        pinned: Array.isArray(raw.pinned) ? raw.pinned : [],
      };
    }
  } catch {
    /* corrupted → defaults */
  }
  return { sortMode: 'custom', customOrder: [], pinned: [] };
}

export const vmOrder = $state(initial());

function save() {
  if (!browser) return;
  localStorage.setItem(
    KEY,
    JSON.stringify({
      sortMode: vmOrder.sortMode,
      customOrder: vmOrder.customOrder,
      pinned: vmOrder.pinned,
    })
  );
}

export function setSortMode(mode) {
  vmOrder.sortMode = normalizeSortMode(mode);
  save();
}

export function togglePin(id) {
  const idx = vmOrder.pinned.indexOf(id);
  if (idx >= 0) vmOrder.pinned = vmOrder.pinned.filter((x) => x !== id);
  else vmOrder.pinned = [...vmOrder.pinned, id];
  save();
}

export function isPinned(id) {
  return vmOrder.pinned.includes(id);
}

// Reorders `ids` (full ID→drag position) after a drag-and-drop move:
// removes draggedId from its old position and re-inserts it right
// before/after targetId depending on `after`.
export function moveInCustomOrder(allIds, draggedId, targetId, after) {
  // Base ordering: whatever customOrder currently has, then any new
  // IDs appended in their natural order.
  const base = vmOrder.customOrder.length
    ? [...vmOrder.customOrder.filter((id) => allIds.includes(id))]
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
  vmOrder.customOrder = withoutDragged;
  vmOrder.sortMode = 'custom';
  save();
}

// Applies the current sortMode + pinned list to a VM array, returning a
// new sorted array. `metrics` is an optional map vmId -> {cpu, ram} last
// values for the cpu/ram sort modes.
export function applySort(vms, metrics = {}) {
  const pinnedSet = new SvelteSet(vmOrder.pinned);
  const pinnedVms = [];
  const restVms = [];
  for (const v of vms) {
    if (pinnedSet.has(v.id)) pinnedVms.push(v);
    else restVms.push(v);
  }

  const sortFns = {
    alpha: (a, b) => (a.alias || a.name || '').localeCompare(b.alias || b.name || ''),
    'alpha-desc': (a, b) => (b.alias || b.name || '').localeCompare(a.alias || a.name || ''),
    state: (a, b) => stateRank(a.state) - stateRank(b.state),
    cpu: (a, b) => (metrics[b.id]?.cpu || 0) - (metrics[a.id]?.cpu || 0),
    ram: (a, b) => (metrics[b.id]?.ram || 0) - (metrics[a.id]?.ram || 0),
  };

  function sortGroup(list) {
    if (vmOrder.sortMode === 'custom') {
      const order = vmOrder.customOrder;
      if (!order.length) return list;
      const idx = new SvelteMap(order.map((id, i) => [id, i]));
      return [...list].sort((a, b) => {
        const ai = idx.has(a.id) ? idx.get(a.id) : Number.MAX_SAFE_INTEGER;
        const bi = idx.has(b.id) ? idx.get(b.id) : Number.MAX_SAFE_INTEGER;
        return ai - bi;
      });
    }
    const fn = sortFns[vmOrder.sortMode];
    return fn ? [...list].sort(fn) : list;
  }

  return [...sortGroup(pinnedVms), ...sortGroup(restVms)];
}

function stateRank(state) {
  switch (state) {
    case 'running':
      return 0;
    case 'paused':
      return 1;
    case 'shutoff':
      return 2;
    case 'crashed':
      return 3;
    default:
      return 4;
  }
}
