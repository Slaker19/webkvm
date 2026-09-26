/**
 * Shared VM state -> color helper for the small status dot (a plain
 * colored circle, e.g. next to a VM name). For a full pill/badge with
 * label, see StatusBadge.svelte, which wraps the shadcn Badge instead
 * of the old bespoke `badge-running` CSS classes this module used to
 * also export.
 */

const DOT_CLASS = {
  running: 'bg-status-running',
  shutoff: 'bg-status-shutoff',
  paused: 'bg-status-paused',
  crashed: 'bg-status-crashed',
};

/** Tailwind background-color class for a status dot, e.g. `bg-status-running`. */
// Unknown/other libvirt states (blocked, idle, pmsuspended, …) render as the
// neutral grey (shutoff) instead of red "crashed" — an unrecognized state
// must not look like a failure.
export function stateDotClass(state) {
  return DOT_CLASS[state] || DOT_CLASS.shutoff;
}
