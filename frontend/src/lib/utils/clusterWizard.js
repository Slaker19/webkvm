// Pure helpers for the "create the cluster" wizard (HA Phase 2). Kept out of
// the component so the validation rules are unit-testable on their own.

// Cluster and node names end up in corosync.conf / pcs: letters, digits and
// hyphen, starting with an alphanumeric, at most 63 characters.
export const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/;

export function isIPv4(v) {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(String(v || '').trim());
  return !!m && m.slice(1).every((o) => Number(o) <= 255);
}

export function isIPv6(v) {
  const s = String(v || '').trim();
  if (!s.includes(':') || !/^[0-9a-fA-F:.]+$/.test(s)) return false;
  if ((s.match(/::/g) || []).length > 1 || /:::/.test(s)) return false;
  const groups = s.split(':');
  if (groups.length > 8 || (!s.includes('::') && groups.length !== 8)) return false;
  return groups.every((g) => g === '' || /^[0-9a-fA-F]{1,4}$/.test(g) || isIPv4(g));
}

export const isIP = (v) => isIPv4(v) || isIPv6(v);

export function validateClusterName(name) {
  return NAME_RE.test(String(name || '').trim()) ? null : 'name_invalid';
}

// validateNodes returns per-row field errors plus a count verdict:
//   count: 'ok' | 'too_few' (<2: never possible) | 'two_nodes' (needs the
//   explicit advanced opt-in, otherwise a quorum risk).
export function validateNodes(nodes, { allowTwo = false } = {}) {
  const rows = (nodes || []).map(() => ({ name: null, address: null }));
  const seenName = new Map();
  const seenAddr = new Map();
  (nodes || []).forEach((n, i) => {
    const name = String(n.name || '').trim();
    const addr = String(n.address || '').trim();
    if (!NAME_RE.test(name)) rows[i].name = 'name_invalid';
    else if (seenName.has(name.toLowerCase())) rows[i].name = 'name_duplicate';
    else seenName.set(name.toLowerCase(), i);
    if (!isIP(addr)) rows[i].address = 'address_invalid';
    else if (seenAddr.has(addr.toLowerCase())) rows[i].address = 'address_duplicate';
    else seenAddr.set(addr.toLowerCase(), i);
  });
  const total = (nodes || []).length;
  const count = total < 2 ? 'too_few' : total === 2 ? 'two_nodes' : 'ok';
  const rowsOk = rows.every((r) => !r.name && !r.address);
  const countOk = count === 'ok' || (count === 'two_nodes' && allowTwo);
  return { rows, count, valid: rowsOk && countOk };
}

export function validateTimeout(v) {
  if (v === '' || v == null) return null;
  const n = Number(v);
  return Number.isInteger(n) && n >= 5 && n <= 120 ? null : 'timeout_invalid';
}

// buildPlanBody builds the body shared by /plan and /setup. Empty optional
// fields are omitted so the backend applies its recommended defaults.
export function buildPlanBody({
  clusterName,
  nodes,
  watchdogDevice = '',
  timeoutSeconds = '',
  fencing = [],
  allowTwo = false,
}) {
  const body = {
    cluster_name: String(clusterName || '').trim(),
    nodes: (nodes || []).map((n) => ({
      name: String(n.name || '').trim(),
      address: String(n.address || '').trim(),
    })),
    sbd: {},
  };
  const dev = String(watchdogDevice || '').trim();
  if (dev) body.sbd.watchdog_device = dev;
  if (timeoutSeconds !== '' && timeoutSeconds != null)
    body.sbd.timeout_seconds = Number(timeoutSeconds);
  if (fencing?.length) body.fencing = [...fencing];
  if (allowTwo) body.allow_two_nodes = true;
  return body;
}

export function buildSetupBody(planBody, { sbdEnable = true, enableOnBoot = true } = {}) {
  return { ...planBody, confirm: true, sbd_enable: !!sbdEnable, enable_on_boot: !!enableOnBoot };
}

// Host part of a registered node URI (qemu+ssh://user@host/system,
// https://[::1]:8443) or '' when there is none.
export function hostFromUri(uri) {
  try {
    const u = new URL(String(uri || '').trim());
    return u.hostname.replace(/^\[|\]$/g, '');
  } catch {
    return '';
  }
}

// Registered node -> editor row. The node name is sanitised to the pcs
// charset; the user can still edit it.
export function rowsFromRegistered(registered) {
  const out = [];
  for (const n of registered || []) {
    if (n.enabled === false) continue;
    const address = hostFromUri(n.uri);
    if (!isIP(address)) continue;
    const name = String(n.name || '')
      .toLowerCase()
      .replace(/[^a-z0-9-]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 63);
    out.push({ name, address });
  }
  return out;
}

// Per-node manual steps. The hacluster password is NEVER collected by the
// wizard: the operator sets it on each node and authenticates with pcs.
export function manualStepCommands(nodes) {
  const names = (nodes || []).map((n) => String(n.name || '').trim()).filter(Boolean);
  return {
    passwd: 'sudo passwd hacluster',
    hostAuth: `sudo pcs host auth ${names.join(' ')} -u hacluster`,
  };
}

function pcsName(name) {
  return String(name || '')
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63);
}

// prefillRows builds the editor rows from the node registry and, when a
// Pacemaker membership is known, its members:
//   - the local node first, named after this server; its address comes from
//     a remote entry that is this same server (is_self), else stays empty for
//     the operator. The browser's host is never used: it may be the VIP.
//   - every other registered libvirt node with an IP address;
//   - every Pacemaker member not listed yet (name only).
// No row appears twice (same name or same non-empty address).
export function prefillRows({ nodes = [], pacemakerNames = [] } = {}) {
  const list = Array.isArray(nodes) ? nodes : [];
  const local = list.find((n) => n.type === 'local');
  const selfAddr = list
    .filter((n) => n.type !== 'local' && n.is_self === true)
    .map((n) => hostFromUri(n.uri))
    .find((a) => isIP(a));
  const out = [];
  const push = (row) => {
    if (!row.name && !row.address) return;
    const dupe = out.some(
      (r) => (row.name && r.name === row.name) || (row.address && r.address === row.address)
    );
    if (!dupe) out.push(row);
  };
  if (local) push({ name: pcsName(local.name), address: selfAddr || '' });
  for (const r of rowsFromRegistered(
    list.filter((n) => n.type !== 'local' && n.is_self !== true)
  )) {
    push(r);
  }
  for (const m of Array.isArray(pacemakerNames) ? pacemakerNames : []) {
    push({ name: pcsName(m), address: '' });
  }
  return out;
}

// existingCluster returns { name, nodes } when the readiness report shows a
// configured Pacemaker cluster, else null.
export function existingCluster(readiness) {
  const pm = readiness?.pacemaker;
  if (!pm) return null;
  const names = pm.node_names?.length
    ? pm.node_names
    : (pm.node_states || []).map((n) => n.name).filter(Boolean);
  if (!names.length) return null;
  return { name: pm.cluster_name || '', nodes: [...names] };
}
