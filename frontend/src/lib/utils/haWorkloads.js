// Pure helpers for the per-workload HA UI.

export const ELIGIBILITY_REASONS = [
  'disk_not_on_replicated_storage',
  'xml_not_on_replicated_storage',
  'drbd_not_uptodate',
  'pool_not_shared',
  'local_devices',
  'boot_autostart_enabled',
  'vm_running',
  'bridge_missing_on_nodes',
  'nic_network_not_on_all_members',
  'name_collision',
];

export const PRECONDITION_BLOCKERS = [
  'fencing_test_required',
  'sbd_not_active',
  'quorum_missing',
  'not_enough_nodes',
  'incus_healing_enabled',
  'incus_member_not_online',
  'control_plane_unhealthy',
  'drbd_not_uptodate',
  'not_eligible',
];

export const HA_ERROR_CODES = [
  'invalid_node_name',
  'invalid_body',
  'workload_not_found',
  'ambiguous_workload',
  'operation_busy',
  'maintenance_active',
  'confirmation_required',
  'precondition_failed',
  'healing_managed_by_pacemaker',
  'pcs_failed',
  'rollback_failed',
  'start_timeout',
  'stop_timeout',
  'workload_active',
  'workload_paused',
  'facts_unavailable',
  'node_not_allowed',
  'id_collision',
  'invalid_spec',
  'module_not_installed',
  // Released KVM VMs (disable stop/forget keeps an inventory entry).
  'release_capture_failed',
  'release_store_failed',
  'release_not_found',
  'release_unrestorable',
  'release_xml_invalid',
  'restore_node_not_local',
  'workload_managed',
  'drbd_managed',
  'domain_conflict',
  'mount_conflict',
  'drbd_primary_elsewhere',
  'drbd_not_uptodate',
  'drbd_up_failed',
  'drbd_promote_failed',
  'mount_failed',
  'define_failed',
  'start_failed',
  'drbd_peers_down',
  'mount_not_visible',
  'xml_link_failed',
];

// Workload identity of a VM/instance row. KVM has no project; Incus always
// has one ("default" when the API does not say).
export function workloadRef(vm) {
  if (!vm) return null;
  const kind = vm.hypervisor === 'incus' ? 'incus' : 'kvm';
  const ref = { kind, name: vm.name, project: kind === 'incus' ? vm.project || 'default' : '' };
  ref.key = kind === 'incus' ? `incus:${ref.project}/${ref.name}` : `kvm:${ref.name}`;
  return ref;
}

export function findWorkload(list, ref) {
  if (!ref || !Array.isArray(list)) return null;
  return list.find((w) => w.key === ref.key) || null;
}

// A VM is HA-managed when the VM list says so (KVM) or the workloads store
// has a managed entry for it (Incus, and KVM while the list is stale).
export function isHaManaged(vm, list) {
  if (vm?.managed_by === 'pacemaker') return true;
  const w = findWorkload(list, workloadRef(vm));
  return !!w && (w.managed !== false || !!w.maint_until);
}

function lookup(t, prefix, code, fallbackKey) {
  const key = `${prefix}.${code}`;
  const out = t(key);
  return out !== key ? out : t(fallbackKey, { code });
}

export const reasonText = (t, code) =>
  lookup(t, 'haWorkloads.reasons', code, 'haWorkloads.unknownReason');
export const reasonFix = (t, code) => {
  const key = `haWorkloads.fixes.${code}`;
  const out = t(key);
  return out !== key ? out : '';
};
export const blockerText = (t, code) =>
  lookup(t, 'haWorkloads.blockers', code, 'haWorkloads.unknownBlocker');

// Translated message for a failed HA call. Falls back to the backend text
// (never to a bare code) for codes without a translation.
export function haErrorText(t, e) {
  const code = e?.code;
  if (code) {
    const key = `haWorkloads.errors.${code}`;
    const out = t(key);
    if (out !== key) {
      const left = e?.data?.leftovers;
      return Array.isArray(left) && left.length ? `${out} ${left.join(', ')}` : out;
    }
  }
  return e?.message || String(e);
}

