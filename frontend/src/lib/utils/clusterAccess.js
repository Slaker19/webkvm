// Pure helpers for the managed cluster SSH access card.

export const SSH_TEST_CODES = [
  'no_key',
  'host_key_unpinned',
  'host_key_changed',
  'ssh_auth_denied',
  'node_unreachable',
  'node_timeout',
  'libvirt_unavailable',
  'transport_unsupported',
];

// A test older than this no longer proves the access works today.
export const ACCESS_FRESH_MS = 10 * 60 * 1000;

// Codes that mean the managed access itself is not set up (or is wrong):
// these block creating VMs on the node. Anything else (node_unreachable,
// node_timeout, libvirt_unavailable, ...) is a transient/other problem and
// never says "access not set up".
export const SETUP_PROBLEM_CODES = [
  'ssh_auth_denied',
  'no_key',
  'host_key_unpinned',
  'host_key_changed',
];

// Access state of a node from GET /cluster/ssh. `node.access` is the verdict
// of the periodic node probe (GET /nodes, also carried by /cluster/ssh): it
// logs in over SSH every ~30 s, so it is the single source every screen uses.
//   'changed'    pinned key no longer matches (red)
//   'ready'      the probe says access ok, or host key pinned and a test
//                passed within the last 10 minutes
//   'unverified' host key pinned but the last test is missing, stale or failed
//                for a non-setup reason: neutral, a one-click test settles it
//   'not_set_up' host key not pinned, or the last test says the key/host key
//                setup is wrong
export function accessState(node, now = Date.now()) {
  if (!node) return 'not_set_up';
  const hk = node.host_key?.state;
  const lt = node.last_test;
  if (hk === 'changed' || lt?.code === 'host_key_changed') return 'changed';
  if (node.access === 'ok') return 'ready';
  if (node.access === 'not_set_up') return 'not_set_up';
  if (hk !== 'pinned') return 'not_set_up';
  if (lt?.ok === false && SETUP_PROBLEM_CODES.includes(lt.code)) return 'not_set_up';
  const at = lt?.at ? Date.parse(lt.at) : NaN;
  if (lt?.ok === true && Number.isFinite(at) && now - at <= ACCESS_FRESH_MS) return 'ready';
  return 'unverified';
}

// withProbeAccess overlays the node-list probe verdict (access/access_code
// of a GET /nodes entry) on a GET /cluster/ssh row, so a screen holding a
// fresher node list never contradicts it. Missing values keep the row's own.
export function withProbeAccess(sshRow, registryNode) {
  if (!sshRow) return sshRow;
  if (!registryNode?.access) return sshRow;
  return { ...sshRow, access: registryNode.access, access_code: registryNode.access_code };
}

// Only a real setup problem blocks creation; an unverified state does not.
export const accessBlocksCreate = (state) => state === 'not_set_up' || state === 'changed';

// Translated badge text of an access state.
export function accessStateLabel(t, state) {
  return t(
    {
      ready: 'clusterAccess.statusReady',
      changed: 'clusterAccess.statusChanged',
      unverified: 'clusterAccess.statusUnverified',
    }[state] || 'clusterAccess.statusNotSetUp'
  );
}

// Current step of the guided flow for a node. Steps: key, authorize,
// hostkey, test, done. `authorize` is only "done" once a test proved it.
export function currentStep(overview, node) {
  if (!overview?.configured) return 'key';
  const hk = node?.host_key?.state;
  if (hk !== 'pinned') return node?.authorized === true ? 'hostkey' : 'authorize';
  if (node?.access === 'ok' || node?.last_test?.ok === true) return 'done';
  return 'test';
}

// Translate a backend code with the same fallback convention used
// elsewhere: an unknown code never shows a raw key.
export function codeLabel(t, prefix, code, fallbackKey) {
  if (!code) return '';
  const key = `${prefix}.${code}`;
  const out = t(key);
  return out !== key ? out : t(fallbackKey, { code });
}

// Error message for an API failure of the SSH endpoints.
export function sshErrorText(t, e) {
  const code = e?.code;
  if (code) {
    const key = `clusterAccess.errors.${code}`;
    const out = t(key);
    if (out !== key) return out;
  }
  return e?.message || String(e);
}

// Shortened fingerprint for tight spaces (keeps the SHA256: prefix).
export function shortFingerprint(fp) {
  if (!fp) return '';
  return fp.length > 24 ? `${fp.slice(0, 15)}…${fp.slice(-6)}` : fp;
}
