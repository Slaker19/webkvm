import { workloadRef } from './haWorkloads.js';

// Pure helpers for the Nodes screen (kept out of the component so they can
// be unit-tested without mounting anything).

// Default WebKVM port. A libvirt node URI only tells us the host, so the
// web UI address is the host plus this port (the documented default).
export const DEFAULT_WEBKVM_PORT = 8080;

// webConsoleURL derives the web UI address of a node from its URI, or
// returns null when it cannot be derived (the UI then disables the link).
// http(s) URIs are used as given; anything else (qemu+ssh://user@host/…)
// contributes its host only. Handles IPv6 literals ([::1]) and URIs
// without a user part.
export function webConsoleURL(uri, uiUrl = '') {
  // The operator-network address, when valid, wins over anything derived
  // from the node URI (which is often a private cluster address).
  const ui = validateUIUrl(uiUrl);
  if (ui.ok && ui.value) return `${ui.value}/`;
  const clean = String(uri || '').trim();
  if (!clean) return null;
  let parsed;
  try {
    parsed = new URL(clean);
  } catch {
    return null;
  }
  if (parsed.protocol === 'http:' || parsed.protocol === 'https:') {
    if (!parsed.hostname) return null;
    return `${parsed.protocol}//${parsed.host}/`;
  }
  const host = parsed.hostname;
  if (!host || !clean.includes('//')) return null;
  // URL keeps the brackets of an IPv6 literal in .hostname.
  return `https://${host}:${DEFAULT_WEBKVM_PORT}/`;
}

// usagePercent returns the used share (0..100) or null when the total is
// unknown. An empty resource is 0, not a forced minimum.
export function usagePercent(total, free) {
  if (!(total > 0)) return null;
  const used = total - (free > 0 ? free : 0);
  return Math.max(0, Math.min(100, Math.round((used / total) * 100)));
}

// healingSecondsValid: 0 disables healing, otherwise 60..86400 whole seconds.
export function healingSecondsValid(value) {
  if (value === '' || value == null) return false;
  const n = Number(value);
  if (!Number.isInteger(n)) return false;
  return n === 0 || (n >= 60 && n <= 86400);
}

// codeText translates a backend code with a fallback when the key is
// missing (t() returns the key itself in that case).
export function codeText(t, prefix, code, fallbackKey) {
  const key = `${prefix}.${code}`;
  const out = t(key);
  return out === key ? t(fallbackKey, { code }) : out;
}

// isSshPortOnly: the probe only completed a TCP handshake to the SSH port
// (libvirt_status "tcp_reachable"; "reachable" is what older versions wrote).
// That proves nothing about login or libvirt, so the UI must not call the
// node simply "reachable".
export function isSshPortOnly(node) {
  const s = node?.libvirt_status;
  return (
    (s === 'tcp_reachable' || s === 'reachable') &&
    node?.backend !== 'incus' &&
    node?.type !== 'local'
  );
}

// pingAllOutcome classifies the result of "Ping all".
export function pingAllOutcome(nodes) {
  const list = Array.isArray(nodes) ? nodes : [];
  const total = list.length;
  const online = list.filter((n) => n.status === 'online').length;
  if (total === 0) return { kind: 'none', online, total };
  return { kind: online === total ? 'ok' : 'partial', online, total };
}

// migrationTargets lists the nodes a VM can be migrated to: enabled libvirt
// (or unset backend) nodes other than the VM's current node. When the VM is
// local, the local host is excluded too (it is the source).
export function migrationTargets(nodes, vm, localName = 'local') {
  const list = Array.isArray(nodes) ? nodes : [];
  const current = vm?.node || localName;
  const vmIsLocal = !vm?.node || vm.node === 'local' || vm.node === localName;
  return list.filter(
    (n) =>
      n.enabled !== false &&
      (!n.backend || n.backend === 'libvirt') &&
      n.name !== current &&
      !isSelfNode(n) &&
      !(vmIsLocal && (n.type === 'local' || n.name === 'local' || n.name === localName))
  );
}

