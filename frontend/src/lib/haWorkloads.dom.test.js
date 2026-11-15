import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  calls: [],
  list: [],
  pre: { ok: true, blockers: [] },
  getWl: null,
  enableErr: null,
  moveErr: null,
  bulk: null,
}));

vi.mock('$lib/stores/auth.svelte.js', () => ({
  auth: { isAdmin: () => true },
  api: {
    listHAWorkloads: () =>
      Promise.resolve({
        preconditions: fx.pre,
        summary: { managed: fx.list.length, failed: 0 },
        workloads: fx.list,
      }),
    getHAWorkload: async (k, n, p) => {
      fx.calls.push(['get', k, n, p]);
      if (fx.getWlGate) await fx.getWlGate;
      return Promise.resolve(
        fx.getWl || { managed: false, eligibility: { eligible: true, reasons: [] } }
      );
    },
    enableHAWorkload: (k, n, p, body) => {
      fx.calls.push(['enable', k, n, p, body]);
      return fx.enableErr
        ? Promise.reject(fx.enableErr)
        : Promise.resolve({ changed: true, commands: ['pcs resource create x'] });
    },
    disableHAWorkload: (k, n, p, o) => {
      fx.calls.push(['disable', k, n, p, o]);
      return Promise.resolve({ changed: true });
    },
    moveHAWorkload: (k, n, p, node) => {
      fx.calls.push(['move', k, n, p, node]);
      return fx.moveErr ? Promise.reject(fx.moveErr) : Promise.resolve({});
    },
    clearHAWorkloadFailcount: (k, n) => {
      fx.calls.push(['clear', k, n]);
      return Promise.resolve({ ok: true });
    },
    maintenanceHAWorkload: (k, n, p, a, ttl) => {
      fx.calls.push(['maint', k, n, a, ttl]);
      return Promise.resolve({});
    },
    bulkHAWorkloads: (b) => {
      fx.calls.push(['bulk', b]);
      return Promise.resolve(fx.bulk || { results: [] });
    },
    listReleasedVMs: () => Promise.resolve({ released: [], local_node: 'a' }),
  },
}));

import HaToggle from './components/HaToggle.svelte';
import HaWorkloadsPanel from './components/HaWorkloadsPanel.svelte';
import { clusterModule } from './stores/clusterModule.svelte.js';
import { resetHaWorkloads } from './stores/haWorkloads.svelte.js';
import { setLocale } from './i18n.svelte.js';

const settle = async (n = 6) => {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => setTimeout(r, 3));
    flushSync();
  }
};
const dlgBtn = (text) =>
  [...document.querySelectorAll('[role=dialog] button')].find((b) => b.textContent.trim() === text);
const btn = (text) =>
  [...document.querySelectorAll('button')].find((b) => b.textContent.trim() === text);

const wl = (over = {}) => ({
  key: 'kvm:web1',
  kind: 'kvm',
  name: 'web1',
  project: '',
  managed: true,
  state: 'started',
  node: 'a',
  since: '2026-11-12T10:00:00Z',
  failcount: 0,
  failures: [],
  allowed_nodes: [],
  eligibility: { eligible: true, reasons: [] },
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
    list: [],
    pre: { ok: true, blockers: [] },
    getWl: null,
    enableErr: null,
    moveErr: null,
    bulk: null,
  });
  clusterModule.enabled = true;
  resetHaWorkloads();
});
afterEach(() => {
  setLocale('en');
  if (mounted) {
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
  }
  document.body.innerHTML = '';
  resetHaWorkloads();
});

