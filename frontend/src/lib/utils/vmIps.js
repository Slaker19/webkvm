/**
 * All IPv4 addresses of a VM/container, in display order. Containers with
 * several NICs expose one IP per interface (vm.ips); older payloads only
 * carried the primary (vm.ip). Falls back gracefully to that.
 */
export function vmIps(vm) {
  if (vm?.ips?.length) return vm.ips;
  return vm?.ip ? [vm.ip] : [];
}