// Remote-edit helpers: which parts of the VM page work for a VM that lives on
// another node, and how to turn the backend's typed errors into text.
//
// KEEP IN SYNC with backend/internal/api/remote_routes.go (vmRouteClasses):
//   A/B routes are served on a remote libvirt node, L routes answer 501
//   `remote_unsupported`. The map below is deliberately tiny: one entry per
//   VM-detail tab. Anything that still slips through is handled at runtime
//   by isRemoteUnsupported()/remoteErrorText(), never shown as a raw error.

import { nodeConsoleURL } from './nodes.js';

// Support of each VM-detail tab on a remote KVM node.
//   full     every control of the tab is class A/B
//   partial  works, but some controls answer 501 (the tab shows the notice
//            when that happens)
//   none     every route of the tab is class L: the tab shows the
//            "open that node's own WebKVM" notice instead of the panel
export const remoteTabs = {
  overview: 'partial', // GET /, meta, boot, autostart A; metrics/firewall/schedule L
  console: 'full', // vnc, spice-ws, graphics, tickets, rdp, spice files: B
  terminal: 'full', // serial: B
  hardware: 'partial', // disks/net/usb A/B; pci L; shared-folders B
  cloudinit: 'none', // seed ISOs live in the local data dir: L
  disks: 'full',
  net: 'full',
  guest: 'partial', // guest-info/exec/sync-time shell out to the local virsh: L; fstrim/freeze A
  history: 'none', // metrics/history: L in V1
  alerts: 'none', // alert rules: L
  snaps: 'full',
};

// Per-VM reads the detail page would issue on every load that are class L
// on a remote node (backend/internal/api/remote_routes.go: GET /metrics,
// GET /firewall, GET /schedule, GET /guest-info answer 501
// remote_unsupported). The page skips them for a remote VM and shows the
// "open that node's own WebKVM" notice straight away. The backend keeps
// answering 501 for any client that still asks.
export const REMOTE_UNSUPPORTED_LOADS = ['metrics', 'firewall', 'schedule', 'guest-info'];

// remoteLoadUnsupported: true when `what` (one of REMOTE_UNSUPPORTED_LOADS)
// is known to answer 501 for this VM, so it must not be requested.
export function remoteLoadUnsupported(vm, what) {
  return isRemoteVm(vm) && REMOTE_UNSUPPORTED_LOADS.includes(what);
}

// Incus instances on a REGISTERED remote Incus endpoint keep the legacy
// narrow allow-list on the backend.
const REMOTE_INCUS_ENDPOINT_TABS = ['overview', 'disks', 'net', 'snaps'];

// isRemoteVm: the VM lives on a registered remote node (libvirt or Incus
// endpoint). Incus cluster members of the LOCAL daemon have no node_id and
// are served by the local handlers, so they are not "remote".
export function isRemoteVm(vm) {
  return !!vm?.node_id;
}

// hv of a VM for the ?hv= selector.
export function vmHv(vm) {
  return vm?.hypervisor === 'incus' ? 'incus' : vm?.hypervisor === 'kvm' ? 'kvm' : '';
}

// vmSelector: the {node, hv} pair every per-VM API call must carry.
export function vmSelector(vm) {
  return { node: vm?.node || '', hv: vmHv(vm) };
}

// vmQuery: query object for navigate('/vms/:id', { query }).
export function vmQuery(vm) {
  const { node, hv } = vmSelector(vm);
  const q = {};
  if (node) q.node = node;
  if (hv) q.hv = hv;
  return q;
}

// tabSupport: 'full' | 'partial' | 'none' for a tab of this VM.
export function tabSupport(tabId, vm) {
  if (!isRemoteVm(vm)) return 'full';
  if (vm.hypervisor === 'incus') {
    return REMOTE_INCUS_ENDPOINT_TABS.includes(tabId) ? 'partial' : 'none';
  }
  return remoteTabs[tabId] || 'none';
}

// Codes that mean "set up (or fix) the managed SSH access of this node".
export const ACCESS_CODES = [
  'host_key_unpinned',
  'host_key_changed',
  'ssh_auth_denied',
  'no_key',
  'transport_unsupported', // the card explains which transport to configure
];

export function needsClusterAccess(code) {
  return ACCESS_CODES.includes(code);
}

// Every code the remote/cluster surface can answer, with an i18n key under
// remote.errors.<code>. Unknown codes fall back to the backend message.
export const REMOTE_ERROR_CODES = [
  'node_unreachable',
  'node_timeout',
  'ssh_auth_denied',
  'host_key_unpinned',
  'host_key_changed',
  'node_disabled',
  'node_not_found',
  'transport_unsupported',
  'no_key',
  'libvirt_unavailable',
  'remote_unsupported',
  'invalid_hv',
  'vm_not_running',
  'ticket_scope',
  'console_busy',
  'console_forward_denied',
  'incus_only',
  'member_not_found',
  'member_offline',
  'needs_stop',
  'same_member',
  'not_clustered',
  'healing_managed_by_pacemaker',
  'local_node',
  'invalid_mode',
];

