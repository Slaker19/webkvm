import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  calls: [],
  admin: true,
  released: [],
  restoreErr: null,
  restoreRes: null,
  disableErrs: [],
}));

vi.mock('$lib/stores/auth.svelte.js', () => ({
  auth: { isAdmin: () => fx.admin },
  api: {
    listHAWorkloads: () => Promise.resolve({ workloads: [], summary: null, preconditions: null }),
    listReleasedVMs: () => {
      fx.calls.push(['list']);
      return Promise.resolve({ released: fx.released, local_node: 'ha1' });
    },
    restoreReleasedVM: (name, opts) => {
      fx.calls.push(['restore', name, opts]);
      return fx.restoreErr
        ? Promise.reject(fx.restoreErr)
        : Promise.resolve(fx.restoreRes || { changed: true, state: 'defined', node: 'ha1' });
    },
    discardReleasedVM: (name, confirm) => {
      fx.calls.push(['discard', name, confirm]);
      return Promise.resolve({ changed: true });
    },
    disableHAWorkload: (k, n, p, o) => {
      fx.calls.push(['disable', k, n, p, o]);
      const err = fx.disableErrs.shift();
      return err ? Promise.reject(err) : Promise.resolve({ changed: true, state: 'absent' });
    },
  },
}));

import HaReleasedList from './components/HaReleasedList.svelte';
import HaDisableDialog from './components/HaDisableDialog.svelte';
import { clusterModule } from './stores/clusterModule.svelte.js';
import { resetHaReleased } from './stores/haReleased.svelte.js';
import { setLocale } from './i18n.svelte.js';

const settle = async (n = 6) => {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => setTimeout(r, 3));
    flushSync();
  }
};
const dlgBtn = (text) =>
  [...document.querySelectorAll('[role=dialog] button')].find((b) => b.textContent.trim() === text);
const typeInto = (sel, value) => {
  const el = document.querySelector(sel);
  el.value = value;
  el.dispatchEvent(new Event('input', { bubbles: true }));
};

const entry = (over = {}) => ({
  key: 'kvm:web1',
  name: 'web1',
  uuid: '5b0c6f1e-1111-4222-8333-944455556666',
  status: 'released',
  mode: 'stop',
  drbd_resource: 'vm_web1',
  mount_dir: '/var/lib/libvirt/web1',
  last_node: 'ha2',
  allowed_nodes: [],
  has_xml: true,
  restorable: true,
  released_at: '2026-11-14T02:02:54Z',
  ...over,
});

let mounted;
async function show(Comp, props) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  mounted = { app: mount(Comp, { target, props }), target };
  await settle();
  return target;
}

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  Object.assign(fx, {
    calls: [],
    admin: true,
    released: [],
    restoreErr: null,
    restoreRes: null,
    disableErrs: [],
  });
  clusterModule.enabled = true;
  resetHaReleased();
});
afterEach(() => {
  setLocale('en');
  if (mounted) {
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
  }
  document.body.innerHTML = '';
  resetHaReleased();
});

