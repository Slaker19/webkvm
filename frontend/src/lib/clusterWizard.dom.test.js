import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  registered: [],
  plan: null,
  planCalls: [],
  setupCalls: [],
  setupError: null,
  statusQueue: [],
  statusCalls: 0,
  statusInFlight: 0,
  maxInFlight: 0,
  statusFail: false,
  readiness: null,
  readinessGate: null,
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = {
    listNodes: () => Promise.resolve({ nodes: fx.registered }),
    getHAReadiness: () =>
      fx.readinessGate
        ? fx.readinessGate.then(() => fx.readiness)
        : fx.readiness
          ? Promise.resolve(fx.readiness)
          : Promise.reject(new Error('404')),
    haClusterPlan: (b) => {
      fx.planCalls.push(b);
      return Promise.resolve(fx.plan);
    },
    haClusterSetup: (b) => {
      fx.setupCalls.push(b);
      return fx.setupError
        ? Promise.reject(fx.setupError)
        : Promise.resolve({ state: 'running', steps: [{ id: 'preflight', state: 'running' }] });
    },
    haClusterStatus: async () => {
      fx.statusCalls++;
      fx.statusInFlight++;
      fx.maxInFlight = Math.max(fx.maxInFlight, fx.statusInFlight);
      await new Promise((r) => setTimeout(r, 2));
      fx.statusInFlight--;
      if (fx.statusFail) throw new Error('boom');
      return fx.statusQueue.length > 1
        ? fx.statusQueue.shift()
        : fx.statusQueue[0] || { state: 'idle', steps: [] };
    },
  };
  return { api, auth: { isAdmin: () => true } };
});

import ClusterWizard from './components/ClusterWizard.svelte';

const settle = async (n = 8) => {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => setTimeout(r, 3));
    flushSync();
  }
};
const btn = (root, text) =>
  [...root.querySelectorAll('button')].find((b) => (b.textContent || '').trim().startsWith(text));
const setVal = (el, v) => {
  el.value = v;
  el.dispatchEvent(new Event('input', { bubbles: true }));
};
const check = (el, v = true) => {
  el.checked = v;
  el.dispatchEvent(new Event('change', { bubbles: true }));
};
const byLabel = (root, text) => root.querySelector(`[aria-label="${text}"]`);

const goodPlan = () => ({
  ok: true,
  blockers: [],
  warnings: [],
  checks: [{ id: 'packages', ok: true, severity: 'info', detail: 'all present' }],
  watchdog: {
    present: true,
    devices: ['/dev/watchdog'],
    module: 'softdog',
    type: 'softdog',
    recommended: '/dev/watchdog',
  },
  virt: { kind: 'kvm', is_vm: true },
  fencing_hint: 'weird_hint',
  commands: ['pcs cluster setup lab a b c'],
  manual_steps: ['Open corosync ports'],
  nodes_needed: 3,
});

let mounted = null;
async function open(props = {}) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(ClusterWizard, { target, props: { pollMs: 5, ...props } });
  mounted = { app, target };
  await settle();
  return target;
}

async function fill(target, nodes = 3) {
  setVal(target.querySelector('#cw-cluster-name'), 'lab');
  for (let i = 1; i < nodes; i++) {
    btn(target, 'Add node').click();
    flushSync();
  }
  for (let i = 0; i < nodes; i++) {
    setVal(byLabel(target, `Node name, row ${i + 1}`), `n${i + 1}`);
    setVal(byLabel(target, `IP address, row ${i + 1}`), `10.0.0.${i + 1}`);
  }
  flushSync();
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('webkvm.locale', 'en');
  fx.registered = [];
  fx.plan = goodPlan();
  fx.planCalls = [];
  fx.setupCalls = [];
  fx.setupError = null;
  fx.statusQueue = [];
  fx.statusCalls = 0;
  fx.statusInFlight = 0;
  fx.maxInFlight = 0;
  fx.statusFail = false;
  fx.readiness = null;
  fx.readinessGate = null;
});
afterEach(() => {
  if (mounted) {
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
  }
  document.body.innerHTML = '';
});

