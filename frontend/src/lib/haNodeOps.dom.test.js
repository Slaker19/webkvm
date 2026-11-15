import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  calls: [],
  standbyError: null,
  fenceError: null,
  fenceResult: null,
  evidence: [],
  evidenceError: false,
  evidenceCalls: 0,
}));

vi.mock('$lib/stores/auth.svelte.js', () => ({
  api: {
    haNodeStandby: (n) => {
      fx.calls.push(['standby', n]);
      return fx.standbyError
        ? Promise.reject(fx.standbyError)
        : Promise.resolve({ ok: true, node: n, state: 'standby' });
    },
    haNodeUnstandby: (n) => {
      fx.calls.push(['unstandby', n]);
      return Promise.resolve({ ok: true, node: n, state: 'online' });
    },
    haFencingEvidence: () => {
      fx.evidenceCalls++;
      if (fx.evidenceError) return Promise.reject(new Error('down'));
      const res = typeof fx.evidence === 'function' ? fx.evidence() : fx.evidence;
      return Promise.resolve(res);
    },
    haFencingTest: (b) => {
      fx.calls.push(['fence', b]);
      return fx.fenceError
        ? Promise.reject(fx.fenceError)
        : Promise.resolve(
            fx.fenceResult || {
              ok: true,
              node: b.node,
              fenced_at: '2026-11-12T10:00:00Z',
              history: [],
            }
          );
    },
  },
}));

import HaNodeOps from './components/HaNodeOps.svelte';

const settle = async (n = 6) => {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => setTimeout(r, 3));
    flushSync();
  }
};
const all = () => document.body;
const btn = (text) =>
  [...document.querySelectorAll('button')].find((b) =>
    (b.textContent || '').trim().startsWith(text)
  );
const setVal = (el, v) => {
  el.value = v;
  el.dispatchEvent(new Event('input', { bubbles: true }));
};
const choose = (el, v) => {
  el.value = v;
  el.dispatchEvent(new Event('change', { bubbles: true }));
};

const readiness = (extra = {}) => ({
  blockers: [],
  pacemaker: {
    node_names: ['a', 'b', 'c'],
    resources: [{ id: 'webkvm-ip', agent: 'x', role: 'Started', node: 'a' }],
  },
  control_plane: { node: 'a' },
  fencing: { tested_at: null },
  ...extra,
});

let mounted = null;
async function open(props) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(HaNodeOps, { target, props: { onChanged: vi.fn(), ...props } });
  mounted = { app, target };
  await settle();
  return target;
}
beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  fx.calls = [];
  fx.standbyError = null;
  fx.fenceError = null;
  fx.fenceResult = null;
  fx.evidence = [];
  fx.evidenceError = false;
  fx.evidenceCalls = 0;
});
afterEach(() => {
  vi.useRealTimers();
  if (mounted) {
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
  }
  document.body.innerHTML = '';
});

describe('standby / resume', () => {
  it('asks for confirmation explaining resources will move, then calls the API', async () => {
    const onChanged = vi.fn();
    await open({ readiness: readiness(), onChanged });
    document.querySelector('[aria-label="Put b in standby"]').click();
    await settle();
    expect(fx.calls).toEqual([]);
    expect(all().textContent).toContain('Resources running on this node will be moved');
    [...document.querySelectorAll('[role=dialog] button')]
      .find((b) => b.textContent.trim() === 'Standby')
      .click();
    await settle();
    expect(fx.calls).toEqual([['standby', 'b']]);
    expect(onChanged).toHaveBeenCalled();
  });

  it('translates 409 would_lose_quorum / last_online_node and falls back to the message', async () => {
    await open({ readiness: readiness() });
    for (const [code, text] of [
      ['would_lose_quorum', 'make the cluster lose quorum'],
      ['last_online_node', 'last online node'],
      ['mystery', 'raw backend message'],
    ]) {
      fx.standbyError = Object.assign(new Error('raw backend message'), { code, status: 409 });
      document.querySelector('[aria-label="Put c in standby"]').click();
      await settle();
      [...document.querySelectorAll('[role=dialog] button')]
        .find((b) => b.textContent.trim() === 'Standby')
        .click();
      await settle();
      expect(all().textContent).toContain(text);
    }
  });

  it('resumes a node', async () => {
    await open({ readiness: readiness() });
    document.querySelector('[aria-label="Resume b"]').click();
    await settle();
    [...document.querySelectorAll('[role=dialog] button')]
      .find((b) => b.textContent.trim() === 'Resume')
      .click();
    await settle();
    expect(fx.calls).toEqual([['unstandby', 'b']]);
  });
});

