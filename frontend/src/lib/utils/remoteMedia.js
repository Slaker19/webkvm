// remoteMediaFields maps the ISO / virtio ISO picked in the create form (the
// option values are the `path` of entries from GET /storage/isos?node=) to
// the fields a REMOTE libvirt node accepts: volume names of its own pools
// plus the pool, never filesystem paths. A value that is not in the node's
// listing is dropped rather than sent as a path.
export function remoteMediaFields(isos, iso, virtioISO) {
  const list = Array.isArray(isos) ? isos : [];
  const find = (path) => (path ? list.find((entry) => entry && entry.path === path) : null);
  const out = {};
  const main = find(iso);
  if (main) {
    out.iso = main.name;
    out.iso_pool = main.pool || undefined;
  } else {
    out.iso = undefined;
  }
  const virtio = find(virtioISO);
  if (virtio) {
    out.virtio_iso = virtio.name;
    out.virtio_iso_pool = virtio.pool || undefined;
  } else {
    out.virtio_iso = undefined;
  }
  return out;
}
