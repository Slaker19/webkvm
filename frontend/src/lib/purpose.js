// Pool purposes are single values: every purpose is its own independent
// pool rooted at its own folder (mydisk-vdi, mydisk-isos,
// mydisk-containers, mydisk-backups, mydisk-plantillas). The CSV
// parsing below only survives for legacy registry rows; nothing writes
// multi-purpose pools anymore.
const ORDER = ['disk', 'container', 'iso', 'backup', 'template'];
const LABELS = {
  disk: 'KVM',
  container: 'LXC',
  iso: 'ISO',
  backup: 'Backup',
  template: 'Template',
};
const ICONS = {
  disk: 'hardDrive',
  container: 'boxes',
  iso: 'disc',
  backup: 'archive',
  template: 'package',
};
const KNOWN = ORDER;

/** Normalized purpose list of a pool (accepts the pool or a raw string). */
export function purposeList(p) {
  const raw = typeof p === 'string' ? p : (p && p.purpose) || '';
  const list = raw
    .split(',')
    .map((s) => s.trim())
    .map((s) => (s === 'lxc' ? 'container' : s))
    .filter((s) => KNOWN.includes(s));
  return list.length ? list : ['disk'];
}

/** True when the pool has the given nature (membership, not equality). */
export function hasPurpose(p, v) {
  const needle = v === 'lxc' ? 'container' : v;
  return purposeList(p).includes(needle);
}

/** True when the pool carries the ISO nature and nothing else. */
export function isIsoOnly(p) {
  const list = purposeList(p);
  return list.length === 1 && list[0] === 'iso';
}

/**
 * True when the pool can back VM disks / backup folders — everything
 * except a pure-ISO pool and the neutral backup/template pools (they
 * are listed in the grid but never offered as VM disk targets).
 */
export function usableForDisks(p) {
  return !isIsoOnly(p) && !hasPurpose(p, 'backup') && !hasPurpose(p, 'template');
}

/**
 * Pool states that can back a new instance. The backends disagree on
 * the word: libvirt says "active", Incus says "created". Matching only
 * "active" hid every working Incus pool from the deploy selector.
 */
const POOL_STATE_USABLE = new Set(['active', 'created']);

/**
 * The pools a deploy may target, given what is being deployed.
 *
 * A container's root disk lives in an Incus pool and a VM's disk image
 * in a libvirt pool; the two are not interchangeable, so the same
 * selector must never mix them. Pools outside the caller's entitlement
 * are dropped as well: offering one only for the server to answer 403
 * is worse than not offering it at all.
 *
 * `allowedPools` empty means unrestricted (admins, and users with no
 * per-pool ACL), which matches how the backend reads AllowedPools.
 */
export function deployablePools(pools, targetType, allowedPools = []) {
  const wantContainer = targetType === 'container';
  return (pools || []).filter((p) => {
    // A VM needs the disk nature explicitly, not merely "not ISO":
    // usableForDisks alone let a container-only pool through, which the
    // libvirt side would then reject.
    const ok = wantContainer
      ? hasPurpose(p, 'container')
      : hasPurpose(p, 'disk') && usableForDisks(p);
    if (!ok) return false;
    if (p.state && !POOL_STATE_USABLE.has(String(p.state).toLowerCase())) return false;
    if (allowedPools.length && !allowedPools.includes(p.name)) return false;
    return true;
  });
}

/**
 * The pools that may hold a KVM disk image, for every selector that
 * allocates one (create, clone, add disk, instantiate, import).
 *
 * This exists because `usableForDisks` is NOT that predicate: it only
 * strips ISO/backup/template pools, so a container (Incus) pool passed
 * straight through and the create form happily offered an Incus pool
 * for a KVM disk. libvirt then failed with "no storage pool with
 * matching name", and for the ISO pool — a real libvirt directory pool
 * — it did not fail at all: the qcow2 landed among the install media.
 */
export function vmDiskPools(pools, allowedPools = []) {
  return deployablePools(pools, 'vm', allowedPools);
}

/** The mirror image: pools that may hold an Incus container. */
export function containerPools(pools, allowedPools = []) {
  return deployablePools(pools, 'container', allowedPools);
}

/**
 * The pools a cached base cloud image may live in: disk pools plus
 * template pools.
 *
 * This is deliberately NOT vmDiskPools: a base image is a golden
 * image, not a disk in daily use, and the template purpose exists
 * precisely to hold them apart from the disks VMs are running on.
 * Mirrors baseImagePoolPurpose() in backend/internal/api/image_hub.go,
 * which is the authority — offering a pool the server would refuse is
 * worse than not offering it.
 *
 * `currentPool` drops the pool an image already sits in, so the same
 * function serves both the download selector (no current pool) and the
 * move selector.
 */
