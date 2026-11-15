// effectiveBootOrder keeps a VM created without installation media from
// booting an empty CD-ROM first. A real ISO still keeps the requested order.
export function effectiveBootOrder(bootOrder, iso) {
  if (bootOrder === 'cdrom' && !String(iso ?? '').trim()) return 'disk';
  return bootOrder || 'disk';
}