// ---------------------------------------------------------------------------
// Address of a node on the operator's real network (`ui_url`).
// KEEP IN SYNC with nodes.ValidateUIURL in backend/internal/nodes/uri.go.

const MAX_UI_URL_LEN = 253;

function stripBrackets(host) {
  const h = String(host || '').trim();
  return h.startsWith('[') && h.endsWith(']') ? h.slice(1, -1) : h;
}

function ipv4Octets(host) {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (!m) return null;
  const o = m.slice(1).map(Number);
  return o.every((n) => n <= 255) ? o : null;
}

// ipv6Head: first 16-bit group of an IPv6 literal (0 for "::..."), or null.
function ipv6Head(host) {
  if (!host.includes(':') || !/^[0-9a-f:.]+$/i.test(host)) return null;
  const first = host.split(':')[0];
  return first === '' ? 0 : parseInt(first, 16);
}

// isPrivateHost: the host only makes sense inside a private network:
// RFC1918 (10/8, 172.16/12, 192.168/16), IPv6 ULA fc00::/7, link-local
// (169.254/16, fe80::/10) and loopback. Names are never classified.
export function isPrivateHost(host) {
  const h = stripBrackets(host).toLowerCase();
  if (!h) return false;
  const o = ipv4Octets(h);
  if (o) {
    return (
      o[0] === 10 ||
      (o[0] === 172 && o[1] >= 16 && o[1] <= 31) ||
      (o[0] === 192 && o[1] === 168) ||
      (o[0] === 169 && o[1] === 254) ||
      o[0] === 127
    );
  }
  if (h === '::1') return true;
  const head = ipv6Head(h);
  if (head == null || Number.isNaN(head)) return false;
  return (head & 0xfe00) === 0xfc00 || (head & 0xffc0) === 0xfe80;
}

// uriHost: bare host of a node URI (IPv6 brackets removed), '' if none.
export function uriHost(uri) {
  const clean = String(uri || '').trim();
  if (!clean.includes('//')) return '';
  try {
    return stripBrackets(new URL(clean).hostname);
  } catch {
    return '';
  }
}

function hostForUrl(host) {
  return host.includes(':') ? `[${host}]` : host;
}

// suggestUIUrl: https://<host>:8080 built from the node URI host, only when
// that host is not private (a private host is the cluster NIC, which the
// operator cannot reach). null otherwise.
export function suggestUIUrl(uri) {
  const host = uriHost(uri);
  if (!host || isPrivateHost(host)) return null;
  return `https://${hostForUrl(host)}:${DEFAULT_WEBKVM_PORT}`;
}

const HOSTNAME_RE = /^(?!-)[a-z0-9-]{1,63}(?<!-)$/;

