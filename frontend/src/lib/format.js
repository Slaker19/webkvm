/**
 * Compatibility shim.
 *
 * There used to be two divergent byte formatters: this module emitted
 * "1.5 MB" (1 decimal) while `$lib/utils/format.js` emitted "1.50 MB"
 * (2 decimals), so the same value rendered differently depending on
 * which one a component imported. The single source of truth is now
 * `$lib/utils/format.js`; this file re-exports from it so existing
 * `$lib/format.js` imports keep working with identical output.
 */
export { formatBytes, formatRAM, formatRate, formatETA } from './utils/format.js';
