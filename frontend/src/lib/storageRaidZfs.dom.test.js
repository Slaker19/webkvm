import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  pools: [],
  disks: [],
  zpools: [],
  zvols: [],
  orphanMounts: [],
  filesystems: [
    { id: 'ext4', label: 'ext4', available: true },
    { id: 'xfs', label: 'XFS', available: true },
    { id: 'btrfs', label: 'Btrfs', available: true },
  ],
  createHostRaid: vi.fn(),
  createHostZpool: vi.fn(),
  createHostZvol: vi.fn(),
  destroyZvol: vi.fn(),
  destroyZfsPool: vi.fn(),
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      listPools: () => Promise.resolve(fx.pools),
      listISOs: () => Promise.resolve([]),
      listHostDisks: () => Promise.resolve(fx.disks),
      listHostZpools: () => Promise.resolve(fx.zpools),
      listHostZVols: () => Promise.resolve(fx.zvols),
      listOrphanMounts: () => Promise.resolve(fx.orphanMounts),
      listFilesystems: () => Promise.resolve(fx.filesystems),
      createHostRaid: (...args) => fx.createHostRaid(...args),
      createHostZpool: (...args) => fx.createHostZpool(...args),
      createHostZvol: (...args) => fx.createHostZvol(...args),
      destroyZvol: (...args) => fx.destroyZvol(...args),
      destroyZfsPool: (...args) => fx.destroyZfsPool(...args),
    },
    {
      get(target, prop) {
        if (prop in target) return target[prop];
        if (typeof prop !== 'string') return undefined;
        return () => Promise.resolve([]);
      },
    }
  );
  return {
    api,
    auth: {
      user: 'admin',
      role: 'admin',
      isLoggedIn: true,
      isAdmin: () => true,
      isOperator: () => true,
    },
  };
});

vi.mock('$lib/router.svelte.js', () => ({ navigate: () => {} }));

import Storage from '../routes/Storage.svelte';

const settle = async () => {
  for (let i = 0; i < 15; i++) {
    await new Promise((r) => setTimeout(r, 20));
    flushSync();
  }
};

