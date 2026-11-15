// VMs created from this browser that no listing includes yet (8.29 #3: a VM
// created on a remote node appeared ~12 s later). VmCreate adds the entry
// from the create response; VmList merges it into every listing until a
// listing contains it (same id, or same name on the same node) or it ages.
// Plain module state: one tab, short-lived, nothing to persist.

const PENDING_TTL_MS = 2 * 60 * 1000;
let pending = [];

export function addPendingVm(vm, now = Date.now()) {
  if (!vm || (!vm.id && !vm.name)) return;
  pending = pending.filter((p) => !same(p.vm, vm));
  pending.push({
    at: now,
    vm: {
      type: 'vm',
      hypervisor: 'kvm',
      state: 'shutoff',
      disks: [],
      networks: [],
      ...vm,
      pending: true,
    },
  });
}

export function clearPendingVms() {
  pending = [];
}

function same(a, b) {
  if (a.id && b.id && a.id === b.id) return true;
  const nodeA = a.node_id || a.node || '';
  const nodeB = b.node_id || b.node || '';
  return !!a.name && a.name === b.name && nodeA === nodeB;
}

// mergePendingVms: the listing plus the pending VMs it does not contain yet.
// Entries the listing already contains, or older than the TTL, are dropped.
export function mergePendingVms(list, now = Date.now()) {
  const vms = Array.isArray(list) ? list : [];
  pending = pending.filter((p) => now - p.at < PENDING_TTL_MS && !vms.some((v) => same(v, p.vm)));
  return pending.length ? [...vms, ...pending.map((p) => p.vm)] : vms;
}
