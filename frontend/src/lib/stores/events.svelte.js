/**
 * Frontend SSE event store.
 *
 * Connects to the backend's /api/events endpoint via EventSource and
 * dispatches incoming events to registered listeners.
 *
 * Auth: EventSource cannot set custom headers, so the JWT is passed
 * via the `?token=` query parameter. The backend's JWT middleware
 * already supports this.
 *
 * Reconnect policy: exponential backoff starting at 1s, doubling up
 * to 30s, reset on successful open. A `reconnecting` state is
 * exposed so the UI can show a "reconnecting…" pill.
 *
 * Usage:
 *   import { events } from '$lib/stores/events.svelte.js';
 *
 *   // In a component:
 *   $effect(() => {
 *     const off = events.onVmState((e) => { ... });
 *     return off;
 *   });
 */

import { auth } from './auth.svelte.js';
import { browser } from '$lib/utils/browser.js';

const MIN_RECONNECT_MS = 1000;
const MAX_RECONNECT_MS = 30000;
const MAX_RECONNECT_ATTEMPTS = 0; // 0 = unlimited

class EventsStore {
  connected = $state(false);
  reconnecting = $state(false);
  reconnectAttempts = $state(0);
  lastError = $state(null);

  constructor() {
    this._es = null;
    this._vmStateListeners = new Set();
    this._removedListeners = new Set();
    this._metricsListeners = new Set();
    this._hostMetricsListeners = new Set();
    this._reconnectTimer = null;
    this._lastToken = null;
  }

  connect() {
    if (!browser) return;
    if (!auth.token) return;
    if (this._es && this._lastToken === auth.token) return;

    this._disconnect();
    this._open();
  }

  _open() {
    const url = `/api/events?token=${encodeURIComponent(auth.token)}`;
    const es = new EventSource(url);
    this._es = es;
    this._lastToken = auth.token;

    es.addEventListener('open', () => {
      this.connected = true;
      this.reconnecting = false;
      this.reconnectAttempts = 0;
      this.lastError = null;
    });

    es.addEventListener('connected', () => {
      this.connected = true;
    });

    es.addEventListener('vm.state', (e) => {
      this._dispatch('vmState', e, this._vmStateListeners);
    });

    es.addEventListener('vm.removed', (e) => {
      this._dispatch('vmRemoved', e, this._removedListeners);
    });

    es.addEventListener('vm.metrics', (e) => {
      this._dispatch('vmMetrics', e, this._metricsListeners);
    });

    es.addEventListener('host.metrics', (e) => {
      this._dispatch('hostMetrics', e, this._hostMetricsListeners);
    });

    es.addEventListener('error', () => {
      this.connected = false;
      // If the token has changed (e.g. logout) the next connect
      // will pick it up; otherwise schedule a backoff.
      if (this._lastToken === auth.token) {
        this._scheduleReconnect();
      }
    });
  }

  _dispatch(name, e, set) {
    try {
      const data = JSON.parse(e.data);
      for (const fn of set) {
        try {
          fn(data);
        } catch (err) {
          console.error(`${name} listener error:`, err);
        }
      }
    } catch (err) {
      console.error(`Failed to parse ${name} event:`, err);
    }
  }

  _scheduleReconnect() {
    if (this._reconnectTimer) return;
    if (MAX_RECONNECT_ATTEMPTS > 0 && this.reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
      this.lastError = 'giving up after ' + MAX_RECONNECT_ATTEMPTS + ' attempts';
      this.reconnecting = false;
      return;
    }
    this.reconnecting = true;
    this.reconnectAttempts += 1;
    // Exponential backoff: 1s, 2s, 4s, 8s, 16s, 30s, 30s, ...
    const delay = Math.min(
      MIN_RECONNECT_MS * Math.pow(2, Math.max(0, this.reconnectAttempts - 1)),
      MAX_RECONNECT_MS
    );
    this._reconnectTimer = setTimeout(() => {
      this._reconnectTimer = null;
      if (auth.token && this._lastToken === auth.token) {
        this._open();
      }
    }, delay);
  }

  _disconnect() {
    if (this._reconnectTimer) {
      clearTimeout(this._reconnectTimer);
      this._reconnectTimer = null;
    }
    if (this._es) {
      this._es.close();
      this._es = null;
    }
    this.connected = false;
    this.reconnecting = false;
  }

  disconnect() {
    this._disconnect();
    this._lastToken = null;
    this.reconnectAttempts = 0;
  }

  reconnectNow() {
    if (this._reconnectTimer) {
      clearTimeout(this._reconnectTimer);
      this._reconnectTimer = null;
    }
    this.reconnectAttempts = 0;
    if (auth.token) this._open();
  }

  onVmState(fn) {
    this._vmStateListeners.add(fn);
    this.connect();
    return () => this._vmStateListeners.delete(fn);
  }

  onVmRemoved(fn) {
    this._removedListeners.add(fn);
    this.connect();
    return () => this._removedListeners.delete(fn);
  }

  onVmMetrics(fn) {
    this._metricsListeners.add(fn);
    this.connect();
    return () => this._metricsListeners.delete(fn);
  }

  onHostMetrics(fn) {
    this._hostMetricsListeners.add(fn);
    this.connect();
    return () => this._hostMetricsListeners.delete(fn);
  }
}

export const events = new EventsStore();