describe('ClusterWizard easy vs advanced', () => {
  it('easy shows name, nodes and recommended settings only', async () => {
    const t = await open();
    expect(t.querySelector('#cw-cluster-name')).toBeTruthy();
    expect(t.textContent).toContain('Use recommended settings');
    expect(btn(t, 'Review plan')).toBeTruthy();
    expect(btn(t, 'Next: fencing')).toBeFalsy();
    expect(t.textContent).not.toContain('Allow two nodes');
  });

  it('advanced remembers the mode and shows the fencing step', async () => {
    const t = await open();
    btn(t, 'Advanced').click();
    await settle(2);
    expect(localStorage.getItem('webkvm.clusterWizard.mode')).toBe('advanced');
    expect(btn(t, 'Next: fencing')).toBeTruthy();
    expect(t.textContent).not.toContain('Use recommended settings');
  });

  it('survives localStorage failures', async () => {
    const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied');
    });
    const t = await open();
    spy.mockRestore();
    expect(btn(t, 'Review plan')).toBeTruthy();
  });

  it('prefills rows from registered nodes', async () => {
    fx.registered = [{ name: 'alpha', uri: 'qemu+ssh://root@10.0.0.9/system' }];
    const t = await open();
    expect(byLabel(t, 'Node name, row 1').value).toBe('alpha');
    expect(byLabel(t, 'IP address, row 1').value).toBe('10.0.0.9');
  });
});

describe('two-node gating', () => {
  it('blocks two nodes in easy and requires the explicit checkbox in advanced', async () => {
    const t = await open();
    await fill(t, 2);
    expect(t.textContent).toContain('Quorum needs at least three nodes');
    btn(t, 'Review plan').click();
    await settle();
    expect(fx.planCalls.length).toBe(0);

    btn(t, 'Advanced').click();
    await settle(2);
    btn(t, 'Next: fencing').click();
    await settle();
    expect(fx.planCalls.length).toBe(0);
    const box = [...t.querySelectorAll('input[type=checkbox]')].find((i) =>
      i.closest('label')?.textContent.includes('Allow two nodes')
    );
    check(box);
    await settle(2);
    btn(t, 'Next: fencing').click();
    await settle();
    expect(fx.planCalls.length).toBe(1);
    expect(fx.planCalls[0].allow_two_nodes).toBe(true);
  });

  it('shows per-row validation errors', async () => {
    const t = await open();
    setVal(t.querySelector('#cw-cluster-name'), 'lab');
    setVal(byLabel(t, 'IP address, row 1'), 'nope');
    btn(t, 'Review plan').click();
    await settle(2);
    expect(t.textContent).toContain('Enter a valid IPv4 or IPv6 address.');
    expect(fx.planCalls.length).toBe(0);
  });
});

