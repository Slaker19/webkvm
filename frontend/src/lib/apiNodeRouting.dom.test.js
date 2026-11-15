import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const fetchMock = vi.fn(
  async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
);

vi.mock('$lib/stores/auth.svelte.js', async (importOriginal) => {
  // Use the real store: we only replace the transport.
  return importOriginal();
});

import { api } from '$lib/stores/auth.svelte.js';

describe('remote node routing for console tickets and hardware mutations', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', fetchMock);
    fetchMock.mockClear();
  });
  afterEach(() => vi.unstubAllGlobals());

  it('routes console and VNC tickets to the remote node', async () => {
    await api.getVNCTicket('vm1', 'node-a', 'kvm');
    await api.getConsoleTicket('vm1', 'node-a', 'kvm');
    expect(String(fetchMock.mock.calls[0][0])).toContain('/vms/vm1/vnc-ticket?node=node-a&hv=kvm');
    expect(String(fetchMock.mock.calls[1][0])).toContain(
      '/vms/vm1/console-ticket?node=node-a&hv=kvm'
    );
  });

  it('routes disk and NIC mutations to the remote node', async () => {
    await api.deleteDisk('vm1', 'vdb', 'node-a', 'kvm');
    await api.resizeVmDisk('vm1', 'vdb', 20, 'node-a', 'kvm');
    await api.changeDiskBus('vm1', 'vdb', 'sata', 'node-a', 'kvm');
    await api.deleteNetIface('vm1', 'aa:bb', 'node-a', 'kvm');
    await api.setVMAutostart('vm1', true, 'node-a', 'kvm');
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls.some((u) => u.includes('/vms/vm1/disks/vdb?node=node-a&hv=kvm'))).toBe(true);
    expect(urls.some((u) => u.includes('/vms/vm1/disks/vdb/resize?node=node-a&hv=kvm'))).toBe(true);
    expect(urls.some((u) => u.includes('/vms/vm1/disks/vdb/bus?node=node-a&hv=kvm'))).toBe(true);
    expect(urls.some((u) => u.includes('/vms/vm1/networks/aa%3Abb?node=node-a&hv=kvm'))).toBe(true);
    expect(urls.some((u) => u.includes('/vms/vm1/autostart?node=node-a&hv=kvm'))).toBe(true);
  });
});