describe('HaReleasedList', () => {
  it('lists released VMs with a badge and a Restore button only when restorable', async () => {
    fx.released = [
      entry(),
      entry({ name: 'db1', key: 'kvm:db1', restorable: false, has_xml: false }),
      entry({ name: 'old', key: 'kvm:old', status: 'restored' }),
    ];
    const t = await show(HaReleasedList, {});
    const web = t.querySelector('[data-testid=ha-released-web1]');
    expect(web.textContent).toContain('Released · storage freed');
    expect(web.textContent).toContain('Last node: ha2');
    expect(web.textContent).toContain('DRBD: vm_web1');
    expect([...web.querySelectorAll('button')].map((b) => b.textContent.trim())).toEqual([
      'Restore',
      'Discard entry',
    ]);
    const db = t.querySelector('[data-testid=ha-released-db1]');
    expect(db.textContent).toContain('No saved definition');
    expect([...db.querySelectorAll('button')].map((b) => b.textContent.trim())).toEqual([
      'Discard entry',
    ]);
    const old = t.querySelector('[data-testid=ha-released-old]');
    expect(old.textContent).toContain('Restored');
    expect(old.textContent).not.toContain('Released · storage freed');
  });

  it('is hidden for non-admins and with the module off, and when empty in the VM list', async () => {
    fx.released = [entry()];
    fx.admin = false;
    let t = await show(HaReleasedList, {});
    expect(t.querySelector('[data-testid=ha-released-list]')).toBeNull();
    expect(fx.calls).toEqual([]);
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;

    fx.admin = true;
    clusterModule.enabled = false;
    t = await show(HaReleasedList, {});
    expect(t.querySelector('[data-testid=ha-released-list]')).toBeNull();
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;

    clusterModule.enabled = true;
    fx.released = [];
    t = await show(HaReleasedList, { hideWhenEmpty: true });
    expect(t.querySelector('[data-testid=ha-released-list]')).toBeNull();
  });

  it('says so when there are no released VMs', async () => {
    fx.released = [];
    const t = await show(HaReleasedList, {});
    const empty = t.querySelector('[data-testid=ha-released-empty]');
    expect(empty).toBeTruthy();
    expect(empty.textContent).toContain('No released VMs');
  });

  it('restore needs the typed name, sends start and the local node, then refreshes', async () => {
    fx.released = [entry()];
    const onChanged = vi.fn();
    const t = await show(HaReleasedList, { onChanged });
    t.querySelector('[data-testid=ha-released-web1] button').click();
    await settle();
    const steps = document.querySelector('[data-testid=ha-restore-steps]').textContent;
    expect(steps).toContain('vm_web1');
    expect(steps).toContain('/var/lib/libvirt/web1');
    expect(document.querySelector('[role=dialog]').textContent).toContain('ha1');
    dlgBtn('Restore').click();
    await settle();
    expect(fx.calls.filter((c) => c[0] === 'restore')).toEqual([]);
    typeInto('#ha-restore-confirm', 'web1');
    const start = [...document.querySelectorAll('[role=dialog] input[type=checkbox]')][0];
    start.click();
    await settle();
    dlgBtn('Restore').click();
    await settle();
    expect(fx.calls.find((c) => c[0] === 'restore')).toEqual([
      'restore',
      'web1',
      { confirm: 'web1', start: true, node: 'ha1' },
    ]);
    expect(onChanged).toHaveBeenCalled();
    expect(fx.calls.filter((c) => c[0] === 'list').length).toBeGreaterThan(1);
    expect(document.querySelector('[data-testid=ha-restore-steps]')).toBeNull();
  });

  it('shows a translated, rolled-back error when a restore step fails', async () => {
    fx.released = [entry()];
    fx.restoreErr = Object.assign(new Error('mount: wrong fs type'), { code: 'mount_failed' });
    const t = await show(HaReleasedList, {});
    t.querySelector('[data-testid=ha-released-web1] button').click();
    await settle();
    typeInto('#ha-restore-confirm', 'web1');
    await settle();
    dlgBtn('Restore').click();
    await settle();
    const dlg = document.querySelector('[role=dialog]').textContent;
    expect(dlg).toContain('The filesystem could not be mounted. Every step was undone.');
    expect(dlg).not.toContain('wrong fs type');
  });

  it('shows the peers to bring up, a copy button and Re-check on drbd_peers_down', async () => {
    fx.released = [entry({ drbd_peers: ['ha2', 'ha3'] })];
    fx.restoreErr = Object.assign(new Error('no quorum'), {
      code: 'drbd_peers_down',
      data: {
        code: 'drbd_peers_down',
        resource: 'vm_web1',
        peers: [
          { node: 'ha2', connection: 'Connecting', command: 'drbdadm up vm_web1' },
          { node: 'ha3', connection: 'Connecting', command: 'drbdadm up vm_web1' },
        ],
      },
    });
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const t = await show(HaReleasedList, {});
    t.querySelector('[data-testid=ha-released-web1] button').click();
    await settle();
    // The precondition is announced before restoring.
    expect(document.querySelector('[data-testid=ha-restore-step-peers]').textContent).toContain(
      'drbdadm up vm_web1'
    );
    expect(document.querySelector('[data-testid=ha-restore-step-peers]').textContent).toContain(
      'ha2, ha3'
    );
    expect(document.querySelector('[data-testid=ha-restore-steps]').textContent).toContain(
      '/etc/libvirt/qemu/web1.xml'
    );
    expect(document.querySelector('[data-testid=ha-restore-recheck]')).toBeNull();
    typeInto('#ha-restore-confirm', 'web1');
    await settle();
    dlgBtn('Restore').click();
    await settle();
    const box = document.querySelector('[data-testid=ha-restore-peers-down]');
    expect(box).not.toBeNull();
    expect(document.querySelector('[role=dialog]').textContent).toContain(
      'The other replicas of the DRBD resource are down'
    );
    const nodes = [...box.querySelectorAll('[data-testid=ha-restore-peers] li')].map((li) =>
      li.textContent.replace(/\s+/g, ' ').trim()
    );
    expect(nodes).toEqual(['ha2 (Connecting)', 'ha3 (Connecting)']);
    expect(box.querySelector('[data-testid=copy-commands-text]').textContent).toBe(
      '# ha2, ha3\ndrbdadm up vm_web1'
    );
    [...box.querySelectorAll('button')].find((b) => b.textContent.includes('Copy')).click();
    await settle();
    expect(writeText).toHaveBeenCalledWith('# ha2, ha3\ndrbdadm up vm_web1');
    // Re-check retries the same restore; success closes the dialog.
    fx.restoreErr = null;
    document.querySelector('[data-testid=ha-restore-recheck]').click();
    await settle();
    expect(fx.calls.filter((c) => c[0] === 'restore')).toEqual([
      ['restore', 'web1', { confirm: 'web1', start: false, node: 'ha1' }],
      ['restore', 'web1', { confirm: 'web1', start: false, node: 'ha1' }],
    ]);
    expect(document.querySelector('[data-testid=ha-restore-peers-down]')).toBeNull();
  });

  it('peers-down guidance tolerates a partial body and other errors show none', async () => {
    fx.released = [entry()];
    fx.restoreErr = Object.assign(new Error('no quorum'), {
      code: 'drbd_peers_down',
      data: { resource: 'vm_web1', peers: [null, { node: '' }, { node: 'ha2' }] },
    });
    const t = await show(HaReleasedList, {});
    t.querySelector('[data-testid=ha-released-web1] button').click();
    await settle();
    expect(document.querySelector('[data-testid=ha-restore-step-peers]').textContent).toContain(
      'other replica nodes'
    );
    typeInto('#ha-restore-confirm', 'web1');
    await settle();
    dlgBtn('Restore').click();
    await settle();
    const box = document.querySelector('[data-testid=ha-restore-peers-down]');
    expect(box.querySelector('[data-testid=ha-restore-peers]').textContent).toContain('ha2');
    expect(box.querySelector('[data-testid=copy-commands-text]').textContent).toBe(
      'drbdadm up vm_web1'
    );
    fx.restoreErr = Object.assign(new Error('x'), { code: 'mount_failed' });
    document.querySelector('[data-testid=ha-restore-recheck]').click();
    await settle();
    expect(document.querySelector('[data-testid=ha-restore-peers-down]')).toBeNull();
  });

  it('peers-down guidance is translated (es)', async () => {
    setLocale('es');
    fx.released = [entry({ drbd_peers: ['ha2'] })];
    fx.restoreErr = Object.assign(new Error('no quorum'), {
      code: 'drbd_peers_down',
      data: {
        resource: 'vm_web1',
        peers: [{ node: 'ha2', connection: 'Connecting', command: 'drbdadm up vm_web1' }],
      },
    });
    const t = await show(HaReleasedList, {});
    t.querySelector('[data-testid=ha-released-web1] button').click();
    await settle();
    typeInto('#ha-restore-confirm', 'web1');
    await settle();
    dlgBtn('Restaurar').click();
    await settle();
    const dlg = document.querySelector('[role=dialog]').textContent;
    expect(dlg).toContain('Las otras réplicas del recurso DRBD están caídas');
    expect(document.querySelector('[data-testid=ha-restore-recheck]').textContent.trim()).toBe(
      'Volver a comprobar'
    );
  });

  it('discard requires the typed name and only removes the entry', async () => {
    fx.released = [entry()];
    const t = await show(HaReleasedList, {});
    const [, discard] = t.querySelectorAll('[data-testid=ha-released-web1] button');
    discard.click();
    await settle();
    expect(document.querySelector('[role=dialog]').textContent).toContain('are not touched');
    dlgBtn('Discard').click();
    await settle();
    expect(fx.calls.filter((c) => c[0] === 'discard')).toEqual([]);
    typeInto('#ha-discard-confirm', 'web1');
    await settle();
    dlgBtn('Discard').click();
    await settle();
    expect(fx.calls.find((c) => c[0] === 'discard')).toEqual(['discard', 'web1', 'web1']);
  });

  it('is translated (es)', async () => {
    setLocale('es');
    fx.released = [entry()];
    const t = await show(HaReleasedList, {});
    expect(t.textContent).toContain('VMs liberadas');
    expect(t.textContent).toContain('Restaurar');
  });
});

