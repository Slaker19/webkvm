/**
 * Host device-model capabilities.
 *
 * The local QEMU/libvirt engine does not support every model the UI
 * knows about. The canonical example: QEMU 10 dropped QXL, so on modern
 * Debian/Ubuntu a VM created with the "qxl" video model fails at define
 * time. The backend probes `virsh domcapabilities` (see
 * internal/hostcaps) and exposes the real supported sets here.
 *
 * The UI keeps its curated lists (nice labels, sensible ordering) and
 * filters them through `supports()`. When a preferred model is missing
 * we don't just drop it silently — callers can render it disabled with
 * the reason, which is the whole point of this feature.
 *
 * Failure is non-fatal and permissive: if the probe never ran or
 * returned nothing, every model is treated as supported, so a hiccup
 * degrades to the old behaviour rather than an empty dropdown.
 */
import { api } from './auth.svelte.js';
import { hasModel, pickModel, flagState, withFlagState } from '$lib/utils/capabilities.js';

export { flagState, withFlagState };

let state = $state({
  loaded: false,
  loading: false,
  failed: false,
  parsed: false,
  qemuVersion: '',
  videoModels: [],
  graphicsTypes: [],
  diskBuses: [],
  diskDevices: [],
  soundModels: [],
  networkModels: [],
  cpuModels: [],
  cpuModes: [],
  cpuFlags: [],
  spiceSupported: false,
  guestfs: false,
});

export const capabilities = {
  get loaded() {
    return state.loaded;
  },
  get parsed() {
    return state.parsed;
  },
  get qemuVersion() {
    return state.qemuVersion;
  },
  get videoModels() {
    return state.videoModels;
  },
  get graphicsTypes() {
    return state.graphicsTypes;
  },
  get diskBuses() {
    return state.diskBuses;
  },
  get diskDevices() {
    return state.diskDevices;
  },
  get soundModels() {
    return state.soundModels;
  },
  get networkModels() {
    return state.networkModels;
  },
  get cpuModels() {
    return state.cpuModels;
  },
  get cpuModes() {
    return state.cpuModes;
  },
  get cpuFlags() {
    return state.cpuFlags;
  },
  get spiceSupported() {
    return state.spiceSupported;
  },
  get guestfs() {
    return state.guestfs;
  },
};

function apply(data) {
  state.videoModels = data.video_models || [];
  state.graphicsTypes = data.graphics_types || [];
  state.diskBuses = data.disk_buses || [];
  state.diskDevices = data.disk_devices || [];
  state.soundModels = data.sound_models || [];
  state.networkModels = data.network_models || [];
  state.cpuModels = data.cpu_models || [];
  state.cpuModes = data.cpu_modes || [];
  state.cpuFlags = data.cpu_flags || [];
  state.spiceSupported = !!data.spice_supported;
  state.guestfs = !!data.guestfs;
  state.qemuVersion = data.qemu_version || '';
  state.parsed = !!data.parsed;
  state.loaded = true;
}

/** Fetch once; safe to call from every component that needs models. */
export async function loadCapabilities(force = false) {
  if (state.loading) return;
  if (state.loaded && !force && !state.failed) return;
  state.loading = true;
  try {
    const data = force ? await api.refreshCapabilities() : await api.getCapabilities();
    apply(data);
    state.failed = false;
  } catch (e) {
    // Permissive fallback: mark loaded with empty lists, which makes
    // supports() return true for everything. `failed` lets the next
    // caller retry instead of keeping a transient error for the whole
    // session.
    console.warn('[WebKVM UI] capabilities probe failed', e);
    state.loaded = true;
    state.failed = true;
  } finally {
    state.loading = false;
  }
}

/**
 * Whether a model is available on this host.
 *
 * `kind` selects the capability list. Returns true when capabilities are
 * unknown (not loaded yet) so a slow probe never hides the whole UI.
 * Individual lists degrade independently: CPU models/flags come from a
 * separate probe (`virsh cpu-models`, `qemu -cpu help`) than the
 * domcapabilities-derived ones, so an empty CPU list does not block on
 * `parsed` being false for the device-model probe.
 */
export function supports(kind, model) {
  if (!state.loaded) return true;
  const list = {
    video: state.parsed ? state.videoModels : [],
    graphics: state.parsed ? state.graphicsTypes : [],
    network: state.networkModels,
    diskbus: state.parsed ? state.diskBuses : [],
    diskdevice: state.parsed ? state.diskDevices : [],
    sound: state.soundModels,
    cpu: state.cpuModels,
    cpumode: state.cpuModes,
    cpuflag: state.cpuFlags,
  }[kind];
  return hasModel(list, model);
}

/**
 * Whether SPICE remote-viewer access should be offered. Unknown
 * capabilities (not loaded / probe failed) do not disable it.
 */
export function spiceAvailable() {
  if (!state.loaded || !state.parsed) return true;
  return state.spiceSupported;
}

/**
 * Human-readable reason a model is unavailable, for a tooltip or inline
 * hint. Returns '' when the model is fine (or capabilities unknown).
 */
export function unavailableReason(kind, model) {
  if (supports(kind, model)) return '';
  return `Unavailable on this host (QEMU ${state.qemuVersion || 'unknown'})`;
}

/**
 * Pick the best available model for a preference chain, using this
 * host's capabilities. Views call this instead of the raw util so the
 * preset logic stays in one place.
 */
export function pick(kind, preferred, fallbacks = []) {
  if (!state.loaded) return preferred;
  const list = {
    video: state.parsed ? state.videoModels : [],
    graphics: state.parsed ? state.graphicsTypes : [],
    network: state.networkModels,
    diskbus: state.parsed ? state.diskBuses : [],
    diskdevice: state.parsed ? state.diskDevices : [],
    sound: state.soundModels,
    cpu: state.cpuModels,
    cpumode: state.cpuModes,
    cpuflag: state.cpuFlags,
  }[kind];
  return pickModel(list, preferred, fallbacks);
}
