/**
 * Shared VM state -> color helpers. Consolidates what used to be
 * separately duplicated `stateColors` objects in VmList.svelte and
 * VmDetail.svelte (and StatCard.svelte's own inline dot-class map) —
 * all three drew from the same --status-* tokens in app.css, just
 * copy-pasted. Single source of truth from here on; introduces no
 * new colors.
 */

const DOT_CLASS = {
  running: 'bg-status-running',
  shutoff: 'bg-status-shutoff',
  paused: 'bg-status-paused',
  crashed: 'bg-status-crashed',
};

const BADGE_CLASS = {
  running: 'badge-running',
  shutoff: 'badge-shutoff',
  paused: 'badge-paused',
  crashed: 'badge-crashed',
};

/** Tailwind background-color class for a status dot, e.g. `bg-status-running`. */
export function stateDotClass(state) {
  return DOT_CLASS[state] || DOT_CLASS.crashed;
}

/** CSS class for a full status pill/badge, e.g. `badge-running` (see app.css). */
export function stateBadgeClass(state) {
  return BADGE_CLASS[state] || BADGE_CLASS.crashed;
}
