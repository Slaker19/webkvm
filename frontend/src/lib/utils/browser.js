/**
 * Browser detection that doesn't depend on SvelteKit's $app/environment.
 * Returns true in the browser, false during SSR.
 */
export const browser = typeof window !== 'undefined' && typeof document !== 'undefined';
