/**
 * Shared instance-type helpers (PLAN-LXD 5.2 / 5.3). Single source of
 * truth for the KVM/Incus identity badge on the VM card and the
 * provisioning footer chip — mirrors the vmState.js pattern and is kept
 * pure so it can be unit-tested with Vitest (node env, no jsdom).
 * v2.2.0: the container backend is Incus (LXD fork); the card badge shows
 * "Incus" while full labels elsewhere read "Incus / LXC".
 */

const BADGE_CLASS = {
  vm: 'border-accent/30 bg-accent/10 text-accent',
  container: 'border-[#b45309]/30 bg-[#b45309]/10 text-[#d97706]',
};

const TYPE_LABEL = {
  vm: 'KVM',
  container: 'Incus',
};

/** Tailwind classes for the type badge (neutral accent for KVM, amber for Incus/LXC). */
export function computeTypeBadgeClass(type) {
  return BADGE_CLASS[type] || BADGE_CLASS.vm;
}

/** Short badge label: "KVM" or "Incus". */
export function computeTypeLabel(type) {
  return TYPE_LABEL[type] || TYPE_LABEL.vm;
}

/**
 * True when a VM object is a container (Incus or legacy LXD). A container
 * is identified by its type ("container") or its hypervisor ("incus"); the
 * hypervisor check also covers Incus virtual machines down the road.
 */
export function isContainer(vm) {
  if (!vm) return false;
  return vm.type === 'container' || vm.hypervisor === 'incus';
}

/**
 * Provisioning chip data for the footer (PLAN-LXD 5.2): the backend
 * exposes `provision_method` ("cloud-init" | "script" | "seed-iso" | "").
 * Returns null when there is nothing to show, so callers can skip the
 * chip entirely for plain instances.
 */
export function provisionChip(method) {
  switch (method) {
    case 'cloud-init':
      return { icon: 'cloud-cog', label: 'cloud-init' };
    case 'script':
      return { icon: 'square-terminal', label: 'script' };
    case 'seed-iso':
      return { icon: 'box', label: 'seed-iso' };
    default:
      return null;
  }
}
