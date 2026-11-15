// 8.29 minor defects (8.30): noVNC "disconnected RFB object" on a forced
// power-off, snapshot field labels / disabled-button hint, the delete
// confirmation label and the duplicated "Settings updated" toast.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({ rfbs: [] }));

vi.mock('$lib/stores/auth.svelte.js', () => ({
  api: { getVNCWsUrl: () => 'ws://x/vnc' },
  auth: { isAdmin: () => true, canMutate: () => true, role: 'admin' },
}));
vi.mock('@novnc/novnc', () => ({
  default: class {
    constructor() {
      this.listeners = {};
      this.disconnects = 0;
      this.dead = false;
      this.deadDisconnects = 0;
      fx.rfbs.push(this);
    }
    addEventListener(name, fn) {
      this.listeners[name] = fn;
    }
    disconnect() {
      // Real noVNC logs "Tried changing state of a disconnected RFB object"
      // when this runs on an object that already disconnected.
      if (this.dead) this.deadDisconnects++;
      else this.disconnects++;
    }
    fire(name, detail) {
      if (name === 'disconnect') this.dead = true;
      this.listeners[name]?.({ detail });
    }
  },
}));

import RemoteVncConsole from './components/vm-detail/RemoteVncConsole.svelte';
import VmSnapshotsTab from './components/vm-detail/VmSnapshotsTab.svelte';
import DeleteVmDialog from './components/DeleteVmDialog.svelte';
import { toast, getToasts, dismiss } from './stores/toast.svelte.js';

const settle = async () => {
  for (let i = 0; i < 8; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

let target, app;
beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  fx.rfbs = [];
  target = document.createElement('div');
  document.body.appendChild(target);
});
afterEach(async () => {
  if (app) await unmount(app);
  app = null;
  target.remove();
  for (const x of getToasts()) dismiss(x.id);
});

describe('RemoteVncConsole on a forced power-off', () => {
  it('does not disconnect an RFB object the server already closed', async () => {
    app = mount(RemoteVncConsole, {
      target,
      props: { vmId: 'v1', ticket: 'T', node: 'kvm-b', hv: 'kvm' },
    });
    await settle();
    const rfb = fx.rfbs[0];
    expect(rfb).toBeTruthy();
    rfb.fire('connect');
    rfb.fire('disconnect', { clean: false });
    await settle();
    await unmount(app); // the VM went off: the console tab unmounts it
    app = null;
    expect(rfb.deadDisconnects).toBe(0);
  });

  it('still disconnects a live session on unmount', async () => {
    app = mount(RemoteVncConsole, {
      target,
      props: { vmId: 'v1', ticket: 'T', node: 'kvm-b', hv: 'kvm' },
    });
    await settle();
    fx.rfbs[0].fire('connect');
    await unmount(app);
    app = null;
    expect(fx.rfbs[0].disconnects).toBe(1);
  });
});

describe('VmSnapshotsTab', () => {
  const base = {
    snapshots: [],
    createSnapshot: () => {},
    revertSnapshot: () => {},
    deleteSnapshot: () => {},
    saveSchedule: () => {},
  };

  it('labels the name field and explains why Create is disabled', async () => {
    app = mount(VmSnapshotsTab, { target, props: base });
    await settle();
    const input = target.querySelector('#snapshot-name');
    expect(input).not.toBeNull();
    const label = target.querySelector('label[for=snapshot-name]');
    expect(label?.textContent.trim()).toBe('Snapshot name');
    const btn = [...target.querySelectorAll('button')].find((b) =>
      b.textContent.includes('Create Snapshot')
    );
    expect(btn.disabled).toBe(true);
    const hint = target.querySelector('[data-testid=snapshot-name-hint]');
    expect(hint?.textContent.trim()).toBe('Enter a name');
    expect(btn.getAttribute('aria-describedby')).toBe('snapshot-name-hint');
  });

  it('drops the hint once a name is typed', async () => {
    app = mount(VmSnapshotsTab, { target, props: { ...base, snapName: 'snap1' } });
    await settle();
    expect(target.querySelector('[data-testid=snapshot-name-hint]')).toBeNull();
  });
});

describe('toast store', () => {
  it('does not stack the same message twice while it is showing', () => {
    toast.success('Settings updated');
    toast.success('Settings updated');
    expect(getToasts().filter((x) => x.message === 'Settings updated').length).toBe(1);
    toast.error('Settings updated');
    expect(getToasts().length).toBe(2);
  });
});

describe('DeleteVmDialog step 2', () => {
  it('the type-the-name field has an accessible label', async () => {
    app = mount(DeleteVmDialog, {
      target,
      props: { open: true, vmId: 'v1', vmName: 'web', vmDisks: [{ name: 'web.qcow2' }] },
    });
    await settle();
    const box = document.querySelector('[role=dialog] input[type=checkbox]');
    box.click();
    await settle();
    [...document.querySelectorAll('[role=dialog] button')]
      .find((b) => b.textContent.trim() === 'Delete')
      .click();
    await settle();
    const input = document.querySelector('#delete-vm-confirm-name');
    expect(input).not.toBeNull();
    const label = document.querySelector('label[for=delete-vm-confirm-name]');
    expect(label?.textContent).toContain('web');
  });
});