describe('fencing test', () => {
  it('excludes the control-plane node, shows last tested_at and gates on the typed text', async () => {
    await open({ readiness: readiness({ fencing: { tested_at: '2026-11-01T10:00:00Z' } }) });
    const sel = document.querySelector('#fence-node');
    expect([...sel.options].map((o) => o.value)).toEqual(['', 'b', 'c']);
    expect(document.querySelector('[data-testid=fence-last]').textContent).toContain(
      'Last successful test'
    );
    expect(all().textContent).toContain('powered off or rebooted');

    const run = () => btn('Fence this node now');
    expect(run().disabled).toBe(true);
    choose(sel, 'b');
    await settle(2);
    const input = document.querySelector('#fence-confirm');
    expect(all().textContent).toContain('Type FENCE b to confirm');
    setVal(input, 'FENCE c');
    await settle(2);
    expect(run().disabled).toBe(true);
    setVal(input, 'FENCE b');
    await settle(2);
    expect(run().disabled).toBe(false);
    run().click();
    await settle();
    expect(fx.calls).toEqual([['fence', { node: 'b', confirm: 'FENCE b' }]]);
    expect(all().textContent).toContain('Fencing test passed: b was fenced');
  });

  it('excludes the node reported by control_plane.node even when the resource runs elsewhere', async () => {
    await open({
      readiness: readiness({
        control_plane: { node: 'c' },
        pacemaker: {
          node_names: ['a', 'b', 'c'],
          resources: [{ id: 'webkvm-ip', agent: 'x', role: 'Started', node: 'a' }],
        },
      }),
    });
    const sel = document.querySelector('#fence-node');
    expect([...sel.options].map((o) => o.value)).toEqual(['', 'a', 'b']);
  });

  it('never tested message when tested_at is null', async () => {
    await open({ readiness: readiness() });
    expect(document.querySelector('[data-testid=fence-last]').textContent).toContain(
      'never been tested'
    );
  });

  it('only offers the control-plane node behind the advanced checkbox and sends the flag', async () => {
    await open({ readiness: readiness(), advanced: true });
    const cb = document.querySelector('input[type=checkbox]');
    cb.checked = true;
    cb.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(2);
    const sel = document.querySelector('#fence-node');
    expect([...sel.options].map((o) => o.value)).toContain('a');
    choose(sel, 'a');
    await settle(2);
    setVal(document.querySelector('#fence-confirm'), 'FENCE a');
    await settle(2);
    btn('Fence this node now').click();
    await settle();
    expect(fx.calls[0][1].allow_control_plane).toBe(true);
  });

  it('is disabled with a reason when readiness has blockers', async () => {
    await open({ readiness: readiness({ blockers: ['no_quorum'] }) });
    expect(all().textContent).toContain('Fix the readiness blockers above');
    expect(document.querySelector('#fence-node').disabled).toBe(true);
    expect(btn('Fence this node now').disabled).toBe(true);
  });

  it('translates 409 fencing blockers', async () => {
    fx.fenceError = Object.assign(new Error('x'), {
      code: 'quorum_missing',
      status: 409,
      data: {
        blockers: [
          'quorum_missing',
          'would_lose_quorum_after_fence',
          'confirm_required',
          'node_offline',
          'fencing_not_enabled',
          'brand_new',
        ],
      },
    });
    await open({ readiness: readiness() });
    choose(document.querySelector('#fence-node'), 'b');
    await settle(2);
    setVal(document.querySelector('#fence-confirm'), 'FENCE b');
    await settle(2);
    btn('Fence this node now').click();
    await settle();
    const txt = all().textContent;
    expect(txt).toContain('The cluster has no quorum.');
    expect(txt).toContain('would make the cluster lose quorum');
    expect(txt).toContain('The confirmation text does not match.');
    expect(txt).toContain('The node is not online.');
    expect(txt).toContain('Fencing is not enabled in the cluster.');
    expect(all().textContent).toContain('Refused: brand_new');
  });
});

describe('node_states and ok:false', () => {
  const withStates = () =>
    readiness({
      pacemaker: {
        node_names: ['a', 'b', 'c'],
        node_states: [
          { name: 'a', online: true, standby: false },
          { name: 'b', online: true, standby: true },
          { name: 'c', online: false, standby: false },
        ],
      },
    });

  it('shows Resume only for standby nodes and fences only online nodes', async () => {
    await open({ readiness: withStates() });
    expect(document.querySelector('[aria-label="Resume b"]')).toBeTruthy();
    expect(document.querySelector('[aria-label="Put b in standby"]')).toBeNull();
    expect(document.querySelector('[aria-label="Put a in standby"]')).toBeTruthy();
    expect(document.querySelector('[aria-label="Resume a"]')).toBeNull();
    const opts = [...document.querySelector('#fence-node').options].map((o) => o.value);
    expect(opts).toEqual(['', 'b']);
  });

  it('shows an error, not success, when the fencing command returned ok:false', async () => {
    fx.fenceResult = { ok: false, node: 'b', history: [] };
    await open({ readiness: readiness() });
    choose(document.querySelector('#fence-node'), 'b');
    await settle(2);
    setVal(document.querySelector('#fence-confirm'), 'FENCE b');
    await settle(2);
    btn('Fence this node now').click();
    await settle();
    expect(all().textContent).toContain('Fencing command failed');
    expect(all().textContent).not.toContain('Fencing test passed');
  });

  it('translates standby backend codes', async () => {
    fx.standbyError = Object.assign(new Error('raw'), {
      code: 'pacemaker_unavailable',
      status: 409,
    });
    await open({ readiness: readiness() });
    document.querySelector('[aria-label="Put b in standby"]').click();
    await settle();
    [...document.querySelectorAll('[role=dialog] button')]
      .find((b) => b.textContent.trim() === 'Standby')
      .click();
    await settle();
    expect(all().textContent).toContain('Pacemaker is not available on this node.');
  });
});