describe('Storage: Physical Disks, RAID creation and ZFS management UI', () => {
  let target, app;

  beforeEach(() => {
    vi.clearAllMocks();
    window.alert = vi.fn();
    fx.disks = [
      {
        path: '/dev/sdb',
        name: 'sdb',
        size: 2147483648,
        model: 'QEMU HARDDISK',
        type: 'disk',
        vendor: 'QEMU',
        partitions: [],
        mountpoints: [],
      },
      {
        path: '/dev/sdc',
        name: 'sdc',
        size: 2147483648,
        model: 'QEMU HARDDISK',
        type: 'disk',
        vendor: 'QEMU',
        partitions: [],
        mountpoints: [],
      },
      {
        path: '/dev/sdd',
        name: 'sdd',
        size: 2147483648,
        model: 'QEMU HARDDISK',
        type: 'disk',
        vendor: 'QEMU',
        partitions: [],
        mountpoints: [],
      },
    ];
    fx.zpools = [
      {
        name: 'tank',
        size: '6G',
        allocated: '1.2G',
        free: '4.8G',
        health: 'ONLINE',
        altroot: '-',
      },
    ];
    fx.zvols = [
      {
        name: 'tank/vol1',
        pool: 'tank',
        type: 'volume',
        volsize: '2G',
        used: '500M',
        available: '4.3G',
        device: '/dev/zvol/tank/vol1',
      },
    ];
    fx.createHostRaid.mockResolvedValue({
      device: '/dev/md0',
      level: 'raid1',
      disks: ['/dev/sdb', '/dev/sdc'],
    });
    fx.createHostZpool.mockResolvedValue({
      pool: 'tank2',
      type: 'stripe',
      disks: ['/dev/sdb'],
    });
    fx.createHostZvol.mockResolvedValue({
      name: 'tank/vol2',
      pool: 'tank',
      size: '1G',
      sparse: true,
      device: '/dev/zvol/tank/vol2',
    });
  });

  afterEach(async () => {
    if (app) await unmount(app);
    target?.remove();
    app = null;
  });

  it('renders Physical Disks tab and submits Create RAID modal', async () => {
    target = document.createElement('div');
    document.body.appendChild(target);
    app = mount(Storage, { target });
    await settle();

    // Click on Disks tab
    const disksTab = [...target.querySelectorAll('button')].find((b) =>
      (b.textContent || '').includes('Physical Disks')
    );
    expect(disksTab).toBeTruthy();
    disksTab.click();
    await settle();

    // Open Create RAID modal
    const raidBtn = [...target.querySelectorAll('button')].find((b) =>
      (b.textContent || '').includes('Create RAID (mdadm)')
    );
    expect(raidBtn).toBeTruthy();
    raidBtn.click();
    await settle();

    // Select 2 checkboxes for RAID 1
    const checkboxes = [...document.body.querySelectorAll('input[type="checkbox"]')];
    expect(checkboxes.length).toBeGreaterThanOrEqual(2);
    checkboxes[0].click();
    checkboxes[1].click();
    await settle();

    // Find submit button in dialog footer and click
    const submitBtn = [...document.body.querySelectorAll('button')].find(
      (b) =>
        b.textContent?.includes('Create RAID (mdadm)') &&
        b.closest('[data-dialog-content], [role="dialog"], .fixed')
    );
    expect(submitBtn).toBeTruthy();
    expect(submitBtn.disabled).toBe(false);
    submitBtn.click();
    await settle();

    expect(fx.createHostRaid).toHaveBeenCalledWith({
      level: '1',
      devices: ['/dev/sdb', '/dev/sdc'],
      name: undefined,
    });
  });

  it('renders ZFS tab with pools, zvols, and handles Pool / ZVol creations', async () => {
    target = document.createElement('div');
    document.body.appendChild(target);
    app = mount(Storage, { target });
    await settle();

    // Click on ZFS tab
    const zfsTab = [...target.querySelectorAll('button')].find(
      (b) => (b.textContent || '').trim() === 'ZFS'
    );
    expect(zfsTab).toBeTruthy();
    zfsTab.click();
    await settle();

    // Verify pools and zvols rendered in page
    expect(target.textContent).toContain('tank');
    expect(target.textContent).toContain('ONLINE');
    expect(target.textContent).toContain('tank/vol1');

    // --- Create ZFS Pool Test ---
    const createPoolBtn = [...target.querySelectorAll('button')].find((b) =>
      (b.textContent || '').includes('Create ZFS Pool')
    );
    expect(createPoolBtn).toBeTruthy();
    createPoolBtn.click();
    await settle();

    // Input pool name
    const nameInput = document.body.querySelector('input#zpool-name');
    expect(nameInput).toBeTruthy();
    nameInput.value = 'tank2';
    nameInput.dispatchEvent(new Event('input'));
    await settle();

    // Select 1 disk for stripe
    const poolCheckboxes = [...document.body.querySelectorAll('input[type="checkbox"]')];
    expect(poolCheckboxes.length).toBeGreaterThanOrEqual(1);
    poolCheckboxes[0].click();
    await settle();

    const submitPoolBtn = [...document.body.querySelectorAll('button')].find(
      (b) =>
        b.textContent?.includes('Create ZFS Pool') &&
        b.closest('[data-dialog-content], [role="dialog"], .fixed')
    );
    expect(submitPoolBtn).toBeTruthy();
    submitPoolBtn.click();
    await settle();

    expect(fx.createHostZpool).toHaveBeenCalledWith({
      name: 'tank2',
      topology: 'stripe',
      devices: ['/dev/sdb'],
    });

    // --- Create ZVol Test ---
    const createZvolBtn = [...target.querySelectorAll('button')].find((b) =>
      (b.textContent || '').includes('Create ZVol')
    );
    expect(createZvolBtn).toBeTruthy();
    createZvolBtn.click();
    await settle();

    const zvolNameInput = document.body.querySelector('input#zvol-name');
    expect(zvolNameInput).toBeTruthy();
    zvolNameInput.value = 'vol2';
    zvolNameInput.dispatchEvent(new Event('input'));
    await settle();

    const submitZvolBtn = [...document.body.querySelectorAll('button')].find(
      (b) =>
        b.textContent?.includes('Create ZVol') &&
        b.closest('[data-dialog-content], [role="dialog"], .fixed')
    );
    expect(submitZvolBtn).toBeTruthy();
    submitZvolBtn.click();
    await settle();

    expect(fx.createHostZvol).toHaveBeenCalledWith({
      pool: 'tank',
      name: 'vol2',
      size_gb: 20,
      sparse: true,
    });
  });
});
