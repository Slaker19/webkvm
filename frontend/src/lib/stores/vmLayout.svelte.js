/**
 * Per-browser layout preferences for the VM detail page: block order and
 * which blocks are minimized. Persisted to localStorage.
 */
import { browser } from '$lib/utils/browser.js';

const KEY = 'webkvm.vmDetail.layout.v1';

export const DEFAULT_ORDER = [
  'overview',
  'metrics',
  'serial',
  'disks',
  'net',
  'snaps',
  'firewall',
  'schedule',
];

function initial() {
  if (!browser) return { order: [...DEFAULT_ORDER], collapsed: {}, dragging: null, drop: null };
  try {
    const raw = JSON.parse(localStorage.getItem(KEY));
    if (raw && Array.isArray(raw.order) && raw.order.length) {
      // Keep known ids in saved order; append any new blocks; drop stale.
      const order = [
        ...raw.order.filter((id) => DEFAULT_ORDER.includes(id)),
        ...DEFAULT_ORDER.filter((id) => !raw.order.includes(id)),
      ];
      return {
        order,
        collapsed: raw.collapsed || {},
        dragging: null,
        drop: null,
      };
    }
  } catch {
    /* corrupted → defaults */
  }
  return { order: [...DEFAULT_ORDER], collapsed: {}, dragging: null, drop: null };
}

export const layout = $state(initial());

export function saveLayout() {
  if (!browser) return;
  localStorage.setItem(KEY, JSON.stringify({ order: layout.order, collapsed: layout.collapsed }));
}

export function moveBlock(id, delta) {
  const i = layout.order.indexOf(id);
  const j = i + delta;
  if (i < 0 || j < 0 || j >= layout.order.length) return;
  const [x] = layout.order.splice(i, 1);
  layout.order.splice(j, 0, x);
  saveLayout();
}

export function blockToTop(id) {
  const i = layout.order.indexOf(id);
  if (i <= 0) return;
  const [x] = layout.order.splice(i, 1);
  layout.order.unshift(x);
  saveLayout();
}

export function toggleCollapsed(id) {
  layout.collapsed[id] = !layout.collapsed[id];
  saveLayout();
}

export function resetLayout() {
  layout.order = [...DEFAULT_ORDER];
  layout.collapsed = {};
  saveLayout();
}

// --- Drag & drop -------------------------------------------------------
export function dragStart(id) {
  layout.dragging = id;
}
export function dragEnd() {
  layout.dragging = null;
  layout.drop = null;
}
/** Called while hovering over card `overId`; pos = 'before' | 'after'. */
export function dragOver(overId, pos) {
  if (!layout.dragging || layout.dragging === overId) {
    layout.drop = null;
    return;
  }
  layout.drop = { over: overId, pos };
}
export function dropCommit() {
  const { dragging, drop } = layout;
  if (dragging && drop && dragging !== drop.over) {
    const from = layout.order.indexOf(dragging);
    const [x] = layout.order.splice(from, 1);
    let to = layout.order.indexOf(drop.over);
    if (!drop.pos.before) to += 1;
    layout.order.splice(to, 0, x);
    saveLayout();
  }
  layout.dragging = null;
  layout.drop = null;
}
