import { describe, it, expect } from 'vitest';
import {
  workloadRef,
  isHaManaged,
  haErrorText,
  moveTargets,
  workloadHealth,
  maintenanceLeft,
  releasesKvmStorage,
  pendingReleased,
  canRestore,
  releasedFor,
  peersDownDetails,
  DISABLE_MODES,
  DEFAULT_DISABLE_MODE,
} from './haWorkloads.js';
import { accessState, currentStep } from './clusterAccess.js';

const t = (k) => ({ 'haWorkloads.errors.operation_busy': 'busy' })[k] ?? k;

describe('haWorkloads utils', () => {
  it('builds refs per hypervisor', () => {
    expect(workloadRef({ name: 'a', hypervisor: 'kvm' }).key).toBe('kvm:a');
    expect(workloadRef({ name: 'a', hypervisor: 'incus' }).key).toBe('incus:default/a');
    expect(workloadRef({ name: 'a', hypervisor: 'incus', project: 'p' }).project).toBe('p');
  });
  it('detects managed from vm flag or store', () => {
    expect(isHaManaged({ name: 'a', managed_by: 'pacemaker' }, [])).toBe(true);
    expect(
      isHaManaged({ name: 'a', hypervisor: 'incus' }, [{ key: 'incus:default/a', managed: true }])
    ).toBe(true);
    expect(isHaManaged({ name: 'a', hypervisor: 'kvm' }, [{ key: 'kvm:b', managed: true }])).toBe(
      false
    );
  });
  it('translates known codes, falls back to the message', () => {
    expect(haErrorText(t, { code: 'operation_busy', message: 'x' })).toBe('busy');
    expect(haErrorText(t, { code: 'zzz', message: 'raw' })).toBe('raw');
  });
  it('excludes the current node from move targets', () => {
    expect(moveTargets({ node: 'a', allowed_nodes: [] }, ['a', 'b'])).toEqual(['b']);
    expect(moveTargets({ node: 'a', allowed_nodes: ['a', 'c'] }, ['a', 'b'])).toEqual(['c']);
  });
  it('classifies health and maintenance', () => {
    expect(workloadHealth({ state: 'started', failcount: 0 })).toBe('ok');
    expect(workloadHealth({ state: 'started', failcount: 2 })).toBe('bad');
    expect(
      maintenanceLeft({ maint_until: '2026-01-01T00:10:00Z' }, Date.parse('2026-01-01T00:00:00Z'))
    ).toBe(600);
  });
});

describe('clusterAccess utils', () => {
  it('derives access state and step', () => {
    expect(accessState({ host_key: { state: 'changed' } })).toBe('changed');
    const fresh = { ok: true, at: new Date().toISOString() };
    expect(accessState({ host_key: { state: 'pinned' }, last_test: fresh })).toBe('ready');
    // Pinned but never (recently) tested is neutral, not "not set up".
    expect(accessState({ host_key: { state: 'pinned' } })).toBe('unverified');
    expect(accessState({ host_key: { state: 'unknown' } })).toBe('not_set_up');
    expect(currentStep({ configured: false }, {})).toBe('key');
    expect(currentStep({ configured: true }, { host_key: { state: 'unknown' } })).toBe('authorize');
    expect(currentStep({ configured: true }, { host_key: { state: 'pinned' } })).toBe('test');
  });
});

describe('disable-mode safety helpers', () => {
  it('defaults to the safest mode (pause), listed first', () => {
    expect(DEFAULT_DISABLE_MODE).toBe('pause');
    expect(DISABLE_MODES[0]).toBe('pause');
  });
  it('only KVM stop/forget release the shared storage', () => {
    expect(releasesKvmStorage({ kind: 'kvm' }, 'stop')).toBe(true);
    expect(releasesKvmStorage({ kind: 'kvm' }, 'forget')).toBe(true);
    expect(releasesKvmStorage({ kind: 'kvm' }, 'pause')).toBe(false);
    expect(releasesKvmStorage({ kind: 'incus' }, 'stop')).toBe(false);
    expect(releasesKvmStorage(null, 'stop')).toBe(false);
  });
});

describe('released VMs helpers', () => {
  const entries = [
    { name: 'a', status: 'released', restorable: true },
    { name: 'b', status: 'released', restorable: false },
    { name: 'c', status: 'restored', restorable: true },
  ];
  it('lists only entries waiting for a restore', () => {
    expect(pendingReleased(entries).map((e) => e.name)).toEqual(['a', 'b']);
    expect(pendingReleased(null)).toEqual([]);
  });
  it('offers restore only for a released entry with a saved definition', () => {
    expect(canRestore(entries[0])).toBe(true);
    expect(canRestore(entries[1])).toBe(false);
    expect(canRestore(entries[2])).toBe(false);
    expect(canRestore(null)).toBe(false);
  });
  it('finds the entry of a VM', () => {
    expect(releasedFor(entries, 'c').status).toBe('restored');
    expect(releasedFor(entries, 'zz')).toBeNull();
    expect(releasedFor(null, 'a')).toBeNull();
  });
});

describe('peersDownDetails', () => {
  it('is null for any other error', () => {
    expect(peersDownDetails(null)).toBeNull();
    expect(peersDownDetails({ code: 'mount_failed', data: { peers: [] } })).toBeNull();
  });
  it('groups nodes per command and keeps the connection state', () => {
    const d = peersDownDetails({
      code: 'drbd_peers_down',
      data: {
        resource: 'r1',
        peers: [
          { node: 'b', connection: 'Connecting', command: 'drbdadm up r1' },
          { node: 'c', connection: '', command: 'drbdadm up r1' },
          { node: 'bad;x', connection: 'Connecting', command: '' },
        ],
      },
    });
    expect(d.resource).toBe('r1');
    expect(d.peers.map((p) => [p.node, p.connection])).toEqual([
      ['b', 'Connecting'],
      ['c', 'unknown'],
      ['bad;x', 'Connecting'],
    ]);
    expect(d.commands).toEqual(['# b, c', 'drbdadm up r1']);
  });
  it('survives malformed bodies', () => {
    expect(peersDownDetails({ code: 'drbd_peers_down' })).toEqual({
      resource: '',
      peers: [],
      commands: [],
    });
    expect(
      peersDownDetails({ code: 'drbd_peers_down', data: { resource: 'r', peers: 'x' } }).commands
    ).toEqual(['drbdadm up r']);
  });
});
