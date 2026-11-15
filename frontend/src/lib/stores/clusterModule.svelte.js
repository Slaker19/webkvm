// Optional Cluster/HA module state. Loaded once per session and refreshed
// while an install runs. The HA routes themselves are not called here.
import { api } from './auth.svelte.js';
import { onSessionCleared } from '../sessionHooks.js';

export const clusterModule = $state({
  loaded: false,
  loading: false,
  enabled: false,
  status: null,
  busy: false,
  error: '',
});

// Single in-flight refresh. Callers that ask for a (forced) refresh while one
// is running get the same promise, so they always wait for fresh data
// instead of returning early with a stale store.
let inflight = null;
// Bumped by resetClusterModule so a response that arrives after a logout
// cannot repopulate the store of the next session.
let generation = 0;

export function resetClusterModule() {
  generation++;
  inflight = null;
  clusterModule.loaded = false;
  clusterModule.loading = false;
  clusterModule.enabled = false;
  clusterModule.status = null;
  clusterModule.busy = false;
  clusterModule.error = '';
}

// Logout, 401 and a failed bootstrap all clear the session: drop whatever
// the previous user could see (module state, status log).
onSessionCleared(resetClusterModule);

export function refreshClusterModule(force = false) {
  if (inflight) return inflight;
  if (clusterModule.loaded && !force) return Promise.resolve();
  const gen = generation;
  clusterModule.loading = true;
  clusterModule.error = '';
  const run = (async () => {
    try {
      const data = await api.getHAModule();
      if (gen !== generation) return;
      clusterModule.enabled = !!data.enabled;
      clusterModule.status = data.status;
      clusterModule.busy = !!data.busy;
      clusterModule.loaded = true;
    } catch (e) {
      if (gen !== generation) return;
      // A failed request is NOT "not installed": keep `loaded` false (or the
      // last good data) and expose the error so the UI can offer a retry.
      clusterModule.error = e?.message || String(e);
    } finally {
      if (gen === generation) {
        clusterModule.loading = false;
        inflight = null;
      }
    }
  })();
  inflight = run;
  return run;
}

export function applyClusterModuleStatus(status) {
  if (!status) return;
  clusterModule.status = status;
  clusterModule.busy = status.state === 'installing' || status.state === 'uninstalling';
}

// opts: { linstor, accept_experimental } (a bare boolean is the LINSTOR flag).
export async function startClusterModuleInstall(opts) {
  const status = await api.installHAModule(opts);
  applyClusterModuleStatus(status);
  clusterModule.enabled = false;
  return status;
}

export async function verifyClusterModule() {
  const status = await api.verifyHAModule();
  applyClusterModuleStatus(status);
  return status;
}

export async function enableClusterModule() {
  await api.enableHAModule();
  clusterModule.enabled = true;
}

export async function disableClusterModule() {
  await api.disableHAModule();
  clusterModule.enabled = false;
}

export async function uninstallClusterModule(purge = false) {
  const status = await api.uninstallHAModule(purge);
  applyClusterModuleStatus(status);
  clusterModule.enabled = false;
  return status;
}
