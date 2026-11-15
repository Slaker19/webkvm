import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { api } from './stores/auth.svelte.js';

let calls;
beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((url, opts) => {
      calls.push([url, opts.method, opts.body]);
      return Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: () => null },
        clone: () => ({}),
        text: () => Promise.resolve('{}'),
      });
    })
  );
});
afterEach(() => vi.unstubAllGlobals());

describe('cluster SSH + HA workload api helpers', () => {
  it('builds ssh endpoints', async () => {
    await api.getClusterSSH();
    await api.generateClusterSSHKey();
    await api.startClusterSSHRotation();
    await api.commitClusterSSHRotation();
    await api.abortClusterSSHRotation();
    await api.getNodeSSHAuthorize('n 1');
    await api.captureNodeHostKey('n1');
    await api.pinNodeHostKey('n1', 'SHA256:x', true);
    await api.testNodeSSH('n1');
    expect(calls.map((c) => `${c[1]} ${c[0]}`)).toEqual([
      'GET /api/cluster/ssh',
      'POST /api/cluster/ssh/key',
      'POST /api/cluster/ssh/rotate',
      'POST /api/cluster/ssh/rotate/commit',
      'POST /api/cluster/ssh/rotate/abort',
      'GET /api/nodes/n%201/ssh-authorize',
      'POST /api/nodes/n1/ssh/hostkey',
      'POST /api/nodes/n1/ssh/pin',
      'POST /api/nodes/n1/ssh/test',
    ]);
    expect(JSON.parse(calls[7][2])).toEqual({ fingerprint: 'SHA256:x', replace: true });
  });

  it('builds HA workload endpoints; project only for incus', async () => {
    await api.listHAWorkloads();
    await api.getHAWorkload('kvm', 'vm1');
    await api.getHAWorkload('incus', 'c1', 'prod');
    await api.enableHAWorkload('incus', 'c1', '', { dry_run: true });
    await api.disableHAWorkload('kvm', 'vm1', '', { mode: 'forget', confirm: 'kvm:vm1' });
    await api.moveHAWorkload('incus', 'c1', 'prod', 'n2');
    await api.clearHAWorkloadFailcount('kvm', 'vm1');
    await api.maintenanceHAWorkload('kvm', 'vm1', '', 'begin', 900);
    await api.bulkHAWorkloads({ action: 'enable', items: [] });
    expect(calls.map((c) => `${c[1]} ${c[0]}`)).toEqual([
      'GET /api/ha/workloads',
      'GET /api/ha/workloads/kvm/vm1',
      'GET /api/ha/workloads/incus/c1?project=prod',
      'POST /api/ha/workloads/incus/c1/enable?project=default',
      'DELETE /api/ha/workloads/kvm/vm1?mode=forget&confirm=kvm%3Avm1',
      'POST /api/ha/workloads/incus/c1/move?project=prod',
      'POST /api/ha/workloads/kvm/vm1/clear-failcount',
      'POST /api/ha/workloads/kvm/vm1/maintenance',
      'POST /api/ha/workloads/bulk',
    ]);
    expect(JSON.parse(calls[7][2])).toEqual({ action: 'begin', ttl_s: 900 });
  });

  it('builds released-VM endpoints and the accept_unrestorable flag', async () => {
    await api.disableHAWorkload('kvm', 'vm1', '', { mode: 'stop', acceptUnrestorable: true });
    await api.disableHAWorkload('kvm', 'vm1', '', { mode: 'stop' });
    await api.listReleasedVMs();
    await api.restoreReleasedVM('vm 1', { confirm: 'vm 1', start: true, node: 'n1' });
    await api.restoreReleasedVM('vm1', { confirm: 'vm1' });
    await api.discardReleasedVM('vm1', 'vm1');
    expect(calls.map((c) => `${c[1]} ${c[0]}`)).toEqual([
      'DELETE /api/ha/workloads/kvm/vm1?mode=stop&accept_unrestorable=true',
      'DELETE /api/ha/workloads/kvm/vm1?mode=stop',
      'GET /api/ha/released',
      'POST /api/ha/released/vm%201/restore',
      'POST /api/ha/released/vm1/restore',
      'DELETE /api/ha/released/vm1?confirm=vm1',
    ]);
    expect(JSON.parse(calls[3][2])).toEqual({ confirm: 'vm 1', start: true, node: 'n1' });
    expect(JSON.parse(calls[4][2])).toEqual({ confirm: 'vm1', start: false });
  });
});
