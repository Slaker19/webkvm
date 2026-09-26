import { describe, it, expect } from 'vitest';
import {
  purposeList,
  hasPurpose,
  isIsoOnly,
  usableForDisks,
  deployablePools,
  movablePools,
  vmDiskPools,
  containerPools,
  baseImagePools,
  purposeLabel,
  purposeIcons,
  FOLDER_NATURE,
  POOL_SUFFIX,
  backendFor,
  poolForFolder,
  poolsForFolders,
  isBuiltinPool,
} from './purpose.js';

describe('purposeList', () => {
  it('splits a CSV list', () => {
    expect(purposeList('disk,iso,container')).toEqual(['disk', 'iso', 'container']);
  });

  it('accepts a pool object', () => {
    expect(purposeList({ purpose: 'iso' })).toEqual(['iso']);
  });

  it('defaults to disk for empty or unknown values', () => {
    expect(purposeList('')).toEqual(['disk']);
    expect(purposeList({ purpose: '' })).toEqual(['disk']);
    expect(purposeList('bogus')).toEqual(['disk']);
  });

  it('normalizes lxc to container and trims/skips junk', () => {
    expect(purposeList('lxc')).toEqual(['container']);
    expect(purposeList(' disk , iso ')).toEqual(['disk', 'iso']);
    expect(purposeList('disk,bogus,iso')).toEqual(['disk', 'iso']);
  });
});

describe('hasPurpose / isIsoOnly / usableForDisks', () => {
  it('matches by membership on multi-purpose lists', () => {
    const multi = { purpose: 'iso,disk,container' };
    expect(hasPurpose(multi, 'iso')).toBe(true);
    expect(hasPurpose(multi, 'disk')).toBe(true);
    expect(hasPurpose(multi, 'container')).toBe(true);
    expect(isIsoOnly(multi)).toBe(false);
    expect(usableForDisks(multi)).toBe(true);
  });

  it('keeps parity with the old single-purpose equality filters', () => {
    expect(hasPurpose({ purpose: 'iso' }, 'iso')).toBe(true);
    expect(usableForDisks({ purpose: 'iso' })).toBe(false); // old: purpose !== 'iso'
    expect(usableForDisks({ purpose: 'disk' })).toBe(true);
    expect(usableForDisks({ purpose: 'container' })).toBe(true);
    expect(hasPurpose({ purpose: 'disk' }, 'container')).toBe(false);
  });

  it('keeps neutral backup/template pools out of the VM disk targets', () => {
    const backups = { purpose: 'backup' };
    const plantillas = { purpose: 'template' };
    expect(hasPurpose(backups, 'backup')).toBe(true);
    expect(hasPurpose(plantillas, 'template')).toBe(true);
    expect(hasPurpose(backups, 'disk')).toBe(false);
    expect(usableForDisks(backups)).toBe(false);
    expect(usableForDisks(plantillas)).toBe(false);
    expect(isIsoOnly(backups)).toBe(false);
  });
});

describe('purposeLabel / purposeIcons', () => {
  it('uses canonical order regardless of input order', () => {
    expect(purposeLabel('iso,container,disk')).toBe('KVM · LXC · ISO');
    expect(purposeIcons('iso,container,disk')).toEqual(['hardDrive', 'boxes', 'disc']);
  });

  it('labels single purposes', () => {
    expect(purposeLabel('iso')).toBe('ISO');
    expect(purposeLabel('container')).toBe('LXC');
    expect(purposeLabel('disk')).toBe('KVM');
    expect(purposeLabel('backup')).toBe('Backup');
    expect(purposeLabel('template')).toBe('Template');
    expect(purposeIcons('backup')).toEqual(['archive']);
    expect(purposeIcons('template')).toEqual(['package']);
  });
});

