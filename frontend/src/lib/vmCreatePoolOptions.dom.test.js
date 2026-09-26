import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

// Mounts the real VmCreate page and reads the storage-pool <select> the
// operator actually sees.
//
// What it protects: every VM-disk selector filtered with usableForDisks,
// which only strips ISO/backup/template pools — a CONTAINER (Incus)
// pool sailed straight through. So "create a KVM VM" offered
// webkvm-incus, and libvirt then failed with "no storage pool with
// matching name". The ISO pool was worse: it IS a libvirt directory
// pool, so a qcow2 was written into the ISO library beside the install
// media (reproduced live before this was fixed).
//
// purpose.test.js covers the predicate. This covers the wiring: that
// the page actually calls it, which is the part a refactor breaks.
const fx = vi.hoisted(() => ({
  // The real pools on the host this was found on.
  pools: [
    { name: 'ISOS', purpose: 'iso', state: 'active' },
    { name: 'Seagate', purpose: 'disk', state: 'inactive' },
    { name: 'webkvm-disks', purpose: 'disk', state: 'active' },
    { name: 'Lexar-Discos', purpose: 'disk', state: 'active' },
    { name: 'Lexar-Contenedores', purpose: 'container', state: 'created' },
    { name: 'Seagate-contenedores', purpose: 'container', state: 'created' },
    { name: 'webkvm-incus', purpose: 'container', state: 'created' },
  ],
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      listPools: () => Promise.resolve(fx.pools),
      listNetworks: () => Promise.resolve({ networks: [{ name: 'default', mode: 'nat' }] }),
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

vi.mock('$lib/router.svelte.js', () => ({ navigate: () => {} }));

import VmCreate from '../routes/VmCreate.svelte';

const settle = async () => {
  for (let i = 0; i < 8; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

// The pool selector lives on the wizard's "Resources" step, so the
// test has to walk there the same way an operator does. The tab label
// carries its step number ("3 Resources").
async function openResourcesStep(root) {
  await settle();
  const tab = [...root.querySelectorAll('button')].find((b) => /\bResources\b/.test(b.textContent));
  if (!tab) throw new Error('Resources step tab not found');
  tab.click();
  await settle();
}

// Every pool name offered anywhere on the page, read from the rendered
// <option> elements rather than from component internals.
function offeredPoolNames(root) {
  const known = new Set(fx.pools.map((p) => p.name));
  const out = new Set();
  for (const opt of root.querySelectorAll('option')) {
    const v = opt.getAttribute('value') || opt.textContent.trim();
    if (known.has(v)) out.add(v);
  }
  return out;
}

describe('VmCreate never offers a pool from another storage world', () => {
  let target, app;

  beforeEach(() => {
    target = document.createElement('div');
    document.body.appendChild(target);
    app = mount(VmCreate, { target });
  });

  afterEach(async () => {
    if (app) await unmount(app);
    target?.remove();
    app = null;
  });

  it('offers the usable disk pools', async () => {
    await openResourcesStep(target);
    const offered = offeredPoolNames(target);
    // Guards the fixture: if nothing rendered, the assertions below
    // would pass vacuously.
    expect(offered.has('webkvm-disks')).toBe(true);
    expect(offered.has('Lexar-Discos')).toBe(true);
  });

  it('never offers an Incus pool for a KVM disk', async () => {
    await openResourcesStep(target);
    const offered = offeredPoolNames(target);
    for (const name of ['webkvm-incus', 'Lexar-Contenedores', 'Seagate-contenedores']) {
      expect(offered.has(name)).toBe(false);
    }
  });

  it('never offers the ISO library for a KVM disk', async () => {
    await openResourcesStep(target);
    expect(offeredPoolNames(target).has('ISOS')).toBe(false);
  });

  it('never offers an inactive pool', async () => {
    await openResourcesStep(target);
    expect(offeredPoolNames(target).has('Seagate')).toBe(false);
  });

  it('offers container pools and excludes VM disk pools when creating an LXC container', async () => {
    await settle();
    const lxcRadio = [...target.querySelectorAll('button[role="radio"]')].find((b) =>
      b.textContent.includes('LXC')
    );
    expect(lxcRadio).toBeTruthy();
    lxcRadio.click();
    await settle();

    await openResourcesStep(target);
    const offered = offeredPoolNames(target);
    expect(offered.has('webkvm-incus')).toBe(true);
    expect(offered.has('Lexar-Contenedores')).toBe(true);
    expect(offered.has('Seagate-contenedores')).toBe(true);

    expect(offered.has('webkvm-disks')).toBe(false);
    expect(offered.has('Lexar-Discos')).toBe(false);
    expect(offered.has('ISOS')).toBe(false);
  });
});
