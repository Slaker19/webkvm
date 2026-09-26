/**
 * Pure helpers for matching model names against a host capability list.
 *
 * Kept separate from the capabilities store so they can be unit-tested
 * without a network layer: the store pulls from the API, these functions
 * only decide whether a given model is offered.
 */

/**
 * Whether `model` is present in `supported`.
 *
 * Matching is case-insensitive because some models are uppercased for
 * display ("VGA") but compared against lowercase XML values ("vga").
 *
 * An empty or missing `supported` list means "unknown", which is
 * treated as permissive: a host we could not probe should not have its
 * whole UI emptied. Likewise an empty/"none" model is always allowed,
 * since it means the device is omitted rather than emulated.
 */
export function hasModel(supported, model) {
  if (model === '' || model === null || model === undefined) return true;
  if (String(model).toLowerCase() === 'none') return true;
  if (!Array.isArray(supported) || supported.length === 0) return true;
  const want = String(model).toLowerCase();
  return supported.some((m) => String(m).toLowerCase() === want);
}

/**
 * Pick the first available model from a preference chain.
 *
 * Presets ask for a preferred model (Windows wanted "qxl") and fall back
 * through alternatives that are more likely to exist. If none match, the
 * first candidate is returned so the caller still has a concrete value
 * rather than an empty string.
 */
export function pickModel(supported, preferred, fallbacks = []) {
  const chain = [preferred, ...fallbacks.filter((f) => f !== preferred)];
  for (const candidate of chain) {
    if (hasModel(supported, candidate)) return candidate;
  }
  return preferred;
}

/**
 * CPU flag tri-state helpers.
 *
 * The wire format (`cpu_flags`, a `[]string` of `+flag`/`-flag` entries)
 * only has two explicit states — required or disabled — with a flag's
 * absence from the array meaning "let libvirt/QEMU decide" (auto). The
 * UI needs all three as first-class states so an operator can tell "not
 * set" apart from "off", and so re-selecting "auto" actually clears a
 * previous +/- choice instead of just being unreachable.
 */

/** Read a flag's UI state ('on' | 'off' | 'auto') out of the wire array. */
export function flagState(flags, name) {
  if (!Array.isArray(flags)) return 'auto';
  if (flags.includes('+' + name)) return 'on';
  if (flags.includes('-' + name)) return 'off';
  return 'auto';
}

/**
 * Return a new flags array with `name` set to `state`. 'auto' removes
 * any existing +/- entry for the flag; 'on'/'off' replace it.
 */
export function withFlagState(flags, name, state) {
  const base = (Array.isArray(flags) ? flags : []).filter(
    (f) => f !== '+' + name && f !== '-' + name
  );
  if (state === 'on') return [...base, '+' + name];
  if (state === 'off') return [...base, '-' + name];
  return base; // 'auto'
}