describe('plan review', () => {
  it('translates blockers and warnings, with an unknown-code fallback', async () => {
    fx.plan = {
      ...goodPlan(),
      ok: false,
      blockers: ['watchdog_missing', 'cluster_cidr_mismatch', 'brand_new_blocker'],
      warnings: ['softdog_no_hardware_protection', 'future_warning'],
    };
    const t = await open();
    await fill(t, 3);
    btn(t, 'Review plan').click();
    await settle();
    const text = t.textContent;
    expect(text).toContain('No watchdog device is available for SBD.');
    expect(text).toContain('outside the configured cluster network');
    expect(text).toContain('Blocked: brand_new_blocker');
    expect(text).toContain('softdog gives no protection if the kernel hangs.');
    expect(text).toContain('Warning: future_warning');
    expect(btn(t, 'Continue').disabled).toBe(true);
  });

  it('shows a red notice when a cluster already exists and blocks continuing', async () => {
    fx.plan = { ...goodPlan(), ok: false, blockers: ['cluster_exists'] };
    const t = await open();
    await fill(t, 3);
    btn(t, 'Review plan').click();
    await settle();
    const alert = [...t.querySelectorAll('[role=alert]')].find((a) =>
      a.textContent.includes('will not touch it')
    );
    expect(alert).toBeTruthy();
    expect(alert.className).toContain('text-destructive');
    expect(btn(t, 'Continue').disabled).toBe(true);
  });

  it('lists exact commands and per-node manual steps, never asking for the password', async () => {
    const t = await open();
    await fill(t, 3);
    btn(t, 'Review plan').click();
    await settle();
    expect(t.querySelector('[data-testid=plan-commands]').textContent).toBe(
      'pcs cluster setup lab a b c'
    );
    expect(t.textContent).toContain('Open corosync ports');
    expect(t.textContent).toContain('On n1 (10.0.0.1): set the hacluster password');
    expect(t.textContent).toContain('On n3 (10.0.0.3): set the hacluster password');
    expect(t.textContent).toContain('sudo pcs host auth n1 n2 n3 -u hacluster');
    expect(t.querySelector('input[type=password]')).toBeNull();
    expect(
      [...t.querySelectorAll('input')].some((i) => /password|secret/i.test(i.name + i.id))
    ).toBe(false);
  });

  it('advanced step 2 explains softdog and the VM hint', async () => {
    const t = await open();
    btn(t, 'Advanced').click();
    await settle(2);
    await fill(t, 3);
    btn(t, 'Next: fencing').click();
    await settle();
    expect(t.querySelector('[data-testid=wd-type]').textContent).toContain('softdog');
    expect(t.textContent).toContain('softdog does not protect a hung kernel');
    expect(t.textContent).toContain('virtual machine (kvm)');
    expect(t.textContent).toContain('Fencing suggestion: weird_hint');
    setVal(t.querySelector('#cw-wd-dev'), '/dev/watchdog1');
    setVal(t.querySelector('#cw-sbd-timeout'), '25');
    btn(t, 'Review plan').click();
    await settle();
    expect(fx.planCalls.at(-1).sbd).toEqual({
      watchdog_device: '/dev/watchdog1',
      timeout_seconds: 25,
    });
  });
});

async function toRun(t) {
  await fill(t, 3);
  btn(t, 'Review plan').click();
  await settle();
  btn(t, 'Continue').click();
  await settle(2);
}

