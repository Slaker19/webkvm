/**
 * Friendly, descriptive network labels (v1.4 Fase 4.1). Raw identifiers
 * like "vmbr0" mean nothing to an operator; the selectors now show
 * "Red Interna (vmbr0)" — the network's name plus the underlying Linux
 * bridge — wherever networks are picked.
 */

/**
 * Human label for a network object: name with the bridge in parens when
 * they differ, otherwise the name itself.
 */
export function networkLabel(net) {
  if (!net) return '';
  if (net.bridge && net.bridge !== net.name) return `${net.name} (${net.bridge})`;
  return net.name;
}

/**
 * Label for a network referenced by an interface row, matched by libvirt
 * network name first, then by bridge device (LXD NICs carry the bridge).
 * Falls back to the raw identifier when nothing matches.
 */
export function networkLabelFor(ident, networks) {
  if (!ident) return '';
  const hit = (networks || []).find((n) => n.name === ident || n.bridge === ident);
  return hit ? networkLabel(hit) : ident;
}

/**
 * Pick the default network for a new machine: the host's PHYSICAL Linux
 * bridge (vmbr0/br0) or a WebKVM network wired to one — never a
 * NAT/virtual bridge. Returns '' when no physical bridge exists, so the
 * backend fails loudly instead of quietly falling back to NAT.
 *
 * Accepts anything and treats a non-array as empty. This ran inside a
 * try/catch that wrapped the entire form load, so being handed the
 * {status, value} wrapper from Promise.allSettled instead of the list
 * threw "find is not a function" and aborted every remaining step,
 * leaving the create screen blank with only "Error loading data".
 * Returning '' degrades to "no network preselected" instead.
 *
 * `allowIncus` is for containers: the managed Incus bridge is a valid
 * explicit choice (NAT), but it must never be the default for a VM.
 */
export function preselectNetwork(nets, { allowIncus = false } = {}) {
  if (!Array.isArray(nets)) return '';
  const virt = /^(virbr|lxdbr|lxcbr|incusbr|docker|br-)/;
  const named = nets.filter((n) => n && typeof n.name === 'string');
  const physical = (n) => !virt.test(n.name) && !virt.test(n.bridge || '');
  const hit =
    named.find((n) => n.name === 'vmbr0' || n.bridge === 'vmbr0') ||
    named.find((n) => n.name === 'br0' || n.bridge === 'br0') ||
    named.find((n) => n.bridge && physical(n)) ||
    named.find((n) => n.forward === 'bridge' && physical(n)) ||
    (allowIncus
      ? named.find((n) => /^incusbr/.test(n.name) || /^incusbr/.test(n.bridge || ''))
      : undefined);
  return hit ? hit.name : '';
}

// Virtual bridges that never put a VM on the LAN by themselves.
const VIRTUAL_BRIDGE_RE = /^(virbr|lxdbr|lxcbr|incusbr|docker|br-)/;
// Incus/LXD bridges look like NAT to libvirt but belong to another daemon:
// calling them "libvirt networks" sends the operator to the wrong place.
const INCUS_BRIDGE_RE = /^(incusbr|lxdbr)/;

/**
 * networkKind: 'host' (a host Linux bridge such as vmbr0/br0: the VM gets an
 * address on the LAN), 'nat' (libvirt/WebKVM NAT network such as virbr0:
 * private address behind this host), 'incus' (an Incus/LXD bridge) or
 * 'isolated'. Uses the backend's kind/forward when present, else the bridge
 * name.
 */
export function networkKind(net) {
  const name = String(net?.bridge || net?.name || '');
  if (INCUS_BRIDGE_RE.test(name) || INCUS_BRIDGE_RE.test(String(net?.name || ''))) return 'incus';
  const kind = String(net?.kind || net?.forward || net?.mode || '').toLowerCase();
  if (kind === 'isolated') return 'isolated';
  if (kind === 'nat' || kind === 'route') return 'nat';
  if (kind === 'direct' || kind === 'bridge') return 'host';
  const dev = net?.bridge || net?.name || '';
  return VIRTUAL_BRIDGE_RE.test(dev) || VIRTUAL_BRIDGE_RE.test(net?.name || '') ? 'nat' : 'host';
}

/** networkGroups: { host, virtual } in the input order. */
export function networkGroups(nets) {
  const out = { host: [], virtual: [] };
  for (const n of Array.isArray(nets) ? nets : []) {
    if (!n || typeof n.name !== 'string') continue;
    (networkKind(n) === 'host' ? out.host : out.virtual).push(n);
  }
  return out;
}

/** networkOptionLabel: option text that says what the VM gets. */
export function networkOptionLabel(t, net) {
  const key = {
    host: 'netOptHost',
    nat: 'netOptNat',
    incus: 'netOptIncus',
    isolated: 'netOptIsolated',
  }[networkKind(net)];
  return t(`vmCreate.${key}`, { name: networkLabel(net) });
}