// These assertions are a CONTRACT with the Go side: the values are
// duplicated in backend/internal/api/pools_init.go (subfolderNature,
// folderPoolSuffix, subfolderPoolSpec). If the backend renames a
// suffix and this file isn't updated, the dialog's preview would
// promise pool names that never get created — so pin them here.
describe('init-disk pool derivation (mirrors the backend)', () => {
  it('maps every folder to the nature the backend assigns', () => {
    expect(FOLDER_NATURE).toEqual({
      isos: 'iso',
      discos: 'disk',
      disks: 'disk',
      images: 'disk',
      contenedores: 'container',
      containers: 'container',
      backups: 'backup',
      plantillas: 'template',
      templates: 'template',
    });
  });

  it('uses the backend suffixes verbatim', () => {
    expect(POOL_SUFFIX).toEqual({
      disk: '-vdi',
      iso: '-isos',
      container: '-containers',
      backup: '-backups',
      template: '-plantillas',
    });
  });

  it('routes containers to Incus and everything else to libvirt', () => {
    expect(backendFor('container')).toBe('Incus');
    for (const p of ['disk', 'iso', 'backup', 'template']) {
      expect(backendFor(p)).toBe('libvirt');
    }
  });

  it('derives one independent pool per folder', () => {
    expect(poolForFolder('discos', 'mydisk')).toMatchObject({
      name: 'mydisk-vdi',
      purpose: 'disk',
      folder: '/discos',
      backend: 'libvirt',
    });
    expect(poolForFolder('contenedores', 'mydisk')).toMatchObject({
      name: 'mydisk-containers',
      backend: 'Incus',
    });
    expect(poolForFolder('isos', 'mydisk').name).toBe('mydisk-isos');
    expect(poolForFolder('backups', 'mydisk').name).toBe('mydisk-backups');
    expect(poolForFolder('plantillas', 'mydisk').name).toBe('mydisk-plantillas');
  });

  it('returns null for a folder with no storage nature', () => {
    expect(poolForFolder('random', 'mydisk')).toBeNull();
    expect(poolForFolder('', 'mydisk')).toBeNull();
  });

  it('shows a placeholder until the volume name is typed', () => {
    expect(poolForFolder('discos', '').name).toBe('…-vdi');
    expect(poolForFolder('discos', '   ').name).toBe('…-vdi');
    expect(poolForFolder('discos', '', 'mi-disco').name).toBe('mi-disco-vdi');
  });

  it('builds three independent pools, never a unified one', () => {
    const pools = poolsForFolders(['discos', 'contenedores', 'isos'], 'mydisk');
    expect(pools.map((p) => p.name)).toEqual(['mydisk-vdi', 'mydisk-containers', 'mydisk-isos']);
    // Each pool carries exactly one purpose.
    for (const p of pools) {
      expect(purposeList(p.purpose)).toHaveLength(1);
    }
  });

  it('dedupes folders that share a nature', () => {
    const pools = poolsForFolders(['discos', 'disks', 'images', 'contenedores'], 'mydisk');
    expect(pools.map((p) => p.name)).toEqual(['mydisk-vdi', 'mydisk-containers']);
  });

  it('yields nothing when no folder carries a nature', () => {
    expect(poolsForFolders([], 'mydisk')).toEqual([]);
    expect(poolsForFolders(['random'], 'mydisk')).toEqual([]);
    expect(poolsForFolders(undefined, 'mydisk')).toEqual([]);
  });
});

describe('deployablePools', () => {
  const pools = [
    { name: 'webkvm-incus', purpose: 'container' },
    { name: 'Lexar-Contenedores', purpose: 'container' },
    { name: 'webkvm-disks', purpose: 'disk' },
    { name: 'ISOS', purpose: 'iso' },
    { name: 'Backups', purpose: 'backup' },
    { name: 'Seagate', purpose: 'disk', state: 'inactive' },
  ];

  it('offers only container pools to a container deploy', () => {
    expect(deployablePools(pools, 'container').map((p) => p.name)).toEqual([
      'webkvm-incus',
      'Lexar-Contenedores',
    ]);
  });

  it('offers only libvirt disk pools to a VM deploy', () => {
    expect(deployablePools(pools, 'vm').map((p) => p.name)).toEqual(['webkvm-disks']);
  });

  // The two worlds must never bleed into each other: an Incus pool fed
  // to libvirt (or the reverse) fails only once the job is running.
  it('never mixes the two storage worlds', () => {
    const ct = deployablePools(pools, 'container').map((p) => p.name);
    const vm = deployablePools(pools, 'vm').map((p) => p.name);
    expect(ct.filter((n) => vm.includes(n))).toEqual([]);
  });

  it('drops inactive pools', () => {
    expect(deployablePools(pools, 'vm').some((p) => p.name === 'Seagate')).toBe(false);
  });

  it('applies the caller pool ACL, and treats an empty ACL as unrestricted', () => {
    expect(deployablePools(pools, 'container', ['Lexar-Contenedores']).map((p) => p.name)).toEqual([
      'Lexar-Contenedores',
    ]);
    expect(deployablePools(pools, 'container', []).length).toBe(2);
  });

  it('survives a missing pool list', () => {
    expect(deployablePools(undefined, 'container')).toEqual([]);
    expect(deployablePools(null, 'vm')).toEqual([]);
  });
});

