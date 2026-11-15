import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  vms: [],
  nodes: [],
  members: [],
  ssh: { configured: true, nodes: [] },
  managedCalls: [],
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      listVMs: () => Promise.resolve(fx.vms),
      listNodes: () => Promise.resolve({ nodes: fx.nodes }),
      getClusterSSH: () => Promise.resolve(fx.ssh),
      getIncusClusterMembers: () => Promise.resolve({ clustered: true, members: fx.members }),
      getClusterSummary: () => Promise.resolve(null),
      listGroups: () => Promise.resolve({ groups: [] }),
      setVMManaged: (...a) => {
        fx.managedCalls.push(a);
        return Promise.resolve({ managed: a[3] });
      },
      getHAModule: () =>
        Promise.resolve({ enabled: false, busy: false, status: { state: 'installed', steps: [] } }),
      listHAWorkloads: () =>
        Promise.resolve({ workloads: [], preconditions: { ok: true, blockers: [] } }),
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
      isAdmin: () => true,
      isOperator: () => true,
      isViewer: () => false,
      canMutate: () => true,
    },
  };
});
vi.mock('$lib/router.svelte.js', () => ({
  navigate: vi.fn(),
  getRoute: () => ({ name: 'vms', path: '/vms', params: {}, query: {} }),
}));

import VmList from '../routes/VmList.svelte';
import Nodes from '../routes/Nodes.svelte';
import { clusterModule } from './stores/clusterModule.svelte.js';

const settle = async () => {
  for (let i = 0; i < 10; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

let mounted = null;
async function render(Comp) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  mounted = { app: mount(Comp, { target }), target };
  await settle();
  return target;
}

const base = { type: 'vm', state: 'running', vcpus: 1, ram_mb: 512, disk_gb: 5 };
const nodes = [
  { id: 'loc', name: 'host', type: 'local', enabled: true, status: 'online', backend: 'libvirt' },
  { id: 'n1', name: 'kvm-b', type: 'remote', enabled: true, status: 'offline', backend: 'libvirt' },
];

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  Object.assign(fx, { vms: [], nodes, members: [], ssh: { configured: true, nodes: [] } });
  fx.managedCalls = [];
  clusterModule.loaded = false;
  clusterModule.enabled = false;
});
afterEach(() => {
  if (mounted) unmount(mounted.app);
  mounted?.target.remove();
  mounted = null;
  document.body.innerHTML = '';
});