describe('watchdog_only mode: guided failure simulation', () => {
  const CMD = `sudo sh -c 'echo 1 > /proc/sys/kernel/sysrq; echo c > /proc/sysrq-trigger'`;
  const ev = (events, extra = {}) => ({
    mode: 'watchdog_only',
    tested: false,
    tested_at: null,
    events,
    recorded_by: null,
    simulate: { command_template: CMD },
    ...extra,
  });
  const wdReadiness = (extra = {}) =>
    readiness({
      fencing: { tested_at: null, mode: 'watchdog_only' },
      pacemaker: {
        node_names: ['a', 'b', 'c'],
        resources: [
          { id: 'webkvm-ip', agent: 'x', role: 'Started', node: 'a', active: true },
          { id: 'ha-vm1', agent: 'x', role: 'Started', node: 'b', active: true },
        ],
      },
      ...extra,
    });
  const tick = async (ms = 0) => {
    await vi.advanceTimersByTimeAsync(ms);
    flushSync();
  };
  // open() waits with real timers; fake-timer tests mount and tick instead.
  async function openFake(props) {
    const target = document.createElement('div');
    document.body.appendChild(target);
    const app = mount(HaNodeOps, { target, props: { onChanged: vi.fn(), ...props } });
    mounted = { app, target };
    await tick();
    return target;
  }
  const reboot = (target, completed) => ({
    target,
    action: 'reboot',
    status: 'success',
    completed,
    delegate: 'a',
  });

  it('explains both modes, hides the typed-confirmation direct test and offers the simulation', async () => {
    fx.evidence = ev([]);
    await open({ readiness: wdReadiness() });
    const txt = all().textContent;
    expect(txt).toContain('Fence device (Proxmox, IPMI');
    expect(txt).toContain('Watchdog only (SBD without a fence device)');
    expect(txt).toContain('NOT reboot it');
    expect(document.querySelector('[data-testid=fence-mode-current]').textContent).toContain(
      'the watchdog only'
    );
    expect(document.querySelector('#fence-confirm')).toBeNull();
    expect(btn('Fence this node now')).toBeUndefined();
    const sel = document.querySelector('#sim-node');
    // Control-plane node a is never offered; the node without HA resources comes first.
    expect([...sel.options].map((o) => o.value)).toEqual(['', 'c', 'b']);
    expect(sel.options[1].textContent).toContain('no HA resources');
    expect(sel.options[2].textContent).toContain('runs 1 HA resources');
  });

  it('keeps the direct test only for device mode and says nothing is configured for none', async () => {
    await open({ readiness: readiness({ fencing: { tested_at: null, mode: 'device' } }) });
    expect(document.querySelector('#fence-confirm')).toBeTruthy();
    expect(document.querySelector('#sim-node')).toBeNull();
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
    await open({ readiness: readiness({ fencing: { tested_at: null, mode: 'none' } }) });
    expect(all().textContent).toContain('No fencing is configured');
    expect(document.querySelector('#fence-confirm')).toBeNull();
    expect(document.querySelector('#sim-node')).toBeNull();
  });

  it('shows what will happen and the copyable command with the node name', async () => {
    fx.evidence = ev([]);
    const writeText = vi.fn().mockResolvedValue();
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    await open({ readiness: wdReadiness() });
    expect(document.querySelector('[data-testid=sim-warning]')).toBeNull();
    choose(document.querySelector('#sim-node'), 'c');
    await settle(2);
    const warn = document.querySelector('[data-testid=sim-warning]').textContent;
    expect(warn).toContain('c will crash and reboot by its watchdog in about 20 to 30 seconds');
    expect(warn).toContain('test window');
    expect(all().textContent).toContain('Run this command on c');
    expect(document.querySelector('[data-testid=copy-commands-text]').textContent).toBe(CMD);
    document.querySelector('[aria-label="Copy the crash command for c"]').click();
    await settle(2);
    expect(writeText).toHaveBeenCalledWith(CMD);
    expect(fx.calls).toEqual([]); // nothing is fenced by the UI
  });

  it('polls every 5 s, turns green with the fence event timestamp and stops polling', async () => {
    vi.useFakeTimers();
    const old = reboot('b', '2026-11-12 09:00:00 +00:00');
    fx.evidence = ev([old]);
    const onChanged = vi.fn();
    await openFake({ readiness: wdReadiness(), onChanged });
    await tick();
    choose(document.querySelector('#sim-node'), 'c');
    await tick();
    btn('I ran it').click();
    await tick();
    expect(document.querySelector('[data-testid=sim-waiting]').textContent).toContain(
      'Waiting for evidence'
    );
    const before = fx.evidenceCalls;
    await tick(5000);
    expect(fx.evidenceCalls).toBe(before + 1);
    // An old event of another node is not proof of this test.
    expect(document.querySelector('[data-testid=sim-proven]')).toBeNull();
    const fresh = reboot('c', '2026-11-12 10:11:12 +00:00');
    fx.evidence = ev([fresh, old], { tested: true });
    await tick(5000);
    const proven = document.querySelector('[data-testid=sim-proven]');
    expect(proven.textContent).toContain('Pacemaker fenced c (reboot)');
    expect(proven.textContent).toMatch(/2026|11\/12|12\/11/);
    expect(onChanged).toHaveBeenCalled();
    const after = fx.evidenceCalls;
    await tick(30000);
    expect(fx.evidenceCalls).toBe(after);
  });

  it('keeps waiting through read errors, then gives up after 10 minutes', async () => {
    vi.useFakeTimers();
    fx.evidence = ev([]);
    await openFake({ readiness: wdReadiness() });
    await tick();
    choose(document.querySelector('#sim-node'), 'c');
    await tick();
    btn('I ran it').click();
    await tick();
    fx.evidenceError = true;
    await tick(5000);
    expect(all().textContent).toContain('Could not read the fencing history');
    expect(document.querySelector('[data-testid=sim-waiting]')).toBeTruthy();
    await tick(10 * 60 * 1000 + 5000);
    expect(document.querySelector('[data-testid=sim-timeout]').textContent).toContain(
      'No fence event appeared after 10 minutes'
    );
    expect(document.querySelector('[data-testid=sim-waiting]')).toBeNull();
    const calls = fx.evidenceCalls;
    await tick(60000);
    expect(fx.evidenceCalls).toBe(calls);
    expect(btn('Wait again')).toBeTruthy();
  });

  it('stops polling when unmounted and when cancelled', async () => {
    vi.useFakeTimers();
    fx.evidence = ev([]);
    await openFake({ readiness: wdReadiness() });
    await tick();
    choose(document.querySelector('#sim-node'), 'c');
    await tick();
    btn('I ran it').click();
    await tick();
    btn('Stop waiting').click();
    await tick();
    const afterCancel = fx.evidenceCalls;
    await tick(20000);
    expect(fx.evidenceCalls).toBe(afterCancel);
    btn('I ran it').click();
    await tick();
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
    const afterUnmount = fx.evidenceCalls;
    await tick(30000);
    expect(fx.evidenceCalls).toBe(afterUnmount);
  });

  it('shows the already-proven state from a real event', async () => {
    fx.evidence = ev([reboot('b', '2026-11-12 09:00:00 +00:00')], {
      tested: true,
      tested_at: '2026-11-12T09:00:00Z',
      recorded_by: 'event',
    });
    await open({ readiness: wdReadiness() });
    expect(document.querySelector('[data-testid=sim-already]').textContent).toContain(
      'already proven by a real event'
    );
    expect(document.querySelector('[data-testid=sim-events]').textContent).toContain('b · reboot');
  });

  it('explains watchdog_only_unreliable when a device-mode UI meets a watchdog-only backend', async () => {
    fx.fenceError = Object.assign(new Error('x'), {
      code: 'watchdog_only_unreliable',
      status: 409,
      data: {
        blockers: ['watchdog_only_unreliable'],
        explanation: 'self_fence_assumed',
        node: 'b',
        simulate: { node: 'b', command: CMD },
      },
    });
    await open({ readiness: readiness({ fencing: { tested_at: null, mode: 'device' } }) });
    choose(document.querySelector('#fence-node'), 'b');
    await settle(2);
    setVal(document.querySelector('#fence-confirm'), 'FENCE b');
    await settle(2);
    btn('Fence this node now').click();
    await settle();
    const txt = all().textContent;
    expect(txt).toContain('does not reboot it, so it proves nothing');
    expect(document.querySelector('[data-testid=fence-unreliable]').textContent).toContain(
      'would only assume the node fenced itself'
    );
    expect(document.querySelector('[data-testid=copy-commands-text]').textContent).toBe(CMD);
  });
});
