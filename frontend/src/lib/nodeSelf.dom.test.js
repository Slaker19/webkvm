import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

// Self-duplicate registry entry, "reachable, access not set up" and HA
// workloads missing from the VM list (browser walkthrough 8.24, items 1/4/7/10).

const fx = vi.hoisted(() => ({
  vms: [],
  nodes: [],
  ssh: { configured: true, nodes: [] },
  haEnabled: false,
  haCalls: 0,
  workloads: [],
  deleteCalls: [],
  navigate: null,
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      listVMs: () => Promise.resolve(fx.vms),
      listNodes: () => Promise.resolve({ nodes: fx.nodes }),
      getClusterSSH: () => Promise.resolve(fx.ssh),
      getIncusClusterMembers: () => Promise.resolve(null),
      getClusterSummary: () => Promise.resolve(null),
      listGroups: () => Promise.resolve({ groups: [] }),
      getHAModule: () =>
        Promise.resolve({
          enabled: fx.haEnabled,
          busy: false,
          status: { state: 'installed', steps: [] },
        }),
      listHAWorkloads: () => {
        fx.haCalls++;
        return Promise.resolve({
          workloads: fx.workloads,
          preconditions: { ok: true, blockers: [] },
        });
      },
      deleteNode: (id, opts) => {
        fx.deleteCalls.push([id, !!opts?.force]);
        fx.nodes = fx.nodes.filter((n) => n.id !== id);
        return Promise.resolve({});
      },
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
  navigate: (...a) => fx.navigate?.(...a),
  getRoute: () => ({ name: 'vms', path: '/vms', params: {}, query: {} }),
}));

import VmList from '../routes/VmList.svelte';
import Nodes from '../routes/Nodes.svelte';
import { clusterModule } from './stores/clusterModule.svelte.js';
import { resetHaWorkloads } from './stores/haWorkloads.svelte.js';

const settle = async () => {
  for (let i = 0; i < 12; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};
const btn = (root, text) =>
  [...root.querySelectorAll('button')].find((b) => (b.textContent || '').trim().includes(text));

let mounted = null;
async function render(Comp) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  mounted = { app: mount(Comp, { target }), target };
  await settle();
  return target;
}

const base = { type: 'vm', state: 'running', vcpus: 1, ram_mb: 512, disk_gb: 5 };
const local = {
  id: 'local',
  name: 'node-b',
  type: 'local',
  enabled: true,
  status: 'online',
  backend: 'libvirt',
  uri: 'qemu:///system',
};
const dup = {
  id: 'd1',
  name: 'node-b',
  type: 'remote',
  enabled: true,
  status: 'online',
  backend: 'libvirt',
  uri: 'qemu+ssh://operator@10.20.30.11/system',
  is_self: true,
};
const dev = {
  id: 'n2',
  name: 'node-a',
  type: 'remote',
  enabled: true,
  status: 'online',
  backend: 'incus',
  uri: 'https://10.0.0.5:8443',
};
const noAccess = {
  id: 'n3',
  name: 'node-b-ha2-with-a-long-name',
  type: 'remote',
  enabled: true,
  status: 'online',
  latency_ms: 2,
  backend: 'libvirt',
  uri: 'qemu+ssh://root@10.20.30.10/system',
  access: 'not_set_up',
  access_code: 'no_key',
  last_error: 'VM listing failed: no cluster SSH key',
};

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  Object.assign(fx, {
    vms: [],
    nodes: [local, dup, dev, noAccess],
    ssh: { configured: true, nodes: [] },
    haEnabled: false,
    haCalls: 0,
    workloads: [],
    deleteCalls: [],
    navigate: vi.fn(),
  });
  clusterModule.loaded = false;
  clusterModule.enabled = false;
  resetHaWorkloads();
});
afterEach(() => {
  if (mounted) unmount(mounted.app);
  mounted?.target.remove();
  mounted = null;
  document.body.innerHTML = '';
});

describe('Nodes: this server registered twice', () => {
  it('shows this server once, labelled, plus a notice that never deletes by itself', async () => {
    const root = await render(Nodes);
    expect(fx.deleteCalls).toEqual([]);
    expect(root.querySelector('[data-testid="node-card-d1"]')).toBeNull();
    expect(root.querySelector('[data-testid="node-card-local"]').textContent).toContain(
      'this server'
    );
    expect(root.querySelector('[data-testid="install-node-d1"]')).toBeNull();
    const notice = root.querySelector('[data-testid="self-duplicate-notice"]');
    expect(notice.textContent).toContain('This server is registered twice');
    expect(notice.textContent).toContain(
      "'node-b' (qemu+ssh://operator@10.20.30.11/system) points to this same machine"
    );
  });

  it('removes the duplicate only after confirming, through the node delete', async () => {
    const root = await render(Nodes);
    const notice = root.querySelector('[data-testid="self-duplicate-notice"]');
    btn(notice, 'Remove duplicate').click();
    await settle();
    expect(fx.deleteCalls).toEqual([]);
    expect(document.body.textContent).toContain("Remove the duplicate entry 'node-b'?");
    const confirm = [...document.body.querySelectorAll('button')].filter(
      (b) => b.textContent.trim() === 'Remove duplicate' && !notice.contains(b)
    );
    confirm[confirm.length - 1].click();
    await settle();
    expect(fx.deleteCalls).toEqual([['d1', false]]);
    expect(root.querySelector('[data-testid="self-duplicate-notice"]')).toBeNull();
  });
});