describe('VmList multi-node', () => {
  beforeEach(() => {
    fx.vms = [
      { ...base, id: 'a', name: 'alpha', node: 'host', hypervisor: 'kvm' },
      {
        ...base,
        id: 'b',
        name: 'beta',
        node: 'kvm-b',
        node_id: 'n1',
        hypervisor: 'kvm',
        stale: true,
        managed: false,
      },
      {
        ...base,
        id: 'c',
        name: 'gamma',
        node: 'm2',
        hypervisor: 'incus',
        type: 'container',
        member_online: false,
      },
    ];
    fx.ssh = {
      configured: true,
      nodes: [{ id: 'n1', host_key: { state: 'changed' }, last_test: { ok: false } }],
    };
  });

  it('shows the banner with the translated code and per-node group headers', async () => {
    const root = await render(VmList);
    const banner = root.querySelector('[data-testid=node-health-banner]');
    expect(banner).not.toBeNull();
    expect(banner.textContent).toContain('kvm-b');
    expect(banner.textContent).toContain('identity of the node changed');
    expect(banner.textContent).toContain('Fix in Cluster access');
    const headers = [...root.querySelectorAll('[data-testid=node-group-header]')];
    expect(headers.length).toBeGreaterThanOrEqual(2);
  });

  // Every code must reach the banner with ITS text (never the generic
  // "node does not answer") and, for access problems, the Cluster access link.
  it.each([
    ['ssh_auth_denied', 'refused WebKVM', true],
    ['host_key_unpinned', 'not confirmed yet', true],
    ['host_key_changed', 'identity of the node changed', true],
    ['no_key', 'no SSH key yet', true],
    ['transport_unsupported', 'cannot use SSH', true],
    ['node_unreachable', 'does not answer', false],
    ['node_timeout', 'took too long', false],
  ])('banner for %s names the exact cause', async (code, text, access) => {
    fx.ssh = {
      configured: true,
      nodes: [{ id: 'n1', host_key: { state: 'pinned' }, last_test: { ok: false, code } }],
    };
    const root = await render(VmList);
    const li = root.querySelector('[data-testid=node-issue-n1]');
    expect(li.textContent).toContain(text);
    if (code !== 'node_unreachable') expect(li.textContent).not.toContain('does not answer');
    const fix = [...root.querySelectorAll('[data-testid=node-health-banner] button')].find((b) =>
      b.textContent.includes('Fix in Cluster access')
    );
    expect(!!fix).toBe(access);
  });

  it('uses the pool error of the overview when no test was run (after a restart)', async () => {
    fx.ssh = {
      configured: true,
      nodes: [{ id: 'n1', host_key: { state: 'pinned' }, pool_error: 'ssh_auth_denied' }],
    };
    const root = await render(VmList);
    const li = root.querySelector('[data-testid=node-issue-n1]');
    expect(li.textContent).toContain('refused WebKVM');
    expect(li.textContent).not.toContain('does not answer');
  });

  it('flags stale data, offline Incus members and unmanaged VMs', async () => {
    const root = await render(VmList);
    expect(root.querySelector('[data-testid=vm-stale-badge]')).not.toBeNull();
    expect(root.querySelector('[data-testid=vm-member-offline]')).not.toBeNull();
    const toggles = [...root.querySelectorAll('[data-testid=vm-managed-toggle]')];
    const unmanaged = toggles.find((b) => b.getAttribute('aria-checked') === 'false');
    expect(unmanaged).toBeTruthy();
    unmanaged.click();
    await settle();
    expect(fx.managedCalls[0]).toEqual(['b', 'kvm-b', 'kvm', true]);
  });

  it('hides unmanaged VMs when the filter is turned off', async () => {
    const root = await render(VmList);
    expect(root.textContent).toContain('beta');
    root.querySelector('[data-testid=show-unmanaged]').click();
    await settle();
    expect(root.textContent).not.toContain('beta');
    expect(root.textContent).toContain('alpha');
  });
});

describe('Nodes cluster operations', () => {
  beforeEach(() => {
    fx.members = [
      { name: 'm1', status: 'Online', url: 'https://m1:8443', roles: [] },
      { name: 'm2', status: 'Offline', url: 'https://m2:8443', roles: [] },
    ];
    fx.vms = [{ ...base, id: 'b', name: 'beta', node: 'kvm-b', hypervisor: 'kvm', managed: false }];
  });

  it('shows the overview strip and explains the enable toggle', async () => {
    const root = await render(Nodes);
    const strip = root.querySelector('[data-testid=cluster-overview]');
    expect(strip).not.toBeNull();
    expect(strip.querySelector('[data-testid=overview-unmanaged]').textContent.trim()).toBe('1');
    expect(strip.querySelector('[data-testid=overview-degraded]').textContent.trim()).toBe('1');
    const local = [...root.querySelectorAll('button[aria-pressed]')].find((b) =>
      b.getAttribute('aria-label')?.includes('host')
    );
    expect(local.disabled).toBe(true);
    expect(local.getAttribute('title')).toContain('local node cannot be disabled');
  });

  it('per-member Evacuate/Restore with a reason when offline', async () => {
    const root = await render(Nodes);
    const m1 = root.querySelector('[data-testid=member-ops-m1]');
    const m2 = root.querySelector('[data-testid=member-ops-m2]');
    expect(m1.textContent).toContain('Evacuate');
    const m2Buttons = [...m2.querySelectorAll('button')];
    expect(m2Buttons.every((b) => b.disabled)).toBe(true);
    expect(m2.textContent).toContain('member is offline');
  });
});