describe('HaToggle', () => {
  it('renders nothing when the module is off', async () => {
    clusterModule.enabled = false;
    const t = await show(HaToggle, { vm: { name: 'v', hypervisor: 'kvm' } });
    expect(t.querySelector('[role=switch]')).toBeNull();
  });

  it('does not offer the local Pacemaker toggle for a registered remote VM', async () => {
    clusterModule.enabled = true;
    const t = await show(HaToggle, {
      vm: { id: 'remote-id', name: 'remote-vm', hypervisor: 'kvm', node_id: 'remote-kvm' },
    });
    expect(t.querySelector('[role=switch]')).toBeNull();
  });

  it('shows eligibility reasons with how to fix, and does not call enable', async () => {
    fx.getWl = {
      eligibility: {
        eligible: false,
        reasons: [
          { code: 'disk_not_on_replicated_storage' },
          { code: 'brand_new', fix: 'backend fix' },
        ],
      },
    };
    const t = await show(HaToggle, { vm: { name: 'v', hypervisor: 'kvm' } });
    t.querySelector('[role=switch]').click();
    await settle();
    const text = document.body.textContent;
    expect(text).toContain('A disk is not on replicated storage');
    expect(text).toContain('How to fix');
    expect(text).toContain('Unrecognized reason: brand_new');
    expect(text).toContain('backend fix');
    dlgBtn('Turn on HA').click();
    await settle();
    expect(fx.calls.some((c) => c[0] === 'enable')).toBe(false);
  });

  it('explains a NIC network missing on some members, with copyable commands (es and ca too)', async () => {
    const writeText = vi.fn().mockResolvedValue();
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const cmds = [
      'incus network create ha-net --target a',
      'incus network create ha-net --target b',
      'incus network create ha-net',
    ];
    fx.getWl = {
      eligibility: {
        eligible: false,
        reasons: [
          {
            code: 'nic_network_not_on_all_members',
            detail: 'NIC eth0 uses parent bridge/interface vmbr0, which is missing on: b, c',
            commands: cmds,
          },
          { code: 'nic_network_not_on_all_members', detail: 'NIC eth1 ...', commands: cmds },
        ],
      },
    };
    const t = await show(HaToggle, { vm: { name: 'c1', hypervisor: 'incus', project: 'default' } });
    t.querySelector('[role=switch]').click();
    await settle();
    const text = document.body.textContent;
    expect(text).toContain('does not exist on every Incus member');
    expect(text).toContain('Use a cluster-wide managed network');
    expect(text).toContain('missing on: b, c');
    expect(document.querySelector('[data-testid=copy-commands-text]').textContent).toBe(
      cmds.join('\n')
    );
    btn('Copy commands').click();
    await settle(2);
    expect(writeText).toHaveBeenCalledWith(cmds.join('\n'));
    for (const [loc, phrase] of [
      ['es', 'no existe en todos los miembros de Incus'],
      ['ca', 'no existeix a tots els membres d’Incus'],
    ]) {
      unmount(mounted.app);
      mounted.target.remove();
      setLocale(loc);
      resetHaWorkloads();
      const t2 = await show(HaToggle, {
        vm: { name: 'c1', hypervisor: 'incus', project: 'default' },
      });
      t2.querySelector('[role=switch]').click();
      await settle();
      expect(document.body.textContent).toContain(phrase);
    }
  });

  it('disables Turn on HA with the reason when blockers exist', async () => {
    fx.getWl = {
      eligibility: { eligible: false, reasons: [{ code: 'disk_not_on_replicated_storage' }] },
    };
    const t = await show(HaToggle, { vm: { name: 'v', hypervisor: 'kvm' } });
    t.querySelector('[role=switch]').click();
    await settle();
    const confirm = dlgBtn('Turn on HA');
    expect(confirm.disabled).toBe(true);
    const reason = document.querySelector('[data-testid=confirm-disabled-reason]');
    expect(reason.textContent).toContain('cannot be turned on');
    expect(confirm.getAttribute('aria-describedby')).toBe(reason.id);
  });

  it('keeps Cancel enabled while eligibility is loading', async () => {
    let release;
    const gate = new Promise((r) => (release = r));
    fx.getWl = null;
    const t = await show(HaToggle, { vm: { name: 'v', hypervisor: 'kvm' } });
    const prev = fx.getWlGate;
    fx.getWlGate = gate;
    t.querySelector('[role=switch]').click();
    await settle(2);
    expect(document.body.textContent).toContain('Checking');
    const cancel = dlgBtn('Cancel');
    expect(cancel.disabled).toBe(false);
    expect(dlgBtn('Turn on HA').disabled).toBe(true);
    release();
    fx.getWlGate = prev;
    await settle();
  });

  it('enables an Incus instance with its project', async () => {
    const t = await show(HaToggle, { vm: { name: 'c1', hypervisor: 'incus', project: 'prod' } });
    t.querySelector('[role=switch]').click();
    await settle();
    expect(fx.calls[0]).toEqual(['get', 'incus', 'c1', 'prod']);
    dlgBtn('Turn on HA').click();
    await settle();
    expect(fx.calls).toContainEqual(['enable', 'incus', 'c1', 'prod', {}]);
  });

  it('shows translated blockers from a 409 precondition_failed', async () => {
    fx.enableErr = Object.assign(new Error('x'), {
      code: 'precondition_failed',
      status: 409,
      data: { blockers: ['fencing_test_required', 'mystery'], reasons: [] },
    });
    const t = await show(HaToggle, { vm: { name: 'v', hypervisor: 'kvm' } });
    t.querySelector('[role=switch]').click();
    await settle();
    dlgBtn('Turn on HA').click();
    await settle();
    const text = document.body.textContent;
    expect(text).toContain('Fencing has not been tested');
    expect(text).toContain('Unrecognized requirement: mystery');
    expect(text).toContain('HA cannot be enabled yet');
  });

  it('advanced dry run shows pcs commands without enabling', async () => {
    const t = await show(HaToggle, { vm: { name: 'v', hypervisor: 'kvm' }, advanced: true });
    t.querySelector('[role=switch]').click();
    await settle();
    btn('Preview pcs commands').click();
    await settle();
    expect(document.body.textContent).toContain('pcs resource create x');
    expect(fx.calls.find((c) => c[0] === 'enable')[4]).toEqual({ dry_run: true });
  });

  it('disable dialog defaults to pause and warns before releasing KVM storage', async () => {
    fx.list = [wl()];
    const t = await show(HaToggle, {
      vm: { name: 'web1', hypervisor: 'kvm', managed_by: 'pacemaker' },
    });
    t.querySelector('[role=switch]').click();
    await settle();
    expect(document.querySelector('input[value=pause]').checked).toBe(true);
    expect(document.querySelector('[data-testid=ha-kvm-release-warning]')).toBeNull();
    document.querySelector('input[value=stop]').click();
    await settle();
    const warn = document.querySelector('[data-testid=ha-kvm-release-warning]');
    expect(warn).not.toBeNull();
    // The VM leaves libvirt but WebKVM keeps it as a restorable entry.
    expect(warn.textContent).toContain('libvirt no longer has it');
    expect(warn.textContent).toContain('“Released” with a Restore button');
    expect(warn.textContent).not.toContain('restore it by hand');
    expect(warn.textContent).toContain('Pause HA');
    document.querySelector('input[value=forget]').click();
    await settle();
    expect(document.querySelector('[data-testid=ha-kvm-release-warning]')).not.toBeNull();
    document.querySelector('input[value=pause]').click();
    await settle();
    expect(document.querySelector('[data-testid=ha-kvm-release-warning]')).toBeNull();
  });

  it('no KVM release warning for Incus workloads', async () => {
    fx.list = [wl({ key: 'incus:default/c1', kind: 'incus', name: 'c1', project: 'default' })];
    const t = await show(HaToggle, {
      vm: { name: 'c1', hypervisor: 'incus', project: 'default', managed_by: 'pacemaker' },
    });
    t.querySelector('[role=switch]').click();
    await settle();
    document.querySelector('input[value=stop]').click();
    await settle();
    expect(document.querySelector('[data-testid=ha-kvm-release-warning]')).toBeNull();
  });

  it('turning off needs a mode; forget needs the typed key', async () => {
    fx.list = [wl()];
    const t = await show(HaToggle, {
      vm: { name: 'web1', hypervisor: 'kvm', managed_by: 'pacemaker' },
    });
    expect(t.querySelector('[role=switch]').getAttribute('aria-checked')).toBe('true');
    t.querySelector('[role=switch]').click();
    await settle();
    document.querySelector('input[value=forget]').click();
    await settle();
    dlgBtn('Disable HA').click();
    await settle();
    expect(fx.calls.some((c) => c[0] === 'disable')).toBe(false);
    const input = document.getElementById('ha-forget-confirm');
    input.value = 'kvm:web1';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    dlgBtn('Disable HA').click();
    await settle();
    expect(fx.calls).toContainEqual([
      'disable',
      'kvm',
      'web1',
      '',
      { mode: 'forget', confirm: 'kvm:web1' },
    ]);
  });
});

