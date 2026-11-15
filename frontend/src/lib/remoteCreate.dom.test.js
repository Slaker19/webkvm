import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  poolCalls: [],
  netCalls: [],
  ssh: { configured: true, nodes: [] },
  members: [],
  sshTests: [],
  afterTest: null,
  remoteNets: null,
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      listPools: (node) => {
        fx.poolCalls.push(node);
        return Promise.resolve(
          node
            ? [{ name: 'east-disks', purpose: 'disk', state: 'active' }]
            : [{ name: 'webkvm-disks', purpose: 'disk', state: 'active' }]
        );
      },
      listNetworks: (node) => {
        fx.netCalls.push(node);
        if (node && fx.remoteNets) return Promise.resolve({ networks: fx.remoteNets });
        return Promise.resolve({ networks: [{ name: node ? 'br-east' : 'default', mode: 'nat' }] });
      },
      listNodes: () =>
        Promise.resolve({
          nodes: [
            { id: 'local', name: 'web-local', type: 'local', enabled: true },
            {
              id: 'remote-kvm',
              name: 'kvm-east',
              backend: 'libvirt',
              type: 'remote',
              enabled: true,
              access: fx.nodeAccess,
            },
          ],
        }),
      getClusterSSH: () => Promise.resolve(fx.ssh),
      testNodeSSH: (id) => {
        fx.sshTests.push(id);
        if (fx.afterTest) fx.ssh = fx.afterTest;
        return Promise.resolve({ ok: true });
      },
      getIncusClusterMembers: () => Promise.resolve({ clustered: true, members: fx.members }),
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
    auth: new Proxy(
      { user: { role: 'admin' }, role: 'admin' },
      {
        get(target, prop) {
          if (prop in target) return target[prop];
          if (typeof prop !== 'string') return undefined;
          return () => true;
        },
      }
    ),
  };
});
vi.mock('$lib/router.svelte.js', () => ({
  getRoute: () => ({ query: {} }),
  navigate: () => {},
}));

import VmCreate from '../routes/VmCreate.svelte';

const settle = async () => {
  for (let i = 0; i < 10; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

let target, app;
beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  localStorage.removeItem('webkvm.createWizardMode');
  fx.poolCalls = [];
  fx.netCalls = [];
  fx.ssh = { configured: true, nodes: [] };
  fx.members = [];
  fx.sshTests = [];
  fx.afterTest = null;
  fx.remoteNets = null;
  target = document.createElement('div');
  document.body.appendChild(target);
  app = mount(VmCreate, { target });
});
afterEach(async () => {
  if (app) await unmount(app);
  target?.remove();
});

const nodeSelect = () =>
  [...target.querySelectorAll('select')].find((s) =>
    [...s.options].some((o) => o.value === 'remote-kvm')
  );

async function pick(value) {
  const sel = nodeSelect();
  sel.value = value;
  sel.dispatchEvent(new Event('change', { bubbles: true }));
  await settle();
}

