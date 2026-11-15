import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  vm: null,
  calls: [],
  ticketError: null,
  nics: [],
  guestError: null,
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      getVM: (...a) => {
        fx.calls.push(['getVM', ...a]);
        return Promise.resolve(fx.vm);
      },
      listNodes: () =>
        Promise.resolve({
          nodes: [
            {
              id: 'n1',
              name: 'kvm-b',
              type: 'remote',
              enabled: true,
              status: 'online',
              uri: 'qemu+ssh://root@10.0.0.9/system',
              ui_url: 'https://192.168.1.9:8080',
            },
          ],
        }),
      getIncusClusterMembers: () =>
        Promise.resolve({
          members: [
            { name: 'm1', status: 'Online' },
            { name: 'm2', status: 'Online' },
          ],
        }),
      getVNCTicket: () =>
        fx.ticketError ? Promise.reject(fx.ticketError) : Promise.resolve({ vnc_ticket: 'T' }),
      getConsoleTicket: () => Promise.resolve({ ticket: 'S' }),
      createNetIface: (id, payload) => {
        fx.nics.push(payload);
        return Promise.resolve({ status: 'attached' });
      },
      getVMMetrics: (...a) => {
        fx.calls.push(['getVMMetrics', ...a]);
        return Promise.reject(new Error('x'));
      },
      getGuestInfo: () =>
        fx.guestError ? Promise.reject(fx.guestError) : Promise.resolve({ available: false }),
      listSnapshots: (...a) => {
        fx.calls.push(['listSnapshots', ...a]);
        return Promise.resolve([]);
      },
      getBootDevice: () => Promise.resolve({ boot_device: 'hd' }),
      getVMMeta: () => Promise.resolve({}),
      getVMFirewall: () => Promise.reject(new Error('x')),
      getVMSchedule: () => Promise.reject(new Error('x')),
      me: () => Promise.resolve({ allowed_networks: [] }),
      getSPICEUrl: () => '/api/vms/v1/spice?node=kvm-b&hv=kvm',
      getVNCWsUrl: () => 'ws://x/vnc',
      getSerialWsUrl: () => 'ws://x/serial',
    },
    {
      get(target, prop) {
        if (prop in target) return target[prop];
        if (typeof prop !== 'string') return undefined;
        return () => Promise.resolve([]);
      },
    }
  );
  const auth = new Proxy(
    { user: 'admin', role: 'admin' },
    {
      get(target, prop) {
        if (prop in target) return target[prop];
        if (typeof prop !== 'string') return undefined;
        return () => true;
      },
    }
  );
  return { api, auth };
});
vi.mock('$lib/router.svelte.js', () => ({
  navigate: vi.fn(),
  getRoute: () => ({ name: 'vm-detail', path: '/vms/v1', params: { id: 'v1' }, query: {} }),
}));
vi.mock('$lib/stores/events.svelte.js', () => ({
  events: { onVmMetrics: () => () => {}, onVmState: () => () => {} },
}));
vi.mock('@novnc/novnc', () => ({
  default: class {
    addEventListener() {}
    disconnect() {}
  },
}));

import VmDetail from '../routes/VmDetail.svelte';

const settle = async () => {
  for (let i = 0; i < 12; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

let target, app;
async function open(props) {
  target = document.createElement('div');
  document.body.appendChild(target);
  app = mount(VmDetail, { target, props });
  await settle();
}

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  fx.calls = [];
  // Incus container on another member of the local Incus cluster: no
  // node_id (not a registered remote endpoint), node = member name.
  fx.vm = {
    id: 'n8n',
    name: 'n8n',
    type: 'container',
    hypervisor: 'incus',
    state: 'running',
    node: 'node-a',
    autostart: true,
    disks: [],
    networks: [],
  };
});
afterEach(async () => {
  if (app) await unmount(app);
  target?.remove();
  app = null;
});

describe('VmDetail for an Incus container on another cluster member', () => {
  it('lists snapshots with node + hv=incus', async () => {
    await open({ vmId: 'n8n', node: 'node-a', hv: 'incus' });
    expect(fx.calls).toContainEqual(['getVM', 'n8n', 'node-a', 'incus']);
    expect(fx.calls).toContainEqual(['listSnapshots', 'n8n', 'node-a', 'incus']);
  });

  it('uses container wording for delete and autostart', async () => {
    await open({ vmId: 'n8n', node: 'node-a', hv: 'incus' });
    const text = target.textContent;
    expect(text).toContain('Delete container');
    expect(text).not.toContain('Delete VM');
    expect(text).not.toContain('libvirtd');
    expect(text).toContain('Incus starts this container automatically when the host boots');
  });

  it('keeps VM wording for a KVM VM', async () => {
    fx.vm = { ...fx.vm, id: 'v1', name: 'v1', type: 'vm', hypervisor: 'kvm', node: '' };
    await open({ vmId: 'v1' });
    const text = target.textContent;
    expect(text).toContain('Delete VM');
    expect(text).toContain('libvirtd starts this VM automatically');
  });
});