// Peers to bring up carried by a 409 drbd_peers_down (null for any other
// error). commands holds the distinct commands, each prefixed by a comment
// naming the nodes it must run on (WebKVM cannot run them there).
export function peersDownDetails(e) {
  if (e?.code !== 'drbd_peers_down') return null;
  const resource = typeof e?.data?.resource === 'string' ? e.data.resource : '';
  const peers = (Array.isArray(e?.data?.peers) ? e.data.peers : [])
    .filter((p) => p && typeof p.node === 'string' && p.node)
    .map((p) => ({
      node: p.node,
      connection: typeof p.connection === 'string' && p.connection ? p.connection : 'unknown',
      command: typeof p.command === 'string' ? p.command : '',
    }));
  const byCmd = new Map();
  for (const p of peers) {
    if (!p.command) continue;
    byCmd.set(p.command, [...(byCmd.get(p.command) || []), p.node]);
  }
  const commands = [];
  for (const [cmd, nodes] of byCmd) commands.push(`# ${nodes.join(', ')}`, cmd);
  if (!commands.length && resource) commands.push(`drbdadm up ${resource}`);
  return { resource, peers, commands };
}

// Blockers + eligibility reasons carried by a 409 precondition_failed.
export function preconditionDetails(e) {
  return {
    blockers: Array.isArray(e?.data?.blockers) ? e.data.blockers : [],
    reasons: Array.isArray(e?.data?.reasons) ? e.data.reasons : [],
  };
}

// Health bucket for badges: ok | warn | bad | muted.
export function workloadHealth(w) {
  if (!w) return 'muted';
  if (w.state === 'failed' || w.failcount > 0) return 'bad';
  if (w.state === 'blocked' || w.state === 'unmanaged' || w.maint_until) return 'warn';
  if (w.state === 'started') return 'ok';
  if (w.state === 'starting') return 'warn';
  return 'muted';
}

// Nodes a workload can be moved to: allowed nodes (or all known) minus the
// one it runs on.
export function moveTargets(w, nodeNames) {
  const pool = w?.allowed_nodes?.length ? w.allowed_nodes : nodeNames || [];
  return pool.filter((n) => n && n !== w?.node);
}

export function isMaintenance(w) {
  return !!w && w.managed === false && !!w.maint_until;
}

// Seconds left of a maintenance window (negative once expired).
export function maintenanceLeft(w, now = Date.now()) {
  if (!w?.maint_until) return null;
  const until = Date.parse(w.maint_until);
  if (Number.isNaN(until)) return null;
  return Math.round((until - now) / 1000);
}

// Modes offered when turning HA off, safest first. Pause keeps the VM defined
// and visible; stop/forget release the shared storage of a KVM workload.
export const DISABLE_MODES = ['pause', 'stop', 'forget'];
export const DEFAULT_DISABLE_MODE = 'pause';

// True when disabling HA this way takes a KVM VM out of libvirt (its DRBD
// resource goes down and the transient domain is gone). WebKVM keeps it as a
// "released" entry that can be restored from the UI.
export function releasesKvmStorage(ref, mode) {
  return ref?.kind === 'kvm' && (mode === 'stop' || mode === 'forget');
}

// Released entries still waiting for a restore (status "released").
export function pendingReleased(entries) {
  return Array.isArray(entries) ? entries.filter((e) => e?.status === 'released') : [];
}

// Restore is offered only for a released entry with a saved definition.
export function canRestore(entry) {
  return !!entry && entry.status === 'released' && entry.restorable === true;
}

// The released entry of a VM name (KVM only), or null.
export function releasedFor(entries, name) {
  if (!name || !Array.isArray(entries)) return null;
  return entries.find((e) => e?.name === name) || null;
}
