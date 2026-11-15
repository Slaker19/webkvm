import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

// Create-on-node with media from the TARGET node's own storage pools: the ISO
// list comes from GET /storage/isos?node=, the payload carries volume names +
// pool (never paths), and extra NICs/disks go to the per-node routes.
const fx = vi.hoisted(() => ({
  isoCalls: [],
  volCalls: [],
  createVM: null,
  disks: [],
  nics: [],
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      listPools: (node) =>
        Promise.resolve(
          node
            ? [{ name: 'east-disks', purpose: 'disk', state: 'active' }]
            : [{ name: 'webkvm-disks', purpose: 'disk', state: 'active' }]
        ),
      listNetworks: (node) =>
        Promise.resolve({ networks: [{ name: node ? 'br-east' : 'default', mode: 'nat' }] }),
      listISOs: (pool, node) => {
        fx.isoCalls.push(node);
        return Promise.resolve(
          node
            ? [
                { name: 'debian.iso', pool: 'east-isos', path: '/srv/isos/debian.iso', size: 1 },
                { name: 'virtio-win.iso', pool: 'east-isos', path: '/srv/isos/virtio-win.iso' },
              ]
            : [{ name: 'local.iso', pool: 'webkvm-isos', path: '/opt/webkvm/isos/local.iso' }]
        );
      },
      listVolumes: (pool, node) => {
        fx.volCalls.push([pool, node]);
        return Promise.resolve([]);
      },
      listNodes: () =>
        Promise.resolve({
          nodes: [
            {
              id: 'remote-kvm',
              name: 'kvm-east',
              backend: 'libvirt',
              type: 'remote',
              enabled: true,
            },
          ],
        }),
      getClusterSSH: () =>
        Promise.resolve({
          configured: true,
          nodes: [
            {
              id: 'remote-kvm',
              host_key: { state: 'pinned' },
              last_test: { ok: true, at: new Date().toISOString() },
            },
          ],
        }),
      createVM: (payload) => fx.createVM(payload),
      createDisk: (id, data, node) => {
        fx.disks.push({ id, data, node });
        return Promise.resolve({});
      },
      createNetIface: (id, data, node) => {
        fx.nics.push({ id, data, node });
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
import { mergePendingVms } from './utils/pendingVms.js';

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
  fx.isoCalls = [];
  fx.volCalls = [];
  fx.disks = [];
  fx.nics = [];
  fx.createVM = vi.fn(() => Promise.resolve({ id: 'vm-uuid-1', name: 'remote-vm1' }));
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
const button = (re) => [...target.querySelectorAll('button')].find((b) => re.test(b.textContent));
async function click(re) {
  const b = button(re);
  if (!b) throw new Error(`button ${re} not found`);
  b.click();
  await settle();
}
// The DOM test environment does not match `option:checked`, which Svelte's
// select binding queries; it then falls back to the first enabled option. So
// the other options are disabled while the change event is dispatched.
function setSelect(sel, value) {
  const others = [...sel.options].filter((o) => o.value !== value && !o.disabled);
  sel.value = value;
  others.forEach((o) => (o.disabled = true));
  sel.dispatchEvent(new Event('change', { bubbles: true }));
  others.forEach((o) => (o.disabled = false));
}

describe('VmCreate on a remote KVM node: media from the node', () => {
  it('lists the ISO volumes of THAT node and lets the operator pick one', async () => {
    await settle();
    expect(fx.isoCalls).toContain(undefined);
    await pick('remote-kvm');
    expect(fx.isoCalls).toContain('remote-kvm');
    const isoSel = target.querySelector('[data-testid=iso-select]');
    expect(isoSel.disabled).toBe(false);
    const values = [...isoSel.options].map((o) => o.value);
    expect(values).toContain('/srv/isos/debian.iso');
    expect(values).not.toContain('/opt/webkvm/isos/local.iso');
    expect(isoSel.textContent).toContain('debian.iso (east-isos)');
    const limits = target.querySelector('[data-testid=remote-kvm-limits]').textContent;
    expect(limits).toContain('storage pools of that node');
    expect(limits).toContain('Cloud-init is not available on remote nodes');
    expect(limits).toContain('Containers are only created on Incus nodes');
    expect(limits).not.toContain('only available on this server');
    // The existing-disk volume list is read from the node too.
    expect(fx.volCalls).toContainEqual(['east-disks', 'remote-kvm']);
  });

  it('going back to this server restores the local ISO list', async () => {
    await settle();
    await pick('remote-kvm');
    await pick('local');
    const values = [...target.querySelector('[data-testid=iso-select]').options].map(
      (o) => o.value
    );
    expect(values).toContain('/opt/webkvm/isos/local.iso');
    expect(values).not.toContain('/srv/isos/debian.iso');
  });

  it('sends the ISO as a volume name + pool and attaches extra disks/NICs on that node', async () => {
    await settle();
    await pick('remote-kvm');
    await click(/^\s*Advanced Mode\s*$/);
    setSelect(target.querySelector('[data-testid=iso-select]'), '/srv/isos/debian.iso');
    await settle();
    await click(/\bIdentity\b/);
    const name = target.querySelector('input[placeholder="my-vm"]');
    name.value = 'remote-vm1';
    name.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    await click(/Network and devices|\bNetwork\b/);
    await click(/Add Disk/);
    const netSel = [...target.querySelectorAll('select')].find((s) =>
      [...s.options].some((o) => o.value === 'br-east')
    );
    setSelect(netSel, 'br-east');
    await settle();
    await click(/Add Interface/);
    await click(/\bReview\b/);
    const create = button(/Create VM/);
    expect(create, target.textContent).toBeTruthy();
    create.click();
    await settle();
    expect(fx.createVM).toHaveBeenCalledOnce();
    const payload = fx.createVM.mock.calls[0][0];
    expect(payload).toMatchObject({
      node: 'remote-kvm',
      network: 'br-east',
      iso: 'debian.iso',
      iso_pool: 'east-isos',
    });
    expect(JSON.stringify(payload)).not.toContain('/srv/isos');
    expect(payload).not.toHaveProperty('cloud_init');
    expect(fx.disks).toEqual([
      {
        id: 'vm-uuid-1',
        data: expect.objectContaining({ pool: 'east-disks' }),
        node: 'remote-kvm',
      },
    ]);
    expect(fx.nics).toEqual([
      {
        id: 'vm-uuid-1',
        data: expect.objectContaining({ network: 'br-east' }),
        node: 'remote-kvm',
      },
    ]);
    // 8.30: the new VM is shown in the list before any listing has it.
    const pending = mergePendingVms([]);
    expect(pending).toEqual([
      expect.objectContaining({ id: 'vm-uuid-1', name: 'remote-vm1', pending: true }),
    ]);
    expect(pending[0].node).toBeTruthy();
    expect(payload).not.toHaveProperty('allow_low_ram');
  });
});
