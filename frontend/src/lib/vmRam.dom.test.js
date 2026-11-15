// 8.29 #4 / 8.30: Edit hardware follows the same RAM rule as the wizard and
// the backend (utils/ramValidation.js): >= 512 MB unless the explicit
// advanced override is ticked, never below 128 MB, floor <= RAM.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({ updates: [] }));
vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      updateVM: (id, data) => {
        fx.updates.push(data);
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
  return { api, auth: { isAdmin: () => true, canMutate: () => true, role: 'admin' } };
});

import VmHardwareTab from './components/vm-detail/VmHardwareTab.svelte';

const settle = async () => {
  for (let i = 0; i < 8; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};
let target, app;
const vm = {
  id: 'v1',
  name: 'web',
  state: 'shutoff',
  vcpus: 1,
  ram_mb: 512,
  min_ram_mb: 0,
  disks: [],
  networks: [],
};
async function openEdit(v = vm) {
  target = document.createElement('div');
  document.body.appendChild(target);
  app = mount(VmHardwareTab, { target, props: { vm: v, vmId: 'v1', load: () => {} } });
  await settle();
  app.openEditTab('cpu');
  await settle();
}
const setNum = async (id, v) => {
  const el = target.querySelector(`#${id}`);
  el.value = String(v);
  el.dispatchEvent(new Event('input', { bubbles: true }));
  await settle();
};
const saveBtns = () =>
  [...target.querySelectorAll('button')].filter((b) => b.textContent.trim() === 'Save');

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  fx.updates = [];
});
afterEach(async () => {
  if (app) await unmount(app);
  app = null;
  target?.remove();
});

describe('Edit hardware RAM rule', () => {
  it('256 MB is refused with a clear message and Save is disabled', async () => {
    await openEdit();
    await setNum('edit-ram', 256);
    expect(target.textContent).toContain('At least 512 MB of RAM');
    expect(saveBtns().length).toBeGreaterThan(0);
    for (const b of saveBtns()) expect(b.disabled).toBe(true);
  });

  it('the explicit override allows it and is sent to the backend', async () => {
    await openEdit();
    await setNum('edit-ram', 256);
    const box = target.querySelector('#edit-allow-low-ram');
    expect(box).not.toBeNull();
    box.click();
    await settle();
    expect(target.textContent).not.toContain('At least 512 MB of RAM');
    saveBtns()[0].click();
    await settle();
    expect(fx.updates[0]).toMatchObject({ ram_mb: 256, allow_low_ram: true });
  });

  it('a floor above the RAM is refused', async () => {
    await openEdit({ ...vm, ram_mb: 1024 });
    await setNum('edit-min-ram', 2048);
    expect(target.textContent).toContain('cannot be greater than the RAM');
    for (const b of saveBtns()) expect(b.disabled).toBe(true);
  });

  it('a VM that already has less than 512 MB can still be edited elsewhere', async () => {
    await openEdit({ ...vm, ram_mb: 256 });
    expect(target.textContent).not.toContain('At least 512 MB of RAM');
    for (const b of saveBtns()) expect(b.disabled).toBe(false);
  });
});