describe('setup run and polling', () => {
  it('requires the confirm checkbox and the typed cluster name', async () => {
    const t = await open();
    await toRun(t);
    const create = () => btn(t, 'Create cluster');
    expect(create().disabled).toBe(true);
    const box = [...t.querySelectorAll('input[type=checkbox]')].find((i) =>
      i.closest('label')?.textContent.includes('I completed the manual steps')
    );
    check(box);
    await settle(2);
    expect(create().disabled).toBe(true);
    setVal(t.querySelector('#cw-type-name'), 'wrong');
    await settle(2);
    expect(create().disabled).toBe(true);
    setVal(t.querySelector('#cw-type-name'), 'lab');
    await settle(2);
    expect(create().disabled).toBe(false);
    create().click();
    await settle();
    expect(fx.setupCalls.length).toBe(1);
    expect(fx.setupCalls[0].confirm).toBe(true);
    expect(fx.setupCalls[0].cluster_name).toBe('lab');
    expect(JSON.stringify(fx.setupCalls[0])).not.toMatch(/password/i);
  });

  async function startRun(t, queue) {
    await toRun(t);
    const box = [...t.querySelectorAll('input[type=checkbox]')].find((i) =>
      i.closest('label')?.textContent.includes('I completed the manual steps')
    );
    check(box);
    setVal(t.querySelector('#cw-type-name'), 'lab');
    await settle(2);
    if (queue) fx.statusQueue = queue;
    btn(t, 'Create cluster').click();
    await settle();
  }

  it('polls without overlapping requests, shows per-step sr-only status and finishes', async () => {
    const running = {
      state: 'running',
      steps: [
        { id: 'preflight', state: 'done' },
        { id: 'sbd_config', state: 'running' },
      ],
    };
    const onCheckReadiness = vi.fn();
    const t = await open({ onCheckReadiness });
    await startRun(t, [
      running,
      running,
      {
        state: 'done',
        steps: [
          { id: 'preflight', state: 'done' },
          { id: 'verify', state: 'done' },
        ],
      },
    ]);
    await settle(20);
    expect(fx.maxInFlight).toBe(1);
    const sr = [...t.querySelectorAll('[data-testid=setup-steps] .sr-only')].map(
      (e) => e.textContent
    );
    expect(sr).toContain('done');
    expect(t.textContent).toContain('The cluster was created');
    btn(t, 'Check HA readiness').click();
    expect(onCheckReadiness).toHaveBeenCalled();
  });

  it('shows the failed step, the error and copyable remaining commands', async () => {
    const failed = [
      {
        state: 'failed',
        steps: [{ id: 'cluster_setup', state: 'failed', detail: 'x' }],
        failed_step: 'cluster_setup',
        error: 'pcs exploded',
        commands_remaining: ['pcs cluster start --all'],
      },
    ];
    const t = await open();
    await startRun(t, failed);
    await settle(6);
    expect(t.textContent).toContain('Failed step: Create the cluster');
    expect(t.textContent).toContain('pcs exploded');
    expect(t.querySelector('[data-testid=commands-remaining]').textContent).toBe(
      'pcs cluster start --all'
    );
    expect(t.querySelector('[aria-label="Copy remaining commands"]')).toBeTruthy();
  });

  it('translates 409 setup errors', async () => {
    fx.setupError = Object.assign(new Error('x'), { code: 'cluster_exists', status: 409 });
    const t = await open();
    await toRun(t);
    const box = [...t.querySelectorAll('input[type=checkbox]')].find((i) =>
      i.closest('label')?.textContent.includes('I completed the manual steps')
    );
    check(box);
    setVal(t.querySelector('#cw-type-name'), 'lab');
    await settle(2);
    btn(t, 'Create cluster').click();
    await settle();
    expect(t.textContent).toContain('A cluster already exists on this node; nothing was changed.');
  });

  it('stops polling after 5 consecutive errors and offers Retry', async () => {
    const t = await open();
    await startRun(t, [{ state: 'running', steps: [] }]);
    fx.statusFail = true;
    await settle(40);
    expect(t.textContent).toContain('Lost contact with the server');
    const calls = fx.statusCalls;
    await settle(10);
    expect(fx.statusCalls).toBe(calls);
    fx.statusFail = false;
    btn(t, 'Retry').click();
    await settle(4);
    expect(fx.statusCalls).toBeGreaterThan(calls);
    expect(t.textContent).not.toContain('Lost contact with the server');
  });

  it('resumes the progress view on mount when a setup is already running', async () => {
    fx.statusQueue = [
      { state: 'running', steps: [{ id: 'cluster_start', state: 'running' }] },
      { state: 'done', steps: [{ id: 'cluster_start', state: 'done' }] },
    ];
    const t = await open();
    await settle(10);
    expect(t.querySelector('[data-testid=setup-steps]')).toBeTruthy();
    expect(t.querySelector('#cw-cluster-name')).toBeNull();
    expect(t.textContent).toContain('The cluster was created');
    expect(fx.setupCalls.length).toBe(0);
  });
});

