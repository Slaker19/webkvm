// 8.29 #3 / 8.30: after creating a VM the list said "0 machines" for seconds
// while the federated GET /api/vms waited on remote nodes. The list now shows
// this server's VMs at once (?scope=local), says it is still loading the
// other nodes, merges the full list when it arrives, and shows a VM that was
// just created even before any listing includes it.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({ local: [], full: [], releaseFull: null, calls: [] }));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      listVMs: (opts) => {
        fx.calls.push(opts?.scope || 'all');
        if (opts?.scope === 'local') return Promise.resolve(fx.local);
        return new Promise((resolve) => {
          fx.releaseFull = () => resolve(fx.full);
        });
      },
      listNodes: () => Promise.resolve({ nodes: [] }),
      getClusterSSH: () => Promise.resolve({ configured: true, nodes: [] }),
      getClusterSummary: () => Promise.resolve(null),
      listGroups: () => Promise.resolve({ groups: [] }),
      getHAModule: () =>
        Promise.resolve({ enabled: false, busy: false, status: { state: 'installed', steps: [] } }),
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
import { addPendingVm, clearPendingVms } from './utils/pendingVms.js';

const settle = async () => {
  for (let i = 0; i < 10; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};
const base = { type: 'vm', state: 'running', vcpus: 1, ram_mb: 512, disk_gb: 5, hypervisor: 'kvm' };
let target, app;
async function render() {
  target = document.createElement('div');
  document.body.appendChild(target);
  app = mount(VmList, { target });
  await settle();
}
const header = () => target.querySelector('h1')?.parentElement?.textContent || '';

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  clearPendingVms();
  fx.calls = [];
  fx.local = [{ ...base, id: 'a', name: 'alpha', node: 'host' }];
  fx.full = [
    { ...base, id: 'a', name: 'alpha', node: 'host' },
    { ...base, id: 'b', name: 'beta', node: 'kvm-b', node_id: 'n1' },
  ];
});
afterEach(async () => {
  if (app) await unmount(app);
  app = null;
  target?.remove();
  document.body.innerHTML = '';
});

describe('VmList: local first, then the other nodes', () => {
  it('shows local VMs at once with a "loading other nodes" note, then merges', async () => {
    await render();
    expect(fx.calls).toContain('local');
    expect(target.textContent).toContain('alpha');
    expect(target.textContent).not.toContain('beta');
    expect(target.querySelector('[data-testid=vms-remote-loading]')).not.toBeNull();
    fx.releaseFull();
    await settle();
    expect(target.textContent).toContain('beta');
    expect(target.querySelector('[data-testid=vms-remote-loading]')).toBeNull();
  });

  it('never says "0 machines" while nothing has arrived yet', async () => {
    fx.local = [];
    await render();
    expect(header()).not.toMatch(/\b0 machines\b/);
    fx.releaseFull();
    await settle();
  });

  it('shows a just-created VM until a listing includes it, never twice', async () => {
    addPendingVm({
      id: 'new1',
      name: 'fresh-vm',
      node: 'kvm-b',
      node_id: 'n1',
      vcpus: 1,
      ram_mb: 512,
    });
    await render();
    expect(target.textContent).toContain('fresh-vm');
    fx.full = [...fx.full, { ...base, id: 'new1', name: 'fresh-vm', node: 'kvm-b', node_id: 'n1' }];
    fx.releaseFull();
    await settle();
    // Same card shape as "beta" (also on kvm-b): same number of mentions.
    const count = (s) => target.textContent.split(s).length - 1;
    expect(count('fresh-vm')).toBeGreaterThan(0);
    expect(count('fresh-vm')).toBe(count('beta'));
  });
});
