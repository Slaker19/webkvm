import { beforeAll, describe, it, expect, vi } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const nodeApi = vi.hoisted(() => ({
  testNode: vi.fn(() => Promise.resolve({ ok: true, domains: 2, kvm_available: true })),
  createNode: vi.fn(() => Promise.resolve({ id: 'remote-node' })),
  listNodes: vi.fn(() =>
    Promise.resolve({
      nodes: [
        { id: 'local', name: 'here', type: 'local', enabled: true, status: 'online' },
        {
          id: 'n1',
          name: 'east',
          type: 'remote',
          backend: 'libvirt',
          uri: 'qemu+ssh://root@east/system',
          enabled: true,
          status: 'online',
          access: 'ok',
        },
      ],
    })
  ),
  getNodeIncusInventory: vi.fn(() => Promise.resolve({})),
  getHAReadiness: vi.fn(() => Promise.resolve({ state: 'blocked', blockers: [] })),
  getHAModule: vi.fn(() =>
    Promise.resolve({
      enabled: true,
      busy: false,
      status: { state: 'installed', steps: [], missing: [], log: [] },
    })
  ),
  getIncusClusterMembers: vi.fn(() => Promise.resolve({ clustered: true, members: [] })),
  getNodeDependencyInstaller: vi.fn(() => Promise.resolve({ script: '#!/bin/sh\ntrue' })),
  installNodeDependencies: vi.fn(() => Promise.resolve({ ok: true, output: 'installed' })),
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      me: () => Promise.resolve({ username: 'admin', role: 'admin' }),
      listVMs: () =>
        Promise.resolve([
          { id: 'vm-1', name: 'vm-1', state: 'running', node: 'local', vcpus: 1, memory: 1024 },
          { id: 'vm-2', name: 'vm-2', state: 'shutoff', node: 'east', vcpus: 1, memory: 512 },
        ]),
      getVM: () =>
        Promise.resolve({ id: 'vm-1', name: 'vm-1', state: 'running', vcpus: 1, memory: 1024 }),
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
      listNodes: nodeApi.listNodes,
      getNodeIncusInventory: nodeApi.getNodeIncusInventory,
      getHAReadiness: nodeApi.getHAReadiness,
      getHAModule: nodeApi.getHAModule,
      getIncusClusterMembers: nodeApi.getIncusClusterMembers,
      testNode: nodeApi.testNode,
      createNode: nodeApi.createNode,
      getNodeDependencyInstaller: nodeApi.getNodeDependencyInstaller,
      installNodeDependencies: nodeApi.installNodeDependencies,
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
          backend: { version: '0.1.9' },
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
      getClusterSSH: () =>
        Promise.resolve({
          configured: true,
          nodes: [{ id: 'n1', name: 'east', host_key: { state: 'pinned' }, access: 'ok' }],
        }),
      getClusterSummary: () => Promise.resolve({ total_nodes: 2, online_nodes: 2 }),
      listHAWorkloads: () =>
        Promise.resolve({ workloads: [], preconditions: { ok: true, blockers: [] } }),
      getHostFirewall: () => Promise.resolve({ rules: [], forwards: [] }),
    },
    {
      // Every request answers late (after the route is left) so the async
      // continuation of each load runs on a destroyed component.
      get(target, prop) {
        if (typeof prop !== 'string') return undefined;
        const fn = prop in target ? target[prop] : () => Promise.resolve([]);
        if (typeof fn !== 'function') return fn;
        return (...args) =>
          new Promise((r) => setTimeout(r, globalThis.__apiDelay || 0)).then(() => fn(...args));
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

// 8.26 defect 7: Svelte "derived_inert" (a derived read after its owner was
// destroyed) on leaving Nodes, the VM list, a VM detail and the create
// wizard. Leaving a route must not log any Svelte warning.
describe('leaving a route logs no Svelte warning', () => {
  const routes = [
    { name: 'Nodes', loader: () => import('../routes/Nodes.svelte') },
    { name: 'VmList', loader: () => import('../routes/VmList.svelte') },
    {
      name: 'VmDetail',
      loader: () => import('../routes/VmDetail.svelte'),
      props: { vmId: 'vm-1' },
    },
    { name: 'VmCreate', loader: () => import('../routes/VmCreate.svelte') },
  ];
  beforeAll(async () => {
    await Promise.all(routes.map((r) => r.loader()));
  }, 180000);

  for (const r of routes) {
    it(`unmounts ${r.name} without console warnings`, async () => {
      const warn = vi.spyOn(console, 'warn');
      const target = document.createElement('div');
      document.body.appendChild(target);
      try {
        const Comp = (await r.loader()).default;
        // Leave both before and after the first data arrives.
        for (const delay of [5, 15, 30, 0]) {
          globalThis.__apiDelay = delay;
          const app = mount(Comp, { target, props: r.props || {} });
          await settle();
          await unmount(app);
          await new Promise((res) => setTimeout(res, 60));
          await settle();
        }
        globalThis.__apiDelay = 0;
        // Let in-flight requests, timers and outros finish after the unmount.
        await new Promise((res) => setTimeout(res, 50));
        await settle();
        const svelteWarnings = warn.mock.calls
          .map((c) => c.map(String).join(' '))
          .filter((m) => m.includes('svelte'));
        expect(svelteWarnings).toEqual([]);
      } finally {
        warn.mockRestore();
        target.remove();
      }
    }, 30000);
  }
});
