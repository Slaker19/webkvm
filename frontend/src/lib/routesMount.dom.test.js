import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      me: () => Promise.resolve({ username: 'admin', role: 'admin' }),
      listVMs: () => Promise.resolve([]),
      listGroups: () => Promise.resolve({ groups: [] }),
      listPools: () => Promise.resolve([]),
      listNetworks: () => Promise.resolve([]),
      listISOs: () => Promise.resolve([]),
      listContainerImages: () => Promise.resolve([]),
      listBaseCloudImages: () => Promise.resolve([]),
      listCloudInitSnippets: () => Promise.resolve([]),
      listTemplates: () => Promise.resolve([]),
      listAppliances: () => Promise.resolve([]),
      listHelperScripts: () => Promise.resolve([]),
      listMedia: () => Promise.resolve([]),
      listUsers: () => Promise.resolve([]),
      listNodes: () => Promise.resolve({ nodes: [] }),
      listSnapshots: () => Promise.resolve([]),
      listBackupTargets: () => Promise.resolve([]),
      listBackupSchedules: () => Promise.resolve([]),
      listBackupJobs: () => Promise.resolve([]),
      listHostDisks: () => Promise.resolve([]),
      listHostZpools: () => Promise.resolve([]),
      listHostZVols: () => Promise.resolve([]),
      listHostRaid: () => Promise.resolve([]),
      listHostInterfaces: () => Promise.resolve([]),
      listHostUSBDevices: () => Promise.resolve([]),
      listHostPCIDevices: () => Promise.resolve([]),
      getHostPCIPreflight: () => Promise.resolve({ supported: true }),
      getHostUpdates: () => Promise.resolve({ updates: [] }),
      getHostInfo: () => Promise.resolve({ pools_dir: '/opt/webkvm/pools' }),
      getHostStats: () => Promise.resolve({}),
      getSystemStatus: () =>
        Promise.resolve({
          backend: { version: '0.1.8' },
          host: { hostname: 'test', os: 'Linux' },
          libvirt: { connected: true, uri: 'qemu:///system' },
          disk: { used_bytes: 100, total_bytes: 1000, used_pct: 10 },
          load: { load1: 0.1, load5: 0.1, load15: 0.1 },
          services: [],
          platform: { kernel: '6.8', nested_virt: {}, iommu: {} },
        }),
      getStorageBreakdown: () => Promise.resolve([]),
      listFilesystems: () => Promise.resolve([]),
      listOrphanMounts: () => Promise.resolve([]),
      getCapabilities: () => Promise.resolve({}),
      getAuditLogs: () => Promise.resolve({ logs: [] }),
      getBranding: () => Promise.resolve({}),
      getVMFirewall: () => Promise.resolve({ rules: [], forwards: [] }),
      getHostFirewall: () => Promise.resolve({ rules: [], forwards: [] }),
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
      token: 'mock-jwt-token',
      isAdmin: () => true,
      isOperator: () => true,
      isViewer: () => false,
      canMutate: () => true,
      checkAuth: () => Promise.resolve(true),
    },
  };
});

vi.mock('$lib/router.svelte.js', () => ({
  navigate: vi.fn(),
  getRoute: () => ({ name: 'test', path: '/', params: { id: 'vm-1' }, query: {} }),
}));

const settle = async () => {
  for (let i = 0; i < 4; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

describe('Frontend: all 22 routes mount without unhandled errors', () => {
  let target, app;

  beforeEach(() => {
    target = document.createElement('div');
    document.body.appendChild(target);
  });

  afterEach(async () => {
    if (app) await unmount(app);
    target?.remove();
    app = null;
  });

  const routes = [
    { name: 'Login', loader: () => import('../routes/Login.svelte') },
    { name: 'NotFound', loader: () => import('../routes/NotFound.svelte') },
    { name: 'AccessDenied', loader: () => import('../routes/AccessDenied.svelte') },
    { name: 'VmList', loader: () => import('../routes/VmList.svelte') },
    { name: 'VmCreate', loader: () => import('../routes/VmCreate.svelte') },
    { name: 'AppStore', loader: () => import('../routes/AppStore.svelte') },
    { name: 'ImageHub', loader: () => import('../routes/ImageHub.svelte') },
    { name: 'MultiView', loader: () => import('../routes/MultiView.svelte') },
    { name: 'Storage', loader: () => import('../routes/Storage.svelte') },
    { name: 'Media', loader: () => import('../routes/Media.svelte') },
    { name: 'Networks', loader: () => import('../routes/Networks.svelte') },
    { name: 'Users', loader: () => import('../routes/Users.svelte') },
    { name: 'Nodes', loader: () => import('../routes/Nodes.svelte') },
    { name: 'Snapshots', loader: () => import('../routes/Snapshots.svelte') },
    { name: 'Backup', loader: () => import('../routes/Backup.svelte') },
    { name: 'Firewall', loader: () => import('../routes/Firewall.svelte') },
    { name: 'AuditLog', loader: () => import('../routes/AuditLog.svelte') },
    { name: 'Status', loader: () => import('../routes/Status.svelte') },
    { name: 'Settings', loader: () => import('../routes/Settings.svelte') },
    { name: 'Account', loader: () => import('../routes/Account.svelte') },
    { name: 'HostConsole', loader: () => import('../routes/HostConsole.svelte') },
  ];

  for (const r of routes) {
    it(`mounts route: ${r.name}`, async () => {
      const mod = await r.loader();
      const Comp = mod.default;
      expect(Comp).toBeTruthy();
      app = mount(Comp, { target });
      await settle();
      expect(target.innerHTML).toBeTruthy();
      await unmount(app);
      app = null;
    }, 30000);
  }
});
