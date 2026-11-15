import { describe, it, expect, beforeEach } from 'vitest';
import { addPendingVm, mergePendingVms, clearPendingVms } from './pendingVms.js';

beforeEach(() => clearPendingVms());

describe('pendingVms', () => {
  it('adds a just-created VM to a listing that does not have it yet', () => {
    addPendingVm({ id: 'x', name: 'new', node: 'kvm-b', node_id: 'n1' }, 0);
    const out = mergePendingVms([{ id: 'a', name: 'a' }], 1000);
    expect(out.map((v) => v.id)).toEqual(['a', 'x']);
    expect(out[1]).toMatchObject({ pending: true, state: 'shutoff', disks: [], networks: [] });
  });
  it('drops it once a listing contains it (by id or by name on the node)', () => {
    addPendingVm({ id: 'x', name: 'new', node_id: 'n1' }, 0);
    expect(mergePendingVms([{ id: 'x', name: 'new' }], 10).length).toBe(1);
    expect(mergePendingVms([], 20).length).toBe(0);
    addPendingVm({ name: 'byname', node_id: 'n1' }, 0);
    expect(mergePendingVms([{ id: 'q', name: 'byname', node_id: 'n1' }], 10).length).toBe(1);
    expect(mergePendingVms([], 20).length).toBe(0);
  });
  it('forgets it after two minutes', () => {
    addPendingVm({ id: 'x', name: 'new' }, 0);
    expect(mergePendingVms([], 119_000).length).toBe(1);
    expect(mergePendingVms([], 121_000).length).toBe(0);
  });
});