// remoteErrorText: translated message for an ApiError (or a plain
// {code,message}). Raw backend text is only shown for codes we do not know.
export function remoteErrorText(t, e) {
  const code = e?.code;
  if (code) {
    const key = `remote.errors.${code}`;
    const out = t(key);
    if (out !== key) {
      const node = e?.data?.node;
      return node ? `${out} (${node})` : out;
    }
  }
  if (e?.status === 403 && /remote node access/i.test(e?.message || '')) {
    return t('remote.errors.remote_forbidden');
  }
  return e?.message || String(e ?? '');
}

export function isRemoteUnsupported(e) {
  return (
    e?.code === 'remote_unsupported' || (e?.status === 501 && e?.data?.hint === 'open_node_ui')
  );
}

// nodeUiUrl: web UI of a node (from the registry entry), or null. Uses the
// operator-network address (ui_url) first; a private URI host is never
// offered because the operator's PC cannot reach it.
export function nodeUiUrl(node) {
  return nodeConsoleURL(node);
}

// findNode: registry node of a VM (by id, then by name).
export function findNode(nodes, vm) {
  const list = Array.isArray(nodes) ? nodes : [];
  if (vm?.node_id) {
    const byId = list.find((n) => n.id === vm.node_id);
    if (byId) return byId;
  }
  return list.find((n) => n.name === vm?.node) || null;
}

// nodeHealth: per-node status shown in the VM list: online | degraded |
// offline | stale | unknown. `stale` wins when the node's VMs are last-known
// data, because the numbers on screen are then not live.
export function nodeHealth(node, vmsOfNode = []) {
  if (vmsOfNode.some((v) => v.stale)) return 'stale';
  if (!node) return 'online'; // the local host (or a member): it answered
  if (node.enabled === false) return 'disabled';
  const s = node.status;
  if (s === 'online' || s === 'degraded' || s === 'offline') return s;
  return 'unknown';
}

// nodeIssues: nodes that failed or returned stale data, for the banner.
// `access` carries the last SSH test (GET /cluster/ssh) per node id so the
// translated code is the precise one (host_key_changed, ...).
export function nodeIssues(vms, nodes, access = {}) {
  const issues = [];
  const seen = new Set();
  const byName = new Map();
  for (const v of Array.isArray(vms) ? vms : []) {
    const key = v.node_id || v.node;
    if (!key || !v.node_id) continue;
    if (!byName.has(key)) byName.set(key, { name: v.node, id: v.node_id, stale: false });
    if (v.stale) byName.get(key).stale = true;
  }
  const list = Array.isArray(nodes) ? nodes : [];
  for (const n of list) {
    if (n.type === 'local' || n.enabled === false) continue;
    if (n.status === 'offline' || n.status === 'degraded') {
      const code = access[n.id]?.code || (n.status === 'offline' ? 'node_unreachable' : '');
      issues.push({
        id: n.id,
        name: n.name,
        kind: n.status,
        stale: !!byName.get(n.id)?.stale,
        code,
        access: needsClusterAccess(code),
      });
      seen.add(n.id);
    }
  }
  for (const [id, info] of byName) {
    if (info.stale && !seen.has(id)) {
      const code = access[id]?.code || 'node_unreachable';
      issues.push({
        id,
        name: info.name,
        kind: 'stale',
        stale: true,
        code,
        access: needsClusterAccess(code),
      });
    }
  }
  return issues;
}

// Members an Incus instance can be moved to: online, not the current one.
export function moveTargets(members, vm) {
  const cur = String(vm?.node || '').toLowerCase();
  return (Array.isArray(members) ? members : []).filter(
    (m) => m.status === 'Online' && String(m.name).toLowerCase() !== cur
  );
}

// An instance that is running cannot be moved without `live` (VMs only).
export function moveNeedsStop(vm, live) {
  const running = vm?.state === 'running' || vm?.state === 'paused';
  return running && !live;
}

// Why an Incus member cannot be evacuated/restored right now ('' = it can).
// hostsHa: it hosts Pacemaker-managed workloads (backend answers 409).
export function memberOpBlock(member, op, hostsHa = false) {
  if (!member) return 'unknown';
  if (member.status === 'Offline' || (member.status && member.status === 'Unknown')) {
    return 'offline';
  }
  if (op === 'evacuate' && member.status === 'Evacuated') return 'alreadyEvacuated';
  if (op === 'restore' && member.status !== 'Evacuated') return 'notEvacuated';
  if (hostsHa) return 'ha';
  return '';
}

// Counts for the Nodes "Cluster overview" strip.
export function clusterOverview(nodes, vms) {
  const list = Array.isArray(nodes) ? nodes : [];
  const enabled = list.filter((n) => n.enabled !== false);
  return {
    enabled: enabled.length,
    disabled: list.length - enabled.length,
    degraded: enabled.filter((n) => n.status === 'degraded' || n.status === 'offline').length,
    unmanaged: (Array.isArray(vms) ? vms : []).filter((v) => v.managed === false).length,
  };
}