describe('HaDisableDialog released-VM flow', () => {
  const ref = { kind: 'kvm', name: 'web1', project: '', key: 'kvm:web1', label: 'web1' };

  it('offers an explicit accept when the definition could not be saved', async () => {
    fx.disableErrs = [
      Object.assign(new Error('could not save'), { code: 'release_capture_failed' }),
    ];
    const onDone = vi.fn();
    await show(HaDisableDialog, { ref, defaultMode: 'stop', onDone });
    expect(document.querySelector('[data-testid=ha-accept-unrestorable]')).toBeNull();
    dlgBtn('Disable HA').click();
    await settle();
    expect(fx.calls[0][4]).toEqual({ mode: 'stop', confirm: '' });
    expect(document.querySelector('[role=dialog]').textContent).toContain(
      'WebKVM could not save the VM definition'
    );
    const accept = document.querySelector('[data-testid=ha-accept-unrestorable] input');
    expect(accept).not.toBeNull();
    // Retrying without ticking it still does not accept.
    fx.disableErrs = [
      Object.assign(new Error('could not save'), { code: 'release_capture_failed' }),
    ];
    dlgBtn('Disable HA').click();
    await settle();
    expect(fx.calls[1][4]).toEqual({ mode: 'stop', confirm: '' });
    document.querySelector('[data-testid=ha-accept-unrestorable] input').click();
    await settle();
    dlgBtn('Disable HA').click();
    await settle();
    expect(fx.calls[2][4]).toEqual({ mode: 'stop', confirm: '', acceptUnrestorable: true });
    expect(onDone).toHaveBeenCalledWith('stop');
    // The released list is refreshed after a KVM stop.
    expect(fx.calls.some((c) => c[0] === 'list')).toBe(true);
  });

  it('never sends accept for pause', async () => {
    await show(HaDisableDialog, { ref, defaultMode: 'pause' });
    dlgBtn('Disable HA').click();
    await settle();
    expect(fx.calls[0][4]).toEqual({ mode: 'pause', confirm: '' });
  });
});