describe('deployablePools pool state', () => {
  // Regression: Incus reports healthy pools as "created", not "active".
  // Matching "active" alone hid every container pool on the host.
  it('accepts the Incus "created" state as usable', () => {
    const pools = [{ name: 'webkvm-incus', purpose: 'container', state: 'created' }];
    expect(deployablePools(pools, 'container').map((p) => p.name)).toEqual(['webkvm-incus']);
  });

  it('still rejects the states that mean trouble', () => {
    for (const state of ['pending', 'errored', 'unknown', 'inactive']) {
      const pools = [{ name: 'p', purpose: 'container', state }];
      expect(deployablePools(pools, 'container')).toEqual([]);
    }
  });
});

describe('movablePools', () => {
  const pools = [
    { name: 'webkvm-disks', purpose: 'disk' },
    { name: 'Lexar-Discos', purpose: 'disk' },
    { name: 'Seagate', purpose: 'disk', state: 'inactive' },
    { name: 'ISOS', purpose: 'iso' },
    { name: 'ISOS-2', purpose: 'iso' },
    { name: 'webkvm-incus', purpose: 'container', state: 'created' },
    { name: 'Lexar-Contenedores', purpose: 'container', state: 'created' },
    { name: 'Backups', purpose: 'backup' },
  ];
  const names = (kind, current) => movablePools(pools, kind, current).map((p) => p.name);

  // The rule the whole feature rests on: a move never crosses from one
  // storage world into another. An ISO filed into a disk pool is
  // invisible to the ISO browser, and a disk in an ISO pool is a VM
  // that cannot be found again.
  it('keeps each kind inside its own world', () => {
    expect(names('disk')).toEqual(['webkvm-disks', 'Lexar-Discos']);
    expect(names('iso')).toEqual(['ISOS', 'ISOS-2']);
    expect(names('container')).toEqual(['webkvm-incus', 'Lexar-Contenedores']);
  });

  it('never offers the pool the volume is already in', () => {
    expect(names('disk', 'webkvm-disks')).toEqual(['Lexar-Discos']);
    expect(names('iso', 'ISOS')).toEqual(['ISOS-2']);
  });

  // A backup/template pool is listed in the grid but is not a disk
  // target, exactly as in the deploy selector.
  it('excludes the neutral backup pools from disk moves', () => {
    expect(names('disk')).not.toContain('Backups');
  });

  it('drops pools that are not usable right now', () => {
    expect(names('disk')).not.toContain('Seagate');
  });

  it('survives a missing pool list', () => {
    expect(movablePools(undefined, 'disk')).toEqual([]);
    expect(movablePools(null, 'iso')).toEqual([]);
  });

  // A template pool is a shelf for golden images. It is not a disk
  // target (so it stays out of the normal move list), but it can be
  // asked for by name when the VM really is a template.
  it('offers template pools only when asked for that kind', () => {
    const withTpl = [...pools, { name: 'Plantillas', purpose: 'template' }];
    expect(movablePools(withTpl, 'disk').map((p) => p.name)).not.toContain('Plantillas');
    expect(movablePools(withTpl, 'template').map((p) => p.name)).toEqual(['Plantillas']);
  });
});

// The invariant the whole pool model rests on: the storage worlds are
// never mixed. A KVM disk image belongs in a libvirt disk pool, an
// Incus container in an Incus pool, an ISO in the ISO library — and no
// selector may offer anything else.
//
// This is a regression guard for a real bug: every VM-disk selector
// filtered with usableForDisks, which only strips ISO/backup/template
// pools and let CONTAINER pools through. The create form therefore
// offered an Incus pool for a KVM disk. Worse, VmList's import dialog
// rendered the pool list unfiltered, so the ISO pool was offered too —
// and that one does not fail: libvirt happily wrote a qcow2 into the
// ISO library, next to the install media.
describe('pool worlds never mix', () => {
  const REAL = [
    { name: 'ISOS', purpose: 'iso', state: 'active' },
    { name: 'Seagate', purpose: 'disk', state: 'inactive' },
    { name: 'webkvm-disks', purpose: 'disk', state: 'active' },
    { name: 'Lexar-Discos', purpose: 'disk', state: 'active' },
    { name: 'Lexar-Contenedores', purpose: 'container', state: 'created' },
    { name: 'webkvm-incus', purpose: 'container', state: 'created' },
    { name: 'Backups', purpose: 'backup', state: 'active' },
    { name: 'Plantillas', purpose: 'template', state: 'active' },
  ];
  const names = (l) => l.map((p) => p.name);

  it('never offers a container pool for a KVM disk', () => {
    const got = names(vmDiskPools(REAL));
    expect(got).not.toContain('webkvm-incus');
    expect(got).not.toContain('Lexar-Contenedores');
  });

  it('never offers the ISO library for a KVM disk', () => {
    expect(names(vmDiskPools(REAL))).not.toContain('ISOS');
  });

  it('never offers backup or template shelves for a KVM disk', () => {
    const got = names(vmDiskPools(REAL));
    expect(got).not.toContain('Backups');
    expect(got).not.toContain('Plantillas');
  });

  it('never offers an inactive pool: its directory is not mounted', () => {
    expect(names(vmDiskPools(REAL))).not.toContain('Seagate');
  });

  it('offers exactly the usable disk pools', () => {
    expect(names(vmDiskPools(REAL))).toEqual(['webkvm-disks', 'Lexar-Discos']);
  });

  it('never offers a libvirt pool for an Incus container', () => {
    const got = names(containerPools(REAL));
    expect(got).toEqual(['Lexar-Contenedores', 'webkvm-incus']);
    for (const n of ['ISOS', 'webkvm-disks', 'Lexar-Discos', 'Seagate']) {
      expect(got).not.toContain(n);
    }
  });

  it('keeps the two worlds disjoint', () => {
    const disks = new Set(names(vmDiskPools(REAL)));
    for (const c of names(containerPools(REAL))) expect(disks.has(c)).toBe(false);
  });

  it('honours the per-user pool allowlist', () => {
    expect(names(vmDiskPools(REAL, ['Lexar-Discos']))).toEqual(['Lexar-Discos']);
    expect(names(containerPools(REAL, ['webkvm-incus']))).toEqual(['webkvm-incus']);
  });

  it('survives a missing pool list', () => {
    expect(vmDiskPools(undefined)).toEqual([]);
    expect(containerPools(null)).toEqual([]);
  });
});

