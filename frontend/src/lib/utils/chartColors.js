/**
 * Shared categorical palette for charts / group swatches.
 *
 * These are deliberately literal hex values, not theme tokens: a
 * categorical palette must stay visually distinct regardless of the
 * active theme/accent, and a user picking a group colour is choosing
 * an absolute colour, not a semantic role. Centralising the list keeps
 * VmList's swatches and the backup chart's fallback colour from
 * diverging (they used to be two independently maintained values).
 */
export const CATEGORICAL_COLORS = [
  '#7c3aed',
  '#3b82f6',
  '#10b981',
  '#f59e0b',
  '#ef4444',
  '#ec4899',
  '#06b6d4',
  '#84cc16',
];

/**
 * Deterministically maps a string (e.g. a group name) to a colour from
 * the shared palette, so the same name always gets the same colour
 * without needing one to be persisted.
 */
export function colorForKey(key) {
  if (!key) return CATEGORICAL_COLORS[0];
  let h = 0;
  for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) | 0;
  return CATEGORICAL_COLORS[Math.abs(h) % CATEGORICAL_COLORS.length];
}
