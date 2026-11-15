import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

// Create wizard on a remote KVM node whose SSH access is not set up: the
// listings of that node answer 409 no_key. The wizard must say so (with a
// way to fix it) instead of "this node reports no networks" / an empty Pool
// dropdown, show one notice about remote resources, list this server once
// and name the target node in the review.
const fx = vi.hoisted(() => ({ listCode: 'no_key' }));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const fail = () => {
    const e = new Error('no cluster SSH key');
    e.status = 409;
    e.code = fx.listCode;
    return Promise.reject(e);
  };
  const api = new Proxy(
    {
      listPools: (node) =>
        node ? fail() : Promise.resolve([{ name: 'webkvm-disks', purpose: 'disk' }]),
      listNetworks: (node) =>
        node ? fail() : Promise.resolve({ networks: [{ name: 'default', mode: 'nat' }] }),
      listISOs: (_p, node) => (node ? fail() : Promise.resolve([])),
      listNodes: () =>
        Promise.resolve({
          nodes: [
            { id: 'local', name: 'node-b', type: 'local', enabled: true },
            // Remote entry pointing at this same server (duplicate of local).
            {
              id: 'self-dup',
              name: 'node-b',
              backend: 'libvirt',
              type: 'remote',
              enabled: true,
              is_self: true,
            },
            {
              id: 'remote-kvm',
              name: 'kvm-east',
              backend: 'libvirt',
              type: 'remote',
              enabled: true,
              access: 'not_set_up',
              access_code: 'no_key',
            },
          ],
        }),
      getClusterSSH: () => Promise.resolve({ configured: false, nodes: [] }),
      getIncusClusterMembers: () => Promise.resolve({ clustered: false, members: [] }),
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
  fx.listCode = 'no_key';
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
const click = async (label) => {
  [...target.querySelectorAll('button')].find((b) => b.textContent.trim() === label)?.click();
  await settle();
};
// Image -> Identity (valid name) -> Resources.
const goResources = async () => {
  await click('Next');
  const name = target.querySelector('input');
  name.value = 'remote-vm1';
  name.dispatchEvent(new Event('input', { bubbles: true }));
  await settle();
  await click('Next');
};

describe('VmCreate target node selector', () => {
  it('lists this server once and has an accessible name', async () => {
    await settle();
    const sel = nodeSelect();
    expect(sel).toBeTruthy();
    expect([...sel.options].map((o) => o.value)).toEqual(['local', 'remote-kvm']);
    expect(sel.getAttribute('aria-label')).toBe('Target node');
  });
});

describe('VmCreate on a remote KVM node without SSH access', () => {
  it('shows one notice about remote resources (no legacy "this host" warning)', async () => {
    await settle();
    await pick('remote-kvm');
    expect(target.querySelector('[data-testid=remote-kvm-limits]')).not.toBeNull();
    expect(target.textContent).not.toContain('These selectors still show this host’s pools');
  });

  it('Resources step: explains the real cause and links to Cluster access', async () => {
    await settle();
    await pick('remote-kvm');
    await goResources();
    const alert = target.querySelector('[data-testid=remote-list-access]');
    expect(alert).not.toBeNull();
    expect(alert.textContent).toContain('Set up SSH access');
    expect(alert.querySelector('a[href="#/nodes"]')).not.toBeNull();
    const pool = target.querySelector('select[aria-label="Pool"]');
    expect(pool).not.toBeNull();
  });

  it('Network step: no "reports no libvirt networks" when the cause is SSH access', async () => {
    await settle();
    await pick('remote-kvm');
    await goResources();
    await click('Next');
    expect(target.querySelector('[data-testid=remote-no-networks]')).toBeNull();
    const alert = target.querySelector('[data-testid=remote-list-access]');
    expect(alert).not.toBeNull();
    expect(alert.textContent).toContain('kvm-east');
    expect(alert.querySelector('a[href="#/nodes"]')).not.toBeNull();
  });

  it('a host key that is not pinned has its own cause', async () => {
    fx.listCode = 'host_key_unpinned';
    await settle();
    await pick('remote-kvm');
    await goResources();
    const alert = target.querySelector('[data-testid=remote-list-access]');
    expect(alert.textContent).toContain('host key');
  });

  it('Review shows the target node', async () => {
    await settle();
    await pick('remote-kvm');
    await goResources();
    await click('Next');
    await click('Next');
    const row = [...target.querySelectorAll('dl > div')].find(
      (d) => d.querySelector('dt')?.textContent.trim() === 'Target node'
    );
    expect(row?.querySelector('dd').textContent.trim()).toBe('kvm-east');
  });

  it('Review names this server for a local target', async () => {
    await settle();
    await goResources();
    await click('Next');
    await click('Next');
    const row = [...target.querySelectorAll('dl > div')].find(
      (d) => d.querySelector('dt')?.textContent.trim() === 'Target node'
    );
    expect(row?.querySelector('dd').textContent.trim()).toBe('This server');
  });
});
