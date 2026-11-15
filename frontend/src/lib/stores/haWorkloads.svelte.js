// Per-workload HA state (GET /api/ha/workloads). Only meaningful when the
// Cluster/HA module is enabled. Polling uses a chained setTimeout (never
// setInterval) so requests cannot overlap, and runs only while at least one
// visible consumer (the panel) has asked for it.
import { api } from './auth.svelte.js';
import { onSessionCleared } from '../sessionHooks.js';

export const POLL_MS = 10000;

export const haWorkloads = $state({
  loaded: false,
  loading: false,
  error: '',
  errorCode: '',
  workloads: [],
  summary: null,
  preconditions: null,
});

let inflight = null;
let generation = 0;
let timer = null;
let watchers = 0;

function clearTimer() {
  if (timer) clearTimeout(timer);
  timer = null;
}

export function resetHaWorkloads() {
  generation++;
  inflight = null;
  watchers = 0;
  clearTimer();
  haWorkloads.loaded = false;
  haWorkloads.loading = false;
  haWorkloads.error = '';
  haWorkloads.errorCode = '';
  haWorkloads.workloads = [];
  haWorkloads.summary = null;
  haWorkloads.preconditions = null;
}

onSessionCleared(resetHaWorkloads);

// One request at a time: a refresh while another is running shares it.
export function refreshHaWorkloads() {
  if (inflight) return inflight;
  const gen = generation;
  haWorkloads.loading = true;
  const run = (async () => {
    try {
      const data = await api.listHAWorkloads();
      if (gen !== generation) return;
      haWorkloads.workloads = Array.isArray(data?.workloads) ? data.workloads : [];
      haWorkloads.summary = data?.summary || null;
      haWorkloads.preconditions = data?.preconditions || null;
      haWorkloads.error = '';
      haWorkloads.errorCode = '';
      haWorkloads.loaded = true;
    } catch (e) {
      if (gen !== generation) return;
      // Keep the last good list; expose the failure so the UI can say so.
      haWorkloads.error = e?.message || String(e);
      haWorkloads.errorCode = e?.code || '';
    } finally {
      if (gen === generation) {
        haWorkloads.loading = false;
        inflight = null;
      }
    }
  })();
  inflight = run;
  return run;
}

function schedule() {
  clearTimer();
  if (watchers <= 0) return;
  const gen = generation;
  timer = setTimeout(async () => {
    timer = null;
    if (gen !== generation || watchers <= 0) return;
    // A hidden tab skips the request but keeps the chain alive.
    if (typeof document === 'undefined' || !document.hidden) await refreshHaWorkloads();
    if (gen === generation) schedule();
  }, POLL_MS);
}

// Start polling for a visible consumer; returns the stop function to call
// on unmount. Several consumers share one chain.
export function watchHaWorkloads() {
  watchers++;
  if (watchers === 1) {
    refreshHaWorkloads().finally(() => {
      if (watchers > 0 && !timer) schedule();
    });
  }
  let stopped = false;
  return () => {
    if (stopped) return;
    stopped = true;
    watchers = Math.max(0, watchers - 1);
    if (watchers === 0) clearTimer();
  };
}