describe('Nodes: reachable but access not set up', () => {
  it('never says online, links to Cluster access, counts consistently', async () => {
    const root = await render(Nodes);
    const card = root.querySelector('[data-testid="node-card-n3"]');
    const badge = card.querySelector('[data-testid="node-status-n3"]');
    expect(badge.textContent).toContain('Reachable, access not set up');
    expect(badge.textContent).not.toContain('2 ms');
    expect(badge.className).toContain('text-info');
    expect(btn(card, 'Set up access')).toBeTruthy();
    // last_error from the federation is not shown as an offline error.
    expect(card.textContent).not.toContain('Offline');
    // Summary and overview strip: self duplicate excluded, no_access apart.
    const strip = root.querySelector('[data-testid=cluster-overview]');
    expect(strip.querySelector('[data-testid=overview-online]').textContent.trim()).toBe('2');
    expect(strip.querySelector('[data-testid=overview-no_access]').textContent.trim()).toBe('1');
    expect(strip.querySelector('[data-testid=overview-enabled]').textContent.trim()).toBe('3');
    expect(strip.querySelector('[data-testid=overview-degraded]').textContent.trim()).toBe('0');
    const online = root.querySelector('[data-testid=summary-online]');
    expect(online.textContent).toContain('2');
    expect(online.textContent).toContain('1 reachable, access not set up');
    expect(online.textContent).not.toContain('offline');
  });

  it('shows the full node name as a tooltip on a truncated name', async () => {
    const root = await render(Nodes);
    const name = root.querySelector('[data-testid="node-name-n3"]');
    expect(name.getAttribute('title')).toBe('node-b-ha2-with-a-long-name');
    expect(name.className).toContain('truncate');
  });
});

describe('VmList: this server once and HA workloads that are not listed', () => {
  beforeEach(() => {
    fx.vms = [
      { ...base, id: 'a', name: 'ha-test', node: 'node-b', hypervisor: 'kvm' },
      {
        ...base,
        id: 'b',
        name: 'n8n',
        node: 'node-a',
        node_id: 'n2',
        hypervisor: 'incus',
        type: 'container',
      },
    ];
  });

  it('one chip per server with the right counts and an accessible group label', async () => {
    const root = await render(VmList);
    const group = root.querySelector('[role="group"][aria-label="Filter by node"]');
    expect(group).not.toBeNull();
    const chips = [...group.querySelectorAll('button')].map((b) =>
      b.textContent.replace(/\s+/g, ' ').trim()
    );
    const ubuntu = chips.filter((c) => c.includes('node-b') && !c.includes('ha2'));
    expect(ubuntu).toHaveLength(1);
    expect(ubuntu[0]).toContain('this server');
    expect(ubuntu[0]).toContain('(1)');
    const headers = [...root.querySelectorAll('[data-testid=node-group-header]')].map((h) =>
      h.textContent.trim()
    );
    expect(headers.filter((h) => h.includes('node-b'))).toHaveLength(1);
  });

  it('does not ask for HA workloads when the module is off', async () => {
    await render(VmList);
    expect(fx.haCalls).toBe(0);
  });

  it('explains HA workloads on an unregistered node or one without access', async () => {
    fx.haEnabled = true;
    fx.workloads = [
      { key: 'kvm:ha-test', kind: 'kvm', name: 'ha-test', managed: true, node: 'node-b' },
      {
        key: 'kvm:ha-test-01',
        kind: 'kvm',
        name: 'ha-test-01',
        managed: true,
        node: 'node-c',
      },
      {
        key: 'kvm:ha-test-02',
        kind: 'kvm',
        name: 'ha-test-02',
        managed: true,
        node: '10.20.30.10',
      },
    ];
    const root = await render(VmList);
    const notice = root.querySelector('[data-testid="ha-missing-notice"]');
    expect(notice).not.toBeNull();
    expect(notice.textContent).toContain(
      'ha-test-01 runs on node node-c: that node is not registered — add it.'
    );
    // Same cause, one notice: the node without SSH access is already in the
    // health banner, which names the HA workload it hides.
    expect(notice.textContent).not.toContain('ha-test-02');
    const banner = root.querySelector('[data-testid="node-health-banner"]');
    expect(banner.textContent).toContain(
      'HA workloads on this node not shown in the list: ha-test-02.'
    );
    expect(notice.textContent).not.toContain('ha-test runs');
    btn(banner, 'Fix in Cluster access').click();
    expect(fx.navigate).toHaveBeenCalledWith('/nodes', { query: { focus: 'access' } });
    btn(notice, 'Add node').click();
    expect(fx.navigate).toHaveBeenCalledWith('/nodes');
  });
});

describe('VmList: node that answers without SSH access', () => {
  it('banner says "reachable, access not set up", never offline, with the access link', async () => {
    const root = await render(VmList);
    const issue = root.querySelector('[data-testid="node-issue-n3"]');
    expect(issue.textContent).toContain('reachable, but SSH access is not set up');
    expect(issue.textContent).not.toContain('offline');
    btn(root.querySelector('[data-testid=node-health-banner]'), 'Fix in Cluster access').click();
    expect(fx.navigate).toHaveBeenCalledWith('/nodes', { query: { focus: 'access' } });
    // The self duplicate is not reported as a separate node.
    expect(root.querySelector('[data-testid="node-issue-d1"]')).toBeNull();
  });

  it('VmNodeCell names the no_access state', async () => {
    const { default: VmNodeCell } = await import('./components/vm-list/VmNodeCell.svelte');
    const target = document.createElement('div');
    document.body.appendChild(target);
    const app = mount(VmNodeCell, {
      target,
      props: { vm: { id: 'x', name: 'x' }, nodeLabel: 'n3', health: 'no_access' },
    });
    flushSync();
    const dot = target.querySelector('[role=img]');
    expect(dot.getAttribute('aria-label')).toBe('Node n3: Reachable, access not set up');
    expect(dot.className).toContain('bg-info');
    unmount(app);
  });
});