describe('host_auth_missing checklist and re-check', () => {
  it('renders manual steps as a checklist with copy buttons and re-calls /plan on Re-check', async () => {
    fx.plan = {
      ...goodPlan(),
      ok: false,
      blockers: ['host_auth_missing'],
      manual_steps: ['sudo passwd hacluster', 'sudo systemctl enable --now pcsd'],
    };
    const t = await open();
    await fill(t, 3);
    btn(t, 'Review plan').click();
    await settle();
    expect(t.textContent).toContain('Some nodes are not authenticated yet');
    const items = t.querySelectorAll('[data-testid=manual-steps] li');
    expect(items.length).toBe(2);
    expect(t.querySelector('[aria-label="Copy manual step 2"]')).toBeTruthy();
    expect(btn(t, 'Continue').disabled).toBe(true);
    const before = fx.planCalls.length;
    fx.plan = { ...goodPlan(), manual_steps: [] };
    btn(t, 'Re-check plan').click();
    await settle();
    expect(fx.planCalls.length).toBe(before + 1);
    expect(t.textContent).not.toContain('Some nodes are not authenticated yet');
    expect(btn(t, 'Continue').disabled).toBe(false);
  });

  it('translates the new blockers, warnings and fencing hints', async () => {
    fx.plan = {
      ...goodPlan(),
      ok: false,
      blockers: ['invalid_sbd_timeout', 'local_node_not_in_list', 'host_auth_unknown'],
      warnings: ['sbd_disabled_no_automatic_failover'],
    };
    const t = await open();
    await fill(t, 3);
    btn(t, 'Review plan').click();
    await settle();
    const txt = t.textContent;
    expect(txt).toContain('between 5 and 120 seconds');
    expect(txt).toContain('This node must be one of the listed nodes');
    expect(txt).toContain('Could not read the pcs authentication state');
    expect(txt).toContain('no automatic failover');
  });
});

describe('skipped step and interrupted run', () => {
  async function run(t, queue) {
    await fill(t, 3);
    btn(t, 'Review plan').click();
    await settle();
    btn(t, 'Continue').click();
    await settle(2);
    const box = [...t.querySelectorAll('input[type=checkbox]')].find((i) =>
      i.closest('label')?.textContent.includes('I completed the manual steps')
    );
    check(box);
    setVal(t.querySelector('#cw-type-name'), 'lab');
    await settle(2);
    fx.statusQueue = queue;
    btn(t, 'Create cluster').click();
    await settle(8);
  }

  it('renders a skipped step as skipped, not failed', async () => {
    const t = await open();
    await run(t, [
      {
        state: 'done',
        steps: [
          { id: 'preflight', state: 'done' },
          { id: 'sbd_config', state: 'skipped' },
        ],
      },
    ]);
    const li = [...t.querySelectorAll('[data-testid=setup-steps] li')][1];
    expect(li.textContent).toContain('skipped');
    expect(li.textContent).not.toContain('failed');
    expect(t.textContent).not.toContain('Cluster setup failed');
  });

  it('offers re-plan after an interrupted run', async () => {
    const t = await open();
    await run(t, [{ state: 'failed', steps: [], error: 'interrupted', commands_remaining: ['x'] }]);
    expect(t.textContent).toContain('interrupted');
    const before = fx.planCalls.length;
    btn(t, 'Re-plan').click();
    await settle();
    expect(fx.planCalls.length).toBe(before + 1);
    expect(t.querySelector('[data-testid=plan-commands]')).toBeTruthy();
  });
});

