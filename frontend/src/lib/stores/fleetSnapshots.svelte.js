/**
 * Whether the fleet has any VM snapshots at all, anywhere. Drives
 * whether the "Snapshots" sidebar link is shown — a fresh install has
 * zero snapshots and nothing to look at there, so the link stays
 * hidden until the first one is taken. Checked once (by Sidebar.svelte
 * on mount, not on every navigation); Snapshots.svelte itself
 * refreshes it on each of its own loads, so visiting the page directly
 * (e.g. a bookmarked/typed URL) self-heals the sidebar state too.
 */
export const fleetSnapshots = $state({ checked: false, hasAny: false });

/** @param {() => Promise<{snapshots?: unknown[]}>} listAllSnapshots */
export async function checkFleetSnapshots(listAllSnapshots) {
  try {
    const res = await listAllSnapshots();
    fleetSnapshots.hasAny = (res.snapshots || []).length > 0;
  } catch {
    // Leave hasAny as-is on failure — don't flip a nav item based on a
    // transient network error.
  } finally {
    fleetSnapshots.checked = true;
  }
}
