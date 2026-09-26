/**
 * Per-browser preference for how CPU/RAM load is visualised.
 *
 * Modes:
 *   - 'radial' — circular ring gauges (default; compact and easy to
 *     scan across a grid of VM cards).
 *   - 'linear' — horizontal bars (denser vertically, closer to the
 *     classic progress-bar look some operators prefer).
 *
 * Persisted to localStorage so the choice survives reloads. Kept in
 * its own tiny store (rather than in the settings API) because it's a
 * per-device display preference, not server state: the same admin may
 * reasonably want rings on a wide desktop and bars on a laptop.
 */
import { browser } from '$lib/utils/browser.js';

const KEY = 'webkvm.gauge.style.v1';
const VALID = ['radial', 'linear'];

function initial() {
  if (!browser) return { mode: 'radial' };
  try {
    const raw = localStorage.getItem(KEY);
    if (VALID.includes(raw)) return { mode: raw };
  } catch {
    /* private mode / storage disabled → default */
  }
  return { mode: 'radial' };
}

export const gaugeStyle = $state(initial());

export function setGaugeStyle(mode) {
  if (!VALID.includes(mode)) return;
  gaugeStyle.mode = mode;
  if (!browser) return;
  try {
    localStorage.setItem(KEY, mode);
  } catch {
    /* non-fatal: the preference just won't persist */
  }
}

export function toggleGaugeStyle() {
  setGaugeStyle(gaugeStyle.mode === 'radial' ? 'linear' : 'radial');
}

/**
 * Traffic-light colour for a 0-100 load percentage. Shared by both
 * gauge variants so a VM at 95% looks equally alarming either way.
 * Returns a CSS custom property so it follows the active theme.
 */
export function loadColor(pct) {
  const n = Number(pct) || 0;
  if (n >= 90) return 'var(--destructive)';
  if (n >= 70) return 'var(--warning)';
  return 'var(--success)';
}