// validateUIUrl mirrors the backend: returns {ok, value} with the normalized
// scheme://host[:port], or {ok:false, error} where error is one of
// chars | length | invalid | scheme | host | credentials | query | path |
// loopback | port (i18n key nodes.uiUrlErrors.<error>). Empty is valid.
export function validateUIUrl(raw) {
  const s = String(raw ?? '').trim();
  if (!s) return { ok: true, value: '' };
  const fail = (error) => ({ ok: false, error });
  if (s.length > MAX_UI_URL_LEN) return fail('length');
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u0020\u007f]/.test(s)) return fail('chars');
  let u;
  try {
    u = new URL(s);
  } catch {
    return fail('invalid');
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return fail('scheme');
  if (!/^https?:\/\//i.test(s)) return fail('scheme');
  if (!u.hostname) return fail('host');
  if (u.username || u.password || /^https?:\/\/[^/?#]*@/i.test(s)) return fail('credentials');
  if (s.includes('?') || s.includes('#')) return fail('query');
  if (u.pathname !== '/' && u.pathname !== '') return fail('path');
  const host = stripBrackets(u.hostname).toLowerCase();
  const o = ipv4Octets(host);
  if (o) {
    if (o[0] === 127 || o.every((n) => n === 0) || (o[0] === 169 && o[1] === 254)) {
      return fail('loopback');
    }
  } else if (host.includes(':')) {
    const head = ipv6Head(host);
    if (head == null || Number.isNaN(head)) return fail('host');
    if (host === '::1' || host === '::' || (head & 0xffc0) === 0xfe80) return fail('loopback');
  } else {
    if (host === 'localhost' || host.endsWith('.localhost')) return fail('loopback');
    if (host.length > 253 || !host.split('.').every((l) => HOSTNAME_RE.test(l))) {
      return fail('host');
    }
  }
  // URL drops default ports; the backend keeps what was typed.
  const pm = /^https?:\/\/(?:\[[^\]]*\]|[^:/]*):(\d*)\/?$/i.exec(s);
  let port = '';
  if (pm) {
    const n = Number(pm[1]);
    if (!pm[1] || !Number.isInteger(n) || n < 1 || n > 65535) return fail('port');
    port = `:${n}`;
  }
  return { ok: true, value: `${u.protocol}//${hostForUrl(host)}${port}` };
}

// nodeConsoleURL: where to open a node's own WebKVM from the operator's PC.
// ui_url first; else the URI host only when it is not private; else null
// (the UI disables the button and explains why).
export function nodeConsoleURL(node) {
  if (!node) return null;
  const ui = validateUIUrl(node.ui_url);
  if (ui.ok && ui.value) return `${ui.value}/`;
  if (isPrivateHost(uriHost(node.uri))) return null;
  return webConsoleURL(node.uri);
}

// nodeAddressInfo: what a node card shows. `cluster` is the host of the node
// URI (the address WebKVM itself uses); `uiUrl` the operator-network address.
export function nodeAddressInfo(node) {
  const cluster = uriHost(node?.uri);
  const ui = validateUIUrl(node?.ui_url);
  const uiUrl = ui.ok ? ui.value : '';
  const privateBadge = !uiUrl && isPrivateHost(cluster);
  return { cluster, uiUrl, privateBadge, needsHint: privateBadge };
}

// ---------------------------------------------------------------------------
// Self-registration and access state (backend fields `is_self`, `access`).

// isSelfNode: a remote registry entry that points at THIS server (registered
// before the control plane moved here). The backend flags it with is_self and
// resolves ?node= for it locally; the UI shows the server once.
export function isSelfNode(node) {
  return !!node && node.type !== 'local' && node.is_self === true;
}

// selfDuplicates returns the remote entries that duplicate this server.
export function selfDuplicates(nodes) {
  return (Array.isArray(nodes) ? nodes : []).filter(isSelfNode);
}

// withoutSelfDuplicates drops the duplicate entries so a list, selector or
// filter shows this server only once (as the local node).
export function withoutSelfDuplicates(nodes) {
  return (Array.isArray(nodes) ? nodes : []).filter((n) => !isSelfNode(n));
}

// nodeHealth is the single classification every status surface uses:
// 'online' | 'no_access' (the host answers but WebKVM cannot log in over SSH
// yet) | 'degraded' | 'offline' | 'disabled' | 'unknown'.
export function nodeHealth(node) {
  if (!node) return 'unknown';
  if (node.enabled === false) return 'disabled';
  const s = node.status;
  if (s === 'offline') return 'offline';
  if (s === 'degraded') return 'degraded';
  if (s === 'online') return node.access === 'not_set_up' ? 'no_access' : 'online';
  return 'unknown';
}

const lc = (s) =>
  String(s ?? '')
    .trim()
    .toLowerCase();

// canonicalNodeName: the node key a VM/filter should use. Empty, "local" and
// the self-duplicate entry (by name or id) all mean this server.
export function canonicalNodeName(name, nodes, localName = 'local') {
  const key = lc(name);
  if (!key || key === 'local') return localName;
  const self = selfDuplicates(nodes).some((n) => lc(n.name) === key || lc(n.id) === key);
  return self ? localName : name;
}

// mergeSelfVms: VMs reported through the self-duplicate entry belong to this
// server. Move them there (dropping node_id, so actions go local) and drop
// the ones this server already lists itself (same workload identity).
export function mergeSelfVms(vms, nodes, localName = 'local') {
  const list = Array.isArray(vms) ? vms : [];
  const dups = selfDuplicates(nodes);
  if (!dups.length) return list;
  // A VM named after the duplicate is local when that name is also ours.
  const fromDup = (v) =>
    dups.some(
      (n) =>
        (v.node_id && v.node_id === n.id) ||
        (!v.node_id && lc(v.node) === lc(n.name) && lc(n.name) !== lc(localName))
    );
  const localKeys = new Set(list.filter((v) => !fromDup(v)).map((v) => workloadRef(v)?.key));
  const out = [];
  for (const v of list) {
    if (!fromDup(v)) {
      out.push(v);
      continue;
    }
    if (localKeys.has(workloadRef(v)?.key)) continue;
    const { node_id: _dropped, ...rest } = v;
    out.push({ ...rest, node: localName });
  }
  return out;
}

// nodeHealthCounts: every count shown on the Nodes page comes from nodeHealth,
// with this server counted once (self duplicates excluded).
export function nodeHealthCounts(nodes) {
  const c = {
    total: 0,
    enabled: 0,
    disabled: 0,
    online: 0,
    no_access: 0,
    degraded: 0,
    offline: 0,
    unknown: 0,
  };
  for (const n of withoutSelfDuplicates(nodes)) {
    c.total++;
    const h = nodeHealth(n);
    if (h === 'disabled') {
      c.disabled++;
      continue;
    }
    c.enabled++;
    c[h]++;
  }
  return c;
}

// findNodeByHost: registry node named `host` (Pacemaker node name) or whose
// URI points at that host. Self duplicates resolve to the local node.
function findNodeByHost(nodes, host) {
  const key = lc(host);
  if (!key) return null;
  const list = Array.isArray(nodes) ? nodes : [];
  const hit =
    list.find((n) => lc(n.name) === key) || list.find((n) => lc(uriHost(n.uri)) === key) || null;
  if (hit && isSelfNode(hit)) return list.find((n) => n.type === 'local') || hit;
  return hit;
}

// missingHaWorkloads: HA workloads (GET /api/ha/workloads) that run on a node
// but are absent from the VM list, with the reason the UI should give:
// not_registered | no_access | offline | disabled | not_listed.
export function missingHaWorkloads(workloads, vms, nodes) {
  const shown = new Set((Array.isArray(vms) ? vms : []).map((v) => workloadRef(v)?.key));
  const out = [];
  for (const w of Array.isArray(workloads) ? workloads : []) {
    if (!w?.name || !w.node || shown.has(w.key)) continue;
    const node = findNodeByHost(nodes, w.node);
    let reason = 'not_listed';
    if (!node) reason = 'not_registered';
    else if (node.type !== 'local' && !isSelfNode(node)) {
      const h = nodeHealth(node);
      if (h === 'no_access' || h === 'offline' || h === 'disabled') reason = h;
    }
    out.push({
      key: w.key,
      name: w.name,
      node: w.node,
      reason,
      nodeId: node && node.type !== 'local' ? node.id : '',
    });
  }
  return out;
}

// foldHaMissing avoids two notices for one cause: an HA workload hidden
// because its node is offline / without SSH access is listed under that
// node in the health banner (byNode: node id -> workload names) instead of
// in a second notice. `rest` keeps the reasons the banner does not cover.
export function foldHaMissing(missing, issues) {
  const covered = new Set((Array.isArray(issues) ? issues : []).map((i) => i.id));
  const rest = [];
  const byNode = {};
  for (const w of Array.isArray(missing) ? missing : []) {
    if (w.nodeId && covered.has(w.nodeId) && w.reason !== 'not_registered') {
      (byNode[w.nodeId] ||= []).push(w.name);
    } else {
      rest.push(w);
    }
  }
  return { rest, byNode };
}