describe('HaWorkloadsPanel', () => {
  it('lists workloads with pacemaker badge, health and precondition blockers', async () => {
    fx.list = [
      wl(),
      wl({
        key: 'kvm:db',
        name: 'db',
        state: 'failed',
        failcount: 3,
        eligibility: { eligible: false, reasons: [{ code: 'vm_running' }] },
      }),
    ];
    fx.pre = { ok: false, blockers: ['quorum_missing'] };
    const t = await show(HaWorkloadsPanel, { nodeNames: ['a', 'b', 'c'] });
    expect(t.textContent).toContain('web1');
    expect(t.textContent).toContain('Pacemaker');
    expect(t.textContent).toContain('Failing');
    expect(t.textContent).toContain('The cluster has no quorum');
    expect(t.textContent).toContain('Not eligible');
  });

  // 8.26 minor: at 390 px the table scrolls inside its card and the row
  // actions were off-screen. The actions column stays pinned to the right.
  it('keeps the actions column pinned (sticky) so it is reachable on a phone', async () => {
    fx.list = [wl()];
    const t = await show(HaWorkloadsPanel, { nodeNames: ['a', 'b', 'c'] });
    const head = t.querySelector('[data-testid=ha-actions-head]');
    const cell = t.querySelector('[data-testid=ha-actions-cell]');
    for (const el of [head, cell]) {
      expect(el.className).toContain('sticky');
      expect(el.className).toContain('right-0');
      expect(el.className).toMatch(/\bbg-/);
    }
  });

  it('move excludes the current node and translates a 409', async () => {
    fx.list = [wl()];
    fx.moveErr = Object.assign(new Error('raw'), { code: 'operation_busy', status: 409 });
    await show(HaWorkloadsPanel, { nodeNames: ['a', 'b', 'c'] });
    btn('Move').click();
    await settle();
    const opts = [...document.querySelectorAll('#ha-move-node option')].map((o) => o.value);
    expect(opts).toEqual(['b', 'c']);
    dlgBtn('Move').click();
    await settle();
    expect(fx.calls).toContainEqual(['move', 'kvm', 'web1', '', 'b']);
    expect(document.body.textContent).toContain(
      'Another HA operation on this workload is in progress'
    );
  });

  it('maintenance begin sends the TTL and explains the safe-edit flow', async () => {
    fx.list = [wl()];
    await show(HaWorkloadsPanel, { nodeNames: ['a', 'b'] });
    btn('Start maintenance').click();
    await settle();
    expect(document.body.textContent).toContain('Safe-edit flow');
    dlgBtn('Start maintenance').click();
    await settle();
    expect(fx.calls).toContainEqual(['maint', 'kvm', 'web1', 'begin', 3600]);
  });

  it('bulk disable shows a per-item results table with translated errors', async () => {
    fx.list = [
      wl(),
      wl({ key: 'incus:default/c1', kind: 'incus', name: 'c1', project: 'default' }),
    ];
    fx.bulk = {
      results: [
        { key: 'kvm:web1', ok: true, changed: true },
        { key: 'incus:default/c1', ok: false, code: 'maintenance_active', error: 'raw' },
      ],
    };
    const t = await show(HaWorkloadsPanel, { nodeNames: ['a', 'b'] });
    t.querySelector('[aria-label="Select all workloads"]').click();
    await settle();
    expect(t.textContent).toContain('2 selected');
    [...t.querySelectorAll('[data-testid=ha-bulk-bar] button')]
      .find((b) => b.textContent.trim() === 'Disable HA')
      .click();
    await settle();
    dlgBtn('Run').click();
    await settle();
    const call = fx.calls.find((c) => c[0] === 'bulk')[1];
    expect(call.action).toBe('disable');
    expect(call.items).toHaveLength(2);
    expect(document.body.textContent).toContain('Done');
    expect(document.body.textContent).toContain('The workload is in maintenance');
  });

  it('blocks bulk above 50 items', async () => {
    fx.list = Array.from({ length: 51 }, (_, i) => wl({ key: `kvm:v${i}`, name: `v${i}` }));
    const t = await show(HaWorkloadsPanel, { nodeNames: [] });
    t.querySelector('[aria-label="Select all workloads"]').click();
    await settle();
    expect(t.textContent).toContain('at most 50');
    const b = [...t.querySelectorAll('[data-testid=ha-bulk-bar] button')].find(
      (x) => x.textContent.trim() === 'Enable HA'
    );
    expect(b.disabled).toBe(true);
  });
});