describe('VmCreate on a remote KVM node', () => {
  it('loads that node pools/networks and explains the limits and missing access', async () => {
    await settle();
    await pick('remote-kvm');
    expect(fx.poolCalls).toContain('remote-kvm');
    expect(fx.netCalls).toContain('remote-kvm');
    const limits = target.querySelector('[data-testid=remote-kvm-limits]');
    // Media now comes from the node's own pools; cloud-init and containers
    // on a libvirt node stay unsupported, with guidance.
    expect(limits.textContent).toContain('listed from the storage pools of that node');
    expect(limits.textContent).toContain('Cloud-init is not available on remote nodes');
    expect(limits.textContent).toContain('Containers are only created on Incus nodes');
    expect(limits.textContent).toContain('Choose the network of that node');
    const access = target.querySelector('[data-testid=remote-access-not-ready]');
    expect(access.textContent).toContain('Access to this node is not set up yet');
  });

  it('hides the access guidance once the node access is ready', async () => {
    fx.ssh = {
      configured: true,
      nodes: [
        {
          id: 'remote-kvm',
          host_key: { state: 'pinned' },
          last_test: { ok: true, at: new Date().toISOString() },
        },
      ],
    };
    unmount(app);
    target.innerHTML = '';
    app = mount(VmCreate, { target });
    await settle();
    await pick('remote-kvm');
    expect(target.querySelector('[data-testid=remote-access-not-ready]')).toBeNull();
    expect(target.querySelector('[data-testid=remote-kvm-limits]')).not.toBeNull();
  });

  const remount = async (node) => {
    fx.ssh = { configured: true, nodes: [{ id: 'remote-kvm', ...node }] };
    unmount(app);
    target.innerHTML = '';
    app = mount(VmCreate, { target });
    await settle();
    await pick('remote-kvm');
  };

  it('a pinned node with no/stale test is "not verified": neutral, not blocking, one-click test', async () => {
    await remount({ host_key: { state: 'pinned' } });
    expect(target.querySelector('[data-testid=remote-access-not-ready]')).toBeNull();
    const notice = target.querySelector('[data-testid=remote-access-unverified]');
    expect(notice.textContent).toContain('not verified recently');
    expect(target.textContent).not.toContain('is not set up yet');
    fx.afterTest = {
      configured: true,
      nodes: [
        {
          id: 'remote-kvm',
          host_key: { state: 'pinned' },
          last_test: { ok: true, at: new Date().toISOString() },
        },
      ],
    };
    target.querySelector('[data-testid=remote-access-test-now]').click();
    await settle();
    expect(fx.sshTests).toEqual(['remote-kvm']);
    expect(target.querySelector('[data-testid=remote-access-unverified]')).toBeNull();
  });

  it('a stale passing test (older than 10 minutes) is also "not verified"', async () => {
    const old = new Date(Date.now() - 30 * 60 * 1000).toISOString();
    await remount({ host_key: { state: 'pinned' }, last_test: { ok: true, at: old } });
    expect(target.querySelector('[data-testid=remote-access-unverified]')).not.toBeNull();
  });

  // 8.26 defect 1: the node-list probe (access:'ok') is the same source the
  // Nodes page uses; with it, never "not verified recently".
  it('a stale test is not "not verified" when the node probe says access ok', async () => {
    const old = new Date(Date.now() - 4 * 3600 * 1000).toISOString();
    fx.nodeAccess = 'ok';
    await remount({ host_key: { state: 'pinned' }, last_test: { ok: true, at: old } });
    expect(target.querySelector('[data-testid=remote-access-unverified]')).toBeNull();
    expect(target.querySelector('[data-testid=remote-access-not-ready]')).toBeNull();
    expect(target.textContent).not.toContain('not verified recently');
    fx.nodeAccess = undefined;
  });

  it('only auth/key/host-key codes still say "not set up"', async () => {
    await remount({
      host_key: { state: 'pinned' },
      last_test: { ok: false, code: 'ssh_auth_denied', at: new Date().toISOString() },
    });
    expect(target.querySelector('[data-testid=remote-access-not-ready]')).not.toBeNull();
    await remount({
      host_key: { state: 'pinned' },
      last_test: { ok: false, code: 'node_unreachable', at: new Date().toISOString() },
    });
    expect(target.querySelector('[data-testid=remote-access-not-ready]')).toBeNull();
    expect(target.querySelector('[data-testid=remote-access-unverified]')).not.toBeNull();
  });

  // 8.26 defect 6: the Network step error must not show in the footer of
  // another step (it appeared on step 2 before the name was typed).
  it('scopes the "choose the network" error to the network and review steps', async () => {
    fx.remoteNets = [];
    await remount({
      host_key: { state: 'pinned' },
      last_test: { ok: true, at: new Date().toISOString() },
    });
    const footerErr = () => target.querySelector('form')?.textContent || '';
    [...target.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Next').click();
    await settle();
    // Identity step, name not typed yet: blocked, but no step-4 error.
    expect(footerErr()).not.toContain('Choose the network of that node');
  });

  // 8.26 defect 6: this server is named like everywhere else.
  it('labels this server "<name> (this server)", not "Local"', async () => {
    await settle();
    const opt = [...nodeSelect().options].find((o) => o.value === 'local');
    expect(opt.textContent.trim()).toBe('web-local (this server)');
  });

  it('returning to the local server reloads local lists', async () => {
    await settle();
    await pick('remote-kvm');
    fx.poolCalls = [];
    await pick('local');
    expect(fx.poolCalls).toEqual([undefined]);
    expect(target.querySelector('[data-testid=remote-kvm-limits]')).toBeNull();
  });
});

describe('VmCreate remote bridge name (libvirt without interface driver)', () => {
  const click = async (label) => {
    [...target.querySelectorAll('button')].find((b) => b.textContent.trim() === label)?.click();
    await settle();
  };
  // Walk the wizard to its Network step with a valid name.
  const goNetwork = async () => {
    await click('Next');
    const name = target.querySelector('input');
    name.value = 'remote-vm1';
    name.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    await click('Next');
    await click('Next');
  };

  it('EASY mode offers only libvirt networks, no free text', async () => {
    await settle();
    await pick('remote-kvm');
    await goNetwork();
    expect(target.textContent).toContain('Network and devices');
    expect(target.querySelector('[data-testid=remote-bridge-name]')).toBeNull();
  });

  // 8.30 (8.29 #6): host bridges and libvirt networks are named apart.
  it('lists host bridges and libvirt networks with what the VM gets', async () => {
    fx.remoteNets = [
      { name: 'virbr0', kind: 'nat', bridge: 'virbr0' },
      { name: 'vmbr0', kind: 'direct', bridge: 'vmbr0' },
    ];
    await settle();
    await pick('remote-kvm');
    await goNetwork();
    const sel = target.querySelector('select[aria-label=Network]');
    expect(sel).not.toBeNull();
    const groups = [...sel.querySelectorAll('optgroup')].map((g) => g.label);
    expect(groups).toEqual(['Host bridges', 'Virtual networks']);
    const texts = [...sel.options].map((o) => o.textContent.trim());
    expect(texts).toContain('Host bridge vmbr0 — VMs get an address on your LAN');
    expect(texts).toContain('libvirt network virbr0 (NAT) — private address behind this host');
    expect(target.textContent).not.toContain('Bridge (shared L2) is the default');
    expect(target.querySelector('[data-testid=remote-no-host-bridge]')).toBeNull();
  });

  it('says when a node reports no host bridge and points to the typed option', async () => {
    fx.remoteNets = [{ name: 'virbr0', kind: 'nat', bridge: 'virbr0' }];
    await settle();
    await pick('remote-kvm');
    await goNetwork();
    const note = target.querySelector('[data-testid=remote-no-host-bridge]');
    expect(note).not.toBeNull();
    expect(note.textContent).toContain('Advanced');
  });

  it('ADVANCED mode explains and validates a typed bridge name', async () => {
    await settle();
    await pick('remote-kvm');
    await click('Advanced Mode');
    await goNetwork();
    const input = target.querySelector('[data-testid=remote-bridge-name]');
    expect(input).not.toBeNull();
    expect(target.textContent).toContain('This node cannot list its bridges');
    expect(target.textContent).toContain('vmbr0 or br0');
    input.value = 'bad name!';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    expect(target.textContent).toContain('Use 1 to 15 characters');
    input.value = 'vmbr0';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    expect(target.textContent).not.toContain('Use 1 to 15 characters');
  });
});
