// Released KVM VMs (GET /api/ha/released): VMs whose HA was turned off with
// stop/forget. Their definition and storage stack are kept by WebKVM so an
// admin can restore them. Only meaningful with the Cluster/HA module on.
import { api } from './auth.svelte.js';
import { onSessionCleared } from '../sessionHooks.js';

export const haReleased = $state({
  loaded: false,
  loading: false,
  error: '',
  errorCode: '',
  entries: [],
  localNode: '',
});

let inflight = null;
let generation = 0;

export function resetHaReleased() {
  generation++;
  inflight = null;
  haReleased.loaded = false;
  haReleased.loading = false;
  haReleased.error = '';
  haReleased.errorCode = '';
  haReleased.entries = [];
  haReleased.localNode = '';
}

onSessionCleared(resetHaReleased);

// One request at a time; a failure keeps the last good list.
export function refreshHaReleased() {
  if (inflight) return inflight;
  const gen = generation;
  haReleased.loading = true;
  const run = (async () => {
    try {
      const data = await api.listReleasedVMs();
      if (gen !== generation) return;
      haReleased.entries = Array.isArray(data?.released) ? data.released : [];
      haReleased.localNode = data?.local_node || '';
      haReleased.error = '';
      haReleased.errorCode = '';
      haReleased.loaded = true;
    } catch (e) {
      if (gen !== generation) return;
      haReleased.error = e?.message || String(e);
      haReleased.errorCode = e?.code || '';
    } finally {
      if (gen === generation) {
        haReleased.loading = false;
        inflight = null;
      }
    }
  })();
  inflight = run;
  return run;
}