export function baseImagePools(pools, currentPool = '', allowedPools = []) {
  return (pools || []).filter((p) => {
    if (!hasPurpose(p, 'disk') && !hasPurpose(p, 'template')) return false;
    // A container pool is Incus storage: it can never hold a qcow2.
    if (hasPurpose(p, 'container')) return false;
    if (p.name === currentPool) return false;
    if (p.state && !POOL_STATE_USABLE.has(String(p.state).toLowerCase())) return false;
    if (allowedPools.length && !allowedPools.includes(p.name)) return false;
    return true;
  });
}

/**
 * Pools a volume of `kind` ("disk" | "iso" | "container") may be moved
 * into, excluding the one it is already in.
 *
 * The kind must match exactly. Storage worlds are never mixed: an ISO
 * belongs in an ISO pool, a VM disk in a disk pool and a container in a
 * container pool, and the backend refuses anything else — so offering a
 * wrong-kind pool here would only produce a guaranteed failure.
 */
export function movablePools(pools, kind, currentPool = '') {
  return (pools || []).filter((p) => {
    if (!hasPurpose(p, kind)) return false;
    // Disks additionally exclude the neutral backup/template pools,
    // which are listed in the grid but are not disk targets.
    if (kind === 'disk' && !usableForDisks(p)) return false;
    if (p.name === currentPool) return false;
    if (p.state && !POOL_STATE_USABLE.has(String(p.state).toLowerCase())) return false;
    return true;
  });
}

/**
 * The pools WebKVM creates and maintains on the system disk. They are
 * not deletable: the server recreates them on the next start, so
 * deleting one only detaches libvirt from a directory that still holds
 * the operator's disks and ISOs.
 *
 * Mirrors isBuiltinPool() in backend/internal/api/storage.go, which is
 * the authority — hiding the button was never enough on its own, since
 * the API is reachable directly.
 *
 * "ISOS" is the pre-v2.5 name of the ISO library, kept so an install
 * whose rename migration has not run yet is still protected.
 */
const BUILTIN_POOLS = new Set(['webkvm-disks', 'webkvm-isos', 'webkvm-incus', 'ISOS']);

/** True when the pool is one WebKVM owns and must not be deleted. */
export function isBuiltinPool(name) {
  return BUILTIN_POOLS.has(name);
}

/** Canonical badge label: "KVM · LXC · ISO". */
export function purposeLabel(p) {
  const list = purposeList(p);
  return ORDER.filter((v) => list.includes(v))
    .map((v) => LABELS[v])
    .join(' · ');
}

/** Canonical icon names, in display order. */
export function purposeIcons(p) {
  const list = purposeList(p);
  return ORDER.filter((v) => list.includes(v)).map((v) => ICONS[v]);
}

// ---------------------------------------------------------------------
// Init-disk pool derivation
//
// These three maps MIRROR the backend, which is the authority:
//   FOLDER_NATURE  <-> subfolderNature   (api/pools_init.go)
//   POOL_SUFFIX    <-> folderPoolSuffix  (api/pools_init.go)
//   backend router <-> subfolderPoolSpec (container -> Incus)
// They exist so the init-disk dialog can show the exact pool names it
// is about to create BEFORE the request. Changing a suffix on either
// side without the other makes the preview lie — purpose.test.js
// pins the values so that divergence fails a test instead of shipping.
// ---------------------------------------------------------------------

/** Init-disk subfolder -> the pool nature it carries. */
export const FOLDER_NATURE = {
  isos: 'iso',
  discos: 'disk',
  disks: 'disk',
  images: 'disk',
  contenedores: 'container',
  containers: 'container',
  backups: 'backup',
  plantillas: 'template',
  templates: 'template',
};

/** Pool nature -> suffix appended to the disk's volume name. */
export const POOL_SUFFIX = {
  disk: '-vdi',
  iso: '-isos',
  container: '-containers',
  backup: '-backups',
  template: '-plantillas',
};

/** Which storage backend owns a pool of this nature. */
export function backendFor(purpose) {
  return purpose === 'container' ? 'Incus' : 'libvirt';
}

/**
 * The independent pool an init-disk subfolder produces, or null when
 * the folder carries no nature. `volumeName` is the disk's volume
 * name; `placeholder` stands in while the operator hasn't typed one.
 */
export function poolForFolder(folderId, volumeName, placeholder = '…') {
  const purpose = FOLDER_NATURE[folderId];
  if (!purpose) return null;
  const base = (volumeName || '').trim() || placeholder;
  return {
    name: `${base}${POOL_SUFFIX[purpose]}`,
    purpose,
    folder: `/${folderId}`,
    backend: backendFor(purpose),
    icon: purposeIcons(purpose)[0],
  };
}

/**
 * The full set of independent pools an init-disk run creates — one per
 * marked folder that carries a nature, deduped by pool name (discos +
 * disks both mean "disk"). Never a unified multi-purpose pool.
 */
export function poolsForFolders(folderIds, volumeName, placeholder = '…') {
  const out = [];
  const seen = new Set();
  for (const id of folderIds || []) {
    const pool = poolForFolder(id, volumeName, placeholder);
    if (!pool || seen.has(pool.name)) continue;
    seen.add(pool.name);
    out.push(pool);
  }
  return out;
}
