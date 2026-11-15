import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { api } from './stores/auth.svelte.js';

// An Incus container on another cluster member must reach the Incus
// handlers: every per-VM call carries node AND hv=incus. Without hv the
// backend takes the libvirt/SSH path and answers 409 no_key.
let calls;
beforeEach(() => {
  calls = [];
  window.location.hash = '';
  vi.stubGlobal(
    'fetch',
    vi.fn((url, opts) => {
      calls.push(`${opts.method} ${url}`);
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
afterEach(() => {
  vi.unstubAllGlobals();
  window.location.hash = '';
});

describe('snapshot api helpers carry node + hv', () => {
  it('list/create/revert/delete send hv=incus&node=…', async () => {
    await api.listSnapshots('n8n', 'node-a', 'incus');
    await api.createSnapshot('n8n', { name: 's1' }, 'node-a', 'incus');
    await api.revertSnapshot('n8n', 's1', 'node-a', 'incus');
    await api.deleteSnapshot('n8n', 's1', 'node-a', 'incus');
    expect(calls).toEqual([
      'GET /api/vms/n8n/snapshots?node=node-a&hv=incus',
      'POST /api/vms/n8n/snapshots?node=node-a&hv=incus',
      'POST /api/vms/n8n/snapshots/s1/revert?node=node-a&hv=incus',
      'DELETE /api/vms/n8n/snapshots/s1?node=node-a&hv=incus',
    ]);
  });

  it('stays clean for a local VM', async () => {
    await api.listSnapshots('v1');
    await api.createSnapshot('v1', { name: 's1' });
    await api.revertSnapshot('v1', 's1', 'local');
    await api.deleteSnapshot('v1', 's1');
    expect(calls).toEqual([
      'GET /api/vms/v1/snapshots',
      'POST /api/vms/v1/snapshots',
      'POST /api/vms/v1/snapshots/s1/revert',
      'DELETE /api/vms/v1/snapshots/s1',
    ]);
  });

  it('adds the route hv when a detail call carries node but no hv', async () => {
    window.location.hash = '#/vms/n8n?node=node-a&hv=incus';
    await api.listSnapshots('n8n', 'node-a');
    await api.startVM('n8n', 'node-a');
    await api.getVMMeta('n8n');
    expect(calls).toEqual([
      'GET /api/vms/n8n/snapshots?node=node-a&hv=incus',
      'POST /api/vms/n8n/start?node=node-a&hv=incus',
      'GET /api/vms/n8n/meta?node=node-a&hv=incus',
    ]);
  });

  it('does not add the route hv to a call for another node', async () => {
    window.location.hash = '#/vms/n8n?node=node-a&hv=incus';
    await api.listSnapshots('n8n', 'other');
    expect(calls).toEqual(['GET /api/vms/n8n/snapshots?node=other']);
  });
});
