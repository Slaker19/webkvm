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