describe('leaving the wizard', () => {
  const inertWarnings = (spy) =>
    spy.mock.calls.filter((c) => c.some((a) => String(a).includes('derived_inert')));

  it('closing via Close does not read deriveds after unmount', async () => {
    const warn = vi.spyOn(console, 'warn');
    const target = document.createElement('div');
    document.body.appendChild(target);
    let app = null;
    const onclose = () => {
      unmount(app);
      target.remove();
    };
    app = mount(ClusterWizard, { target, props: { pollMs: 5, onclose } });
    await settle();
    setVal(target.querySelector('#cw-cluster-name'), 'lab');
    flushSync();
    btn(target, 'Close').click();
    await settle();
    expect(inertWarnings(warn)).toEqual([]);
    warn.mockRestore();
  });

  it('unmounting while the mount requests are in flight is quiet', async () => {
    const warn = vi.spyOn(console, 'warn');
    fx.registered = [{ name: 'alpha', uri: 'qemu+ssh://root@10.0.0.9/system' }];
    const target = document.createElement('div');
    document.body.appendChild(target);
    const app = mount(ClusterWizard, { target, props: { pollMs: 5 } });
    flushSync();
    unmount(app);
    target.remove();
    await settle();
    expect(inertWarnings(warn)).toEqual([]);
    warn.mockRestore();
  });

  it('cancelling from the review step is quiet', async () => {
    const warn = vi.spyOn(console, 'warn');
    const target = document.createElement('div');
    document.body.appendChild(target);
    let app = null;
    app = mount(ClusterWizard, {
      target,
      props: { pollMs: 5, onclose: () => unmount(app) },
    });
    await settle();
    await fill(target, 3);
    btn(target, 'Review plan').click();
    await settle();
    btn(target, 'Close').click();
    await settle();
    target.remove();
    expect(inertWarnings(warn)).toEqual([]);
    warn.mockRestore();
  });
});

describe('prefill and an existing cluster', () => {
  it('prefills the local node by name (never the browser host), once, without a self duplicate', async () => {
    fx.registered = [
      { id: 'local', type: 'local', name: 'node-b', uri: 'qemu:///system' },
      {
        id: 'n1',
        type: 'remote',
        name: 'node-b',
        is_self: true,
        uri: 'qemu+ssh://operator@10.20.30.11/system',
      },
      { id: 'n2', type: 'remote', name: 'node-a', uri: 'qemu+ssh://u@10.20.30.10/system' },
    ];
    const t = await open();
    expect(byLabel(t, 'Node name, row 1').value).toBe('node-b');
    expect(byLabel(t, 'IP address, row 1').value).toBe('10.20.30.11');
    expect(byLabel(t, 'Node name, row 2').value).toBe('node-a');
    expect(byLabel(t, 'Node name, row 3')).toBeFalsy();
  });

  it('shows the running cluster with its members instead of offering to create one', async () => {
    fx.registered = [{ id: 'local', type: 'local', name: 'node-b', uri: 'qemu:///system' }];
    fx.readiness = {
      pacemaker: {
        state: 'observed',
        cluster_name: 'lab',
        node_names: ['node-a', 'node-b', 'node-c'],
      },
    };
    const t = await open();
    const panel = t.querySelector('[data-testid=cw-existing-cluster]');
    expect(panel).toBeTruthy();
    expect(panel.textContent).toContain('node-c');
    expect(panel.textContent).toContain('Add a node');
    expect(btn(t, 'Review plan')).toBeFalsy();
    expect(t.querySelector('#cw-cluster-name')).toBeFalsy();
    expect(panel.querySelector('a[href="#/nodes"]')).toBeTruthy();
  });
});

// 8.26 defect 4: no "Create the cluster" form flash while /api/ha/readiness
// is still answering (it can take seconds): a loading skeleton instead.
describe('ClusterWizard loading', () => {
  it('shows a skeleton, not the create form, until readiness answers', async () => {
    let release;
    fx.readinessGate = new Promise((r) => (release = r));
    fx.readiness = {
      pacemaker: { state: 'observed', cluster_name: 'lab', node_names: ['a', 'b'] },
    };
    const t = await open();
    expect(t.querySelector('[data-testid=cw-loading]')).toBeTruthy();
    expect(t.querySelector('#cw-cluster-name')).toBeFalsy();
    expect(t.querySelector('[data-testid=cw-existing-cluster]')).toBeFalsy();
    release();
    await settle();
    expect(t.querySelector('[data-testid=cw-loading]')).toBeFalsy();
    expect(t.querySelector('[data-testid=cw-existing-cluster]')).toBeTruthy();
  });

  it('falls back to the create form when readiness fails', async () => {
    const t = await open();
    expect(t.querySelector('[data-testid=cw-loading]')).toBeFalsy();
    expect(t.querySelector('#cw-cluster-name')).toBeTruthy();
  });
});
