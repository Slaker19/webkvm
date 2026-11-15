import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';
import HaDrbdRepair from './components/HaDrbdRepair.svelte';
import { setLocale } from './i18n.svelte.js';

const RES = 'incus-volume-26439be54d4d4485b77c2eaa9dbb0417';

const primarySide = {
  resource: RES,
  local_role: 'Primary',
  local_disk: 'UpToDate',
  local_node: 'node-a',
  peer: 'node-b',
  peer_connection: 'StandAlone',
  peer_disk: 'DUnknown',
  replication: 'Off',
  kind: 'standalone',
  severity: 'critical',
  safe_to_discard_local: false,
  manual_resolution: false,
  repair_commands: [],
  repair_steps: [
    {
      node: 'node-b',
      command: `sudo drbdadm disconnect ${RES}:node-a`,
      verify_peer_stale: true,
    },
    {
      node: 'node-b',
      command: `sudo drbdadm connect --discard-my-data ${RES}:node-a`,
      verify_peer_stale: true,
    },
    { node: 'node-a', command: `sudo drbdadm connect ${RES}:node-b` },
  ],
};

const staleSide = {
  ...primarySide,
  local_role: 'Secondary',
  local_disk: 'Inconsistent',
  local_node: 'node-b',
  peer: 'node-a',
  peer_connection: 'Connecting',
  kind: 'inconsistent',
  severity: 'warning',
  safe_to_discard_local: true,
  repair_steps: [
    { node: 'node-b', command: `sudo drbdadm disconnect ${RES}:node-a` },
    { node: 'node-b', command: `sudo drbdadm connect --discard-my-data ${RES}:node-a` },
    { node: 'node-a', command: `sudo drbdadm connect ${RES}:node-b` },
  ],
};

const splitBrain = {
  ...primarySide,
  local_role: 'Secondary',
  local_node: 'nodea',
  peer: 'nodeb',
  peer_disk: 'UpToDate',
  kind: 'split_brain_suspected',
  safe_to_discard_local: false,
  manual_resolution: true,
  repair_steps: [{ node: 'nodea', command: `sudo drbdadm connect ${RES}:nodeb` }],
};

let app = null;
let target = null;
function render(props) {
  target = document.createElement('div');
  document.body.appendChild(target);
  app = mount(HaDrbdRepair, { target, props });
  flushSync();
  return target;
}
const cmdBlocks = () =>
  [...document.querySelectorAll('[data-testid="copy-commands-text"]')].map((e) => e.textContent);
const steps = () => [...document.querySelectorAll('p')].map((p) => p.textContent.trim());

beforeEach(() => setLocale('en'));
afterEach(() => {
  if (app) unmount(app);
  target?.remove();
  app = target = null;
});

describe('HaDrbdRepair', () => {
  it('renders nothing without issues', () => {
    render({ issues: [] });
    expect(document.querySelector('[data-testid="ha-drbd-repair"]')).toBeNull();
  });

  it('StandAlone on the primary side: stale node, risk and commands on the right nodes', () => {
    render({ issues: [primarySide] });
    const text = document.body.textContent;
    expect(text).toContain('Storage replica out of sync');
    expect(document.querySelector('[data-testid="ha-drbd-stale"]').textContent).toContain('node-b');
    expect(document.querySelector('[data-testid="ha-drbd-risk"]').textContent).toBe(
      'Only the stale copy on node-b is overwritten; node-a keeps running.'
    );
    const ps = steps();
    expect(ps).toContain('Step 1: run on node-b');
    expect(ps).toContain('Step 2: run on node-a');
    expect(ps.some((p) => p.includes(`sudo drbdadm status ${RES} on node-b`))).toBe(true);
    expect(cmdBlocks()).toEqual([
      `sudo drbdadm disconnect ${RES}:node-a\nsudo drbdadm connect --discard-my-data ${RES}:node-a`,
      `sudo drbdadm connect ${RES}:node-b`,
    ]);
    expect(document.querySelector('[data-testid="ha-drbd-raw"]')).toBeNull();
  });

  it('stale side Inconsistent and safe: discard runs on the local stale node', () => {
    render({ issues: [staleSide] });
    expect(document.querySelector('[data-testid="ha-drbd-risk"]').textContent).toContain(
      'stale copy on node-b is overwritten; node-a keeps running'
    );
    expect(cmdBlocks()[0]).toContain('--discard-my-data');
    expect(steps().some((p) => p.includes('drbdadm status'))).toBe(false);
  });

  it('stale side not proven safe: UI drops the local discard and warns', () => {
    render({ issues: [{ ...staleSide, safe_to_discard_local: false }] });
    expect(cmdBlocks().join('\n')).not.toContain('--discard-my-data');
    expect(document.querySelector('[data-testid="ha-drbd-nodiscard"]')).not.toBeNull();
  });

  it('UpToDate vs UpToDate StandAlone: no discard suggestion, manual resolution', () => {
    render({ issues: [splitBrain] });
    expect(cmdBlocks().join('\n')).not.toContain('--discard-my-data');
    expect(document.querySelector('[data-testid="ha-drbd-manual"]').textContent).toContain(
      'split-brain'
    );
    expect(document.querySelector('[data-testid="ha-drbd-risk"]')).toBeNull();
  });

  it('rejects unsafe names: nothing is rendered as a command', () => {
    const evil = {
      ...staleSide,
      peer: 'x;rm -rf /',
      manual_resolution: true,
      repair_steps: [
        { node: 'node-b', command: 'sudo drbdadm disconnect r:x;rm -rf /' },
        { node: 'node-b', command: 'sudo drbdadm connect --discard-my-data r:$(id)' },
        { node: 'node-b', command: 'rm -rf /' },
      ],
    };
    render({ issues: [evil] });
    expect(cmdBlocks()).toEqual([]);
    expect(document.querySelector('[data-testid="ha-drbd-manual"]')).not.toBeNull();
  });

  it('ADVANCED mode shows the raw issue data', () => {
    render({ issues: [primarySide], advanced: true });
    const raw = document.querySelector('[data-testid="ha-drbd-raw"]').textContent;
    expect(JSON.parse(raw).peer_connection).toBe('StandAlone');
  });

  it('is translated to es and ca', () => {
    setLocale('es');
    render({ issues: [primarySide] });
    expect(document.body.textContent).toContain('Réplica de almacenamiento desincronizada');
    unmount(app);
    target.remove();
    setLocale('ca');
    render({ issues: [primarySide] });
    expect(document.body.textContent).toContain("Rèplica d'emmagatzematge desincronitzada");
  });
});
