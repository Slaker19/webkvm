/**
 * Toast notification system.
 *
 * Usage:
 *   import { toast } from '$lib/stores/toast.svelte.js';
 *   toast.success('VM started');
 *   toast.error('Failed to delete VM');
 *   toast.info('Update available');
 *
 * Mount <Toaster /> once in App.svelte.
 */

let nextId = 1;

export const toast = {
  success(message, opts = {}) {
    return add({ type: 'success', message, ...opts });
  },
  error(message, opts = {}) {
    return add({ type: 'error', message, ...opts });
  },
  info(message, opts = {}) {
    return add({ type: 'info', message, ...opts });
  },
  warning(message, opts = {}) {
    return add({ type: 'warning', message, ...opts });
  },
};

function add(t) {
  const id = nextId++;
  const full = {
    id,
    type: t.type,
    message: t.message,
    title: t.title || null,
    duration: t.duration ?? 3500,
  };
  _toasts = [..._toasts, full];
  if (full.duration > 0) {
    setTimeout(() => dismiss(id), full.duration);
  }
  return id;
}

export function dismiss(id) {
  _toasts = _toasts.filter((t) => t.id !== id);
}

let _toasts = $state([]);
export function getToasts() {
  return _toasts;
}
