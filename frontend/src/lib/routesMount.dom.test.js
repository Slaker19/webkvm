import { beforeAll, describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const nodeApi = vi.hoisted(() => ({
  testNode: vi.fn(() => Promise.resolve({ ok: true, domains: 2, kvm_available: true })),
  createNode: vi.fn(() => Promise.resolve({ id: 'remote-node' })),
  listNodes: vi.fn(() => Promise.resolve({ nodes: [] })),
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

import { resetClusterModule } from '$lib/stores/clusterModule.svelte.js';
import { resetHaWorkloads } from '$lib/stores/haWorkloads.svelte.js';

// Reinstall the API defaults: a mockResolvedValueOnce queued by one test
// must not be consumed by the next one when the runner shuffles the order.
function installNodeDefaults() {
  Object.values(nodeApi).forEach((fn) => fn.mockReset());
  nodeApi.testNode.mockResolvedValue({ ok: true, domains: 2, kvm_available: true });
  nodeApi.createNode.mockResolvedValue({ id: 'remote-node' });
  nodeApi.listNodes.mockResolvedValue({ nodes: [] });
  nodeApi.getNodeIncusInventory.mockResolvedValue({});
  nodeApi.getHAReadiness.mockResolvedValue({ state: 'blocked', blockers: [] });
  nodeApi.getHAModule.mockResolvedValue({
    enabled: true,
    busy: false,
    status: { state: 'installed', steps: [], missing: [], log: [] },
  });
  nodeApi.getIncusClusterMembers.mockResolvedValue({ clustered: true, members: [] });
  nodeApi.getNodeDependencyInstaller.mockResolvedValue({ script: '#!/bin/sh\ntrue' });
  nodeApi.installNodeDependencies.mockResolvedValue({ ok: true, output: 'installed' });
}

const settle = async () => {
  for (let i = 0; i < 4; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

describe('Frontend: all 22 routes mount without unhandled errors', () => {
  let target, app;

  beforeEach(() => {
    installNodeDefaults();
    resetClusterModule();
    resetHaWorkloads();
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

  // Warm the module graph ONCE with a generous hook timeout: the first route
  // pays the cold transform/import cost of the whole app, which under a loaded
  // machine (25 DOM files in parallel) exceeded the 30 s per-test budget. The
  // per-test timeout then measures the mount itself.
  beforeAll(async () => {
    await Promise.all(routes.map((r) => r.loader()));
  }, 180000);

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

  it('tests libvirt before allowing a node to be saved', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    nodeApi.testNode.mockClear();
    nodeApi.createNode.mockClear();
    nodeApi.installNodeDependencies.mockClear();
    nodeApi.getNodeDependencyInstaller.mockClear();
    const mod = await import('../routes/Nodes.svelte');
    app = mount(mod.default, { target });
    await settle();

    const button = (label) =>
      [...target.querySelectorAll('button')].find((item) => item.textContent.trim() === label);
    button('Add node').click();
    await settle();

    const name = target.querySelector('#node-add-name');
    name.value = 'test-node';
    name.dispatchEvent(new Event('input', { bubbles: true }));
    const setInput = (id, value) => {
      const input = target.querySelector(id);
      input.value = value;
      input.dispatchEvent(new Event('input', { bubbles: true }));
    };
    setInput('#node-add-host', 'host.example');
    setInput('#node-add-user', 'test');
    await settle();
    expect(target.textContent).toContain('qemu+ssh://test@host.example/system');

    button('Advanced').click();
    await settle();
    setInput('#node-add-uri', 'qemu+ssh://test@host.example/system');
    button('Easy').click();
    await settle();

    const save = button('Save');
    expect(save.disabled).toBe(true);
    expect(nodeApi.createNode).not.toHaveBeenCalled();
    const originalCreateObjectURL = URL.createObjectURL;
    const originalRevokeObjectURL = URL.revokeObjectURL;
    const originalAnchorClick = HTMLAnchorElement.prototype.click;
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: () => 'blob:installer',
    });
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: () => {} });
    HTMLAnchorElement.prototype.click = () => {};
    try {
      button('Download script').click();
      await settle();
      expect(nodeApi.getNodeDependencyInstaller).toHaveBeenCalledOnce();
    } finally {
      Object.defineProperty(URL, 'createObjectURL', {
        configurable: true,
        value: originalCreateObjectURL,
      });
      Object.defineProperty(URL, 'revokeObjectURL', {
        configurable: true,
        value: originalRevokeObjectURL,
      });
      HTMLAnchorElement.prototype.click = originalAnchorClick;
    }

    nodeApi.testNode.mockRejectedValueOnce(new Error('libvirt unavailable'));
    button('Test connection').click();
    await settle();
    expect(save.disabled).toBe(true);
    expect(target.textContent).toContain('libvirt unavailable');

    button('Test connection').click();
    await settle();
    expect(nodeApi.testNode).toHaveBeenCalledWith(
      'qemu+ssh://test@host.example/system',
      'libvirt',
      '',
      ''
    );
    expect(save.disabled).toBe(false);
    expect(target.textContent).toContain('Found 2 domains');

    button('Install over SSH').click();
    await settle();
    const installConfirm = [...document.body.querySelectorAll('button')]
      .filter((item) => item.textContent.trim() === 'Install over SSH')
      .at(-1);
    installConfirm.click();
    await settle();
    expect(nodeApi.installNodeDependencies).toHaveBeenCalledWith(
      'qemu+ssh://test@host.example/system',
      false
    );

    save.click();
    await settle();
    expect(nodeApi.createNode).toHaveBeenCalledWith(
      'test-node',
      'qemu+ssh://test@host.example/system',
      'libvirt',
      '',
      '',
      ''
    );
  }, 30000);

  it('enrolls a remote Incus project over HTTPS and displays its pinned certificate', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    nodeApi.listNodes.mockResolvedValue({ nodes: [] });
    nodeApi.testNode.mockResolvedValue({
      ok: true,
      backend: 'incus',
      server: { name: 'incus-west', version: '6.0' },
      server_fingerprint: 'SHA256:server-fingerprint',
    });
    nodeApi.createNode.mockResolvedValue({ id: 'incus-node' });

    const mod = await import('../routes/Nodes.svelte');
    app = mount(mod.default, { target });
    await settle();
    const button = (label) =>
      [...target.querySelectorAll('button')].find((item) => item.textContent.trim() === label);
    const setInput = (id, value) => {
      const input = target.querySelector(id);
      input.value = value;
      input.dispatchEvent(new Event('input', { bubbles: true }));
    };

    button('Add node').click();
    await settle();
    button('Incus').click();
    await settle();
    button('Easy').click();
    await settle();
    setInput('#node-add-name', 'incus-west');
    setInput('#node-add-host', 'incus.example');
    setInput('#node-add-port', '8443');
    setInput('#node-incus-project', 'containers');
    setInput('#node-incus-token', 'one-time-token');

    expect(target.querySelector('#node-add-host').value).toBe('incus.example');
    expect(target.querySelector('#node-add-port').value).toBe('8443');
    expect(target.querySelector('#node-incus-project').value).toBe('containers');
    expect(target.querySelector('#node-incus-token').value).toBe('one-time-token');
    button('Test connection').click();
    await settle();
    expect(nodeApi.testNode).toHaveBeenCalledWith(
      'https://incus.example:8443',
      'incus',
      'containers',
      'one-time-token'
    );
    expect(target.textContent).toContain('incus-west');
    expect(target.textContent).toContain('SHA256:server-fingerprint');

    // Saving is blocked until the operator confirms the fingerprint.
    expect(button('Save').disabled).toBe(true);
    const verified = target.querySelector('[data-testid=fingerprint-verified]');
    verified.checked = true;
    verified.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    button('Save').click();
    await settle();
    expect(nodeApi.createNode).toHaveBeenCalledWith(
      'incus-west',
      'https://incus.example:8443',
      'incus',
      'containers',
      'one-time-token',
      ''
    );
  }, 30000);

  it('loads the selected Incus node inventory on demand', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    nodeApi.listNodes.mockResolvedValue({
      nodes: [
        {
          id: 'incus-node',
          name: 'incus-west',
          uri: 'https://incus.example:8443',
          backend: 'incus',
          project: 'containers',
          type: 'remote',
          enabled: true,
          status: 'online',
        },
      ],
    });
    nodeApi.getNodeIncusInventory.mockResolvedValue({
      project: 'containers',
      host_arch: 'x86_64',
      pools: [{ name: 'fast', type: 'dir' }],
      networks: [{ name: 'incusbr0' }],
      profiles: ['default', 'web'],
      images: [{ ref: 'images:alpine/3.20', label: 'Alpine 3.20' }],
    });

    const mod = await import('../routes/Nodes.svelte');
    app = mount(mod.default, { target });
    await settle();
    const inventoryButton = [...target.querySelectorAll('button')].find((item) =>
      item.textContent.includes('View Incus inventory')
    );
    expect(inventoryButton).toBeTruthy();
    inventoryButton.click();
    await settle();
    expect(nodeApi.getNodeIncusInventory).toHaveBeenCalledWith('incus-node');
    expect(target.textContent).toContain('fast (dir)');
    expect(target.textContent).toContain('incusbr0');
    expect(target.textContent).toContain('Alpine 3.20');
  }, 30000);

  it('hides the HA section when the optional module is disabled', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    nodeApi.getHAModule.mockResolvedValueOnce({
      enabled: false,
      busy: false,
      status: { state: 'not_installed', steps: [], missing: [], log: [] },
    });

    const mod = await import('../routes/Nodes.svelte');
    app = mount(mod.default, { target });
    await settle();
    const button = (label) =>
      [...target.querySelectorAll('button')].find((item) => item.textContent.trim() === label);
    expect(button('Check HA readiness')).toBeUndefined();
    expect(target.textContent).toContain('Cluster & HA dependencies');
    expect(target.textContent).toContain('Install cluster/HA dependencies');
    expect(nodeApi.getHAReadiness).not.toHaveBeenCalled();
  }, 30000);

  it('shows read-only HA blockers and advanced cluster status', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    nodeApi.getHAReadiness.mockResolvedValue({
      state: 'blocked',
      can_enable: false,
      blockers: ['pacemaker_fencing_unconfigured', 'shared_storage_not_verified'],
      pacemaker: {
        state: 'observed',
        cluster_name: 'lab-cluster',
        quorum: true,
        nodes_configured: 3,
        nodes_online: 2,
        fencing_enabled: false,
        fence_devices: 0,
      },
      control_plane: {
        shared_data_dir_verified: false,
        virtual_ip_configured: false,
      },
      incus_targets: [],
    });

    const mod = await import('../routes/Nodes.svelte');
    app = mount(mod.default, { target });
    await settle();
    const button = (label) =>
      [...target.querySelectorAll('button')].find((item) => item.textContent.trim() === label);
    button('Check HA readiness').click();
    await settle();
    expect(nodeApi.getHAReadiness).toHaveBeenCalledOnce();
    expect(target.textContent).toContain('Automatic failover is not active');
    expect(target.textContent).toContain('Enable STONITH and configure at least one fence device');
    expect(target.textContent).toContain('Shared storage for the workloads has not been verified');

    button('Advanced').click();
    await settle();
    expect(target.textContent).toContain('lab-cluster');
    expect(target.textContent).toContain('Configured fence devices');
    expect(target.textContent).toContain('Shared DATA_DIR');
  }, 30000);

  it('shows HA active without blockers when readiness is ready', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    nodeApi.getHAReadiness.mockResolvedValue({
      state: 'ready',
      can_enable: true,
      blockers: [],
      pacemaker: {
        state: 'observed',
        cluster_name: 'webkvm-ha',
        quorum: true,
        nodes_configured: 3,
        nodes_online: 3,
        fencing_enabled: true,
        watchdog_fencing: true,
        fencing_tested: true,
        managed_ip: true,
        fence_devices: 0,
        resources: [
          {
            id: 'kvm-vm',
            agent: 'ocf:heartbeat:VirtualDomain',
            role: 'Started',
            active: true,
            node: 'node-c',
          },
        ],
      },
      control_plane: {
        state: 'active',
        shared_data_dir_verified: true,
        virtual_ip_configured: true,
        fencing_tested: true,
        shared_storage_verified: true,
      },
      prerequisites: [],
      incus_targets: [],
    });

    const mod = await import('../routes/Nodes.svelte');
    app = mount(mod.default, { target });
    await settle();
    const button = (label) =>
      [...target.querySelectorAll('button')].find((item) => item.textContent.trim() === label);
    button('Check HA readiness').click();
    await settle();
    expect(target.textContent).toContain('HA is active');
    expect(target.textContent).toContain('No blockers were reported.');
    expect(target.textContent).not.toContain('Automatic failover is not active');

    button('Advanced').click();
    await settle();
    expect(target.textContent).toContain('Watchdog fencing (SBD)');
    expect(target.textContent).toContain('Pacemaker resources');
    expect(target.textContent).toContain('kvm-vm');
    expect(target.textContent).toContain('Verified');
    expect(target.textContent).toContain('Configured');
  }, 30000);
});
