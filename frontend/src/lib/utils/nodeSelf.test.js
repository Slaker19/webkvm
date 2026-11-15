import { describe, it, expect } from 'vitest';
import {
  canonicalNodeName,
  mergeSelfVms,
  nodeHealthCounts,
  missingHaWorkloads,
  foldHaMissing,
  migrationTargets,
} from './nodes.js';

const local = { id: 'local', name: 'node-b', type: 'local', status: 'online' };
const dup = {
  id: 'd1',
  name: 'old-name',
  type: 'remote',
  backend: 'libvirt',
  uri: 'qemu+ssh://operator@10.20.30.11/system',
  is_self: true,
  enabled: true,
  status: 'online',
};
const dev = { id: 'n2', name: 'node-a', type: 'remote', backend: 'incus', status: 'online' };
const ha2 = {
  id: 'n3',
  name: 'node-b-ha2',
  type: 'remote',
  backend: 'libvirt',
  uri: 'qemu+ssh://root@10.20.30.10/system',
  status: 'online',
  access: 'not_set_up',
  access_code: 'no_key',
};
const nodes = [local, dup, dev, ha2];

describe('canonicalNodeName', () => {
  it('maps empty, "local" and the self duplicate (name or id) to this server', () => {
    expect(canonicalNodeName('', nodes, 'node-b')).toBe('node-b');
    expect(canonicalNodeName('local', nodes, 'node-b')).toBe('node-b');
    expect(canonicalNodeName('old-name', nodes, 'node-b')).toBe('node-b');
    expect(canonicalNodeName('d1', nodes, 'node-b')).toBe('node-b');
    expect(canonicalNodeName('node-a', nodes, 'node-b')).toBe('node-a');
  });
});

describe('mergeSelfVms', () => {
  it('moves VMs of the self entry to this server and drops copies already listed locally', () => {
    const vms = [
      { id: 'a', name: 'ha-test', node: 'node-b' },
      { id: 'b', name: 'ha-test', node: 'old-name', node_id: 'd1' },
      { id: 'c', name: 'only-dup', node: 'old-name', node_id: 'd1' },
      { id: 'd', name: 'n8n', node: 'node-a', node_id: 'n2', hypervisor: 'incus' },
    ];
    const out = mergeSelfVms(vms, nodes, 'node-b');
    expect(out.map((v) => v.id)).toEqual(['a', 'c', 'd']);
    const moved = out.find((v) => v.id === 'c');
    expect(moved.node).toBe('node-b');
    expect(moved.node_id).toBeUndefined();
  });
  it('returns the same list when there is no self entry', () => {
    const vms = [{ id: 'a', name: 'x', node: 'node-b' }];
    expect(mergeSelfVms(vms, [local], 'node-b')).toEqual(vms);
  });
});

describe('nodeHealthCounts', () => {
  it('classifies with nodeHealth, excludes self duplicates and counts no_access apart', () => {
    const c = nodeHealthCounts([
      ...nodes,
      { id: 'o', name: 'off', type: 'remote', status: 'offline' },
      { id: 'g', name: 'deg', type: 'remote', status: 'degraded' },
      { id: 'x', name: 'dis', type: 'remote', enabled: false, status: 'offline' },
    ]);
    expect(c).toEqual({
      total: 6,
      enabled: 5,
      disabled: 1,
      online: 2,
      no_access: 1,
      degraded: 1,
      offline: 1,
      unknown: 0,
    });
  });
  it('a node with a last_error but status online is never counted offline', () => {
    const c = nodeHealthCounts([
      { ...ha2, last_error: 'VM listing failed: no cluster SSH key', access: '' },
    ]);
    expect(c.offline).toBe(0);
    expect(c.online).toBe(1);
  });
});

describe('missingHaWorkloads', () => {
  const vms = [{ id: 'a', name: 'ha-test', node: 'node-b', hypervisor: 'kvm' }];
  const wl = (over) => ({ key: `kvm:${over.name}`, kind: 'kvm', managed: true, ...over });
  it('explains HA workloads that the VM list does not show', () => {
    const out = missingHaWorkloads(
      [
        wl({ name: 'ha-test', node: 'node-b' }),
        wl({ name: 'ha-test-01', node: 'node-c' }),
        wl({ name: 'ha-test-02', node: 'node-b-ha2' }),
        wl({ name: 'ha-test-03', node: '10.20.30.10' }),
        wl({ name: 'stopped', node: '' }),
      ],
      vms,
      nodes
    );
    expect(out).toEqual([
      {
        key: 'kvm:ha-test-01',
        name: 'ha-test-01',
        node: 'node-c',
        reason: 'not_registered',
        nodeId: '',
      },
      {
        key: 'kvm:ha-test-02',
        name: 'ha-test-02',
        node: 'node-b-ha2',
        reason: 'no_access',
        nodeId: 'n3',
      },
      {
        key: 'kvm:ha-test-03',
        name: 'ha-test-03',
        node: '10.20.30.10',
        reason: 'no_access',
        nodeId: 'n3',
      },
    ]);
  });
  it('a workload on this server (or its duplicate) that is missing is "not listed"', () => {
    const out = missingHaWorkloads([wl({ name: 'ghost', node: 'node-b' })], [], nodes);
    expect(out[0].reason).toBe('not_listed');
  });
  it('handles offline / disabled nodes and bad input', () => {
    const list = [
      { id: 'o', name: 'off', type: 'remote', status: 'offline' },
      { id: 'x', name: 'dis', type: 'remote', enabled: false },
    ];
    const out = missingHaWorkloads(
      [wl({ name: 'a', node: 'off' }), wl({ name: 'b', node: 'DIS' })],
      [],
      list
    );
    expect(out.map((w) => w.reason)).toEqual(['offline', 'disabled']);
    expect(missingHaWorkloads(null, null, null)).toEqual([]);
  });
});

describe('migrationTargets and self duplicates', () => {
  it('never offers the duplicate entry of this server as a target', () => {
    const list = [local, dup, { ...ha2, access: 'ok' }];
    const vm = { node: 'node-b' };
    expect(migrationTargets(list, vm, 'node-b').map((n) => n.id)).toEqual(['n3']);
  });
});

// 8.26 minor: the VM list stacked two notices for the same cause (node
// without SSH access): the health banner and the HA "not shown" notice.
describe('foldHaMissing', () => {
  const issues = [{ id: 'n2', name: 'east', kind: 'no_access' }];
  it('moves workloads hidden by a node already in the health banner into that banner', () => {
    const missing = [
      { key: 'a', name: 'kvm-vm', node: 'east', reason: 'no_access', nodeId: 'n2' },
      { key: 'b', name: 'lost', node: 'gone', reason: 'not_registered', nodeId: '' },
    ];
    const out = foldHaMissing(missing, issues);
    expect(out.rest.map((w) => w.key)).toEqual(['b']);
    expect(out.byNode).toEqual({ n2: ['kvm-vm'] });
  });
  it('keeps everything when no banner issue covers the node', () => {
    const missing = [{ key: 'a', name: 'x', node: 'west', reason: 'offline', nodeId: 'n3' }];
    expect(foldHaMissing(missing, issues)).toEqual({ rest: missing, byNode: {} });
    expect(foldHaMissing(null, null)).toEqual({ rest: [], byNode: {} });
  });
});
