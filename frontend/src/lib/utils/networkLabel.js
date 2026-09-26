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
 */
export function preselectNetwork(nets) {
  if (!Array.isArray(nets)) return '';
  const virt = /^(virbr|lxdbr|lxcbr|docker|br-)/;
  const named = nets.filter((n) => n && typeof n.name === 'string');
  const physical = (n) => !virt.test(n.name) && !virt.test(n.bridge || '');
  const hit =
    named.find((n) => n.name === 'vmbr0' || n.bridge === 'vmbr0') ||
    named.find((n) => n.name === 'br0' || n.bridge === 'br0') ||
    named.find((n) => n.bridge && physical(n)) ||
    named.find((n) => n.forward === 'bridge' && physical(n));
  return hit ? hit.name : '';
}