// A cached base cloud image is a golden image, not a disk in daily
// use, so it is the one qcow2 a template pool may hold. This mirrors
// baseImagePoolPurpose() in backend/internal/api/image_hub.go: the two
// drifting apart would either hide a valid destination or offer one
// the server refuses.
describe('baseImagePools', () => {
  const REAL = [
    { name: 'ISOS', purpose: 'iso', state: 'active' },
    { name: 'Seagate', purpose: 'disk', state: 'inactive' },
    { name: 'webkvm-disks', purpose: 'disk', state: 'active' },
    { name: 'Lexar-Discos', purpose: 'disk', state: 'active' },
    { name: 'webkvm-incus', purpose: 'container', state: 'created' },
    { name: 'Backups', purpose: 'backup', state: 'active' },
    { name: 'Plantillas', purpose: 'template', state: 'active' },
  ];
  const names = (l) => l.map((p) => p.name);

  it('offers disk AND template pools', () => {
    const got = names(baseImagePools(REAL));
    expect(got).toContain('webkvm-disks');
    expect(got).toContain('Lexar-Discos');
    expect(got).toContain('Plantillas');
  });

  it('never offers an ISO, backup or container pool', () => {
    const got = names(baseImagePools(REAL));
    expect(got).not.toContain('ISOS');
    expect(got).not.toContain('Backups');
    expect(got).not.toContain('webkvm-incus');
  });

  it('drops an inactive pool: its path is an unmounted mount point', () => {
    expect(names(baseImagePools(REAL))).not.toContain('Seagate');
  });

  it('excludes the pool the image already sits in', () => {
    expect(names(baseImagePools(REAL, 'Plantillas'))).not.toContain('Plantillas');
  });

  it('honours the per-user pool allowlist', () => {
    expect(names(baseImagePools(REAL, '', ['Plantillas']))).toEqual(['Plantillas']);
  });

  it('survives a missing pool list', () => {
    expect(baseImagePools(undefined)).toEqual([]);
    expect(baseImagePools(null)).toEqual([]);
  });
});

// The built-in pools are protected in TWO places: this list hides the
// delete button, and isBuiltinPool() in backend/internal/api/storage.go
// refuses the call. The UI list going stale is the dangerous direction —
// it would show a delete button for a pool the server then rejects, or
// worse, for one it accepts because the backend list drifted too.
describe('isBuiltinPool', () => {
  it('protects the pools WebKVM owns on the system disk', () => {
    for (const n of ['webkvm-disks', 'webkvm-isos', 'webkvm-incus']) {
      expect(isBuiltinPool(n)).toBe(true);
    }
  });

  it('still protects the pre-v2.5 ISO pool name', () => {
    // An install whose rename migration has not run yet.
    expect(isBuiltinPool('ISOS')).toBe(true);
  });

  it("leaves the operator's own pools deletable", () => {
    for (const n of ['Lexar-Discos', 'Seagate', 'Lexar-Contenedores', 'default', '']) {
      expect(isBuiltinPool(n)).toBe(false);
    }
  });

  it('does not match on case or partial names', () => {
    for (const n of ['WEBKVM-DISKS', 'webkvm-isos-old', 'my-webkvm-disks']) {
      expect(isBuiltinPool(n)).toBe(false);
    }
  });
});
