// Tiny dependency-free registry so stores that cache per-session data can
// reset themselves when the session ends (logout, 401, bootstrap failure)
// without auth.svelte.js having to import them (that would be an import
// cycle: those stores import `api` from auth.svelte.js).
const listeners = new Set();

export function onSessionCleared(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function notifySessionCleared() {
  for (const fn of listeners) {
    try {
      fn();
    } catch {
      // a broken listener must never block the logout
    }
  }
}
