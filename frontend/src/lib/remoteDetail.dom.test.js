import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  vm: null,
  calls: [],
  meta: {},
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
        fx.calls.push(['getVNCTicket']) && fx.ticketError
          ? Promise.reject(fx.ticketError)
          : Promise.resolve({ vnc_ticket: 'T' }),
      getConsoleTicket: () => {
        fx.calls.push(['getConsoleTicket']);
        return Promise.resolve({ ticket: 'S' });
      },
      createNetIface: (id, payload) => {
        fx.nics.push(payload);
        return Promise.resolve({ status: 'attached' });
      },
      getVMMetrics: (...a) => {
        fx.calls.push(['getVMMetrics', ...a]);
        return Promise.reject(new Error('x'));
      },
      getGuestInfo: () =>
        fx.calls.push(['getGuestInfo']) && fx.guestError
          ? Promise.reject(fx.guestError)
          : Promise.resolve({ available: false }),
      listSnapshots: () => Promise.resolve([]),
      getBootDevice: () => Promise.resolve({ boot_device: 'hd' }),
      getVMMeta: (...args) => {
        fx.calls.push(['getVMMeta', ...args]);
        return Promise.resolve(fx.meta);
      },
      getVMFirewall: (...a) => {
        fx.calls.push(['getVMFirewall', ...a]);
        return Promise.reject(new Error('x'));
      },
      getVMSchedule: (...a) => {
        fx.calls.push(['getVMSchedule', ...a]);
        return Promise.reject(new Error('x'));
      },
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
const tabLabels = () => [...target.querySelectorAll('[role=tab]')].map((b) => b.textContent.trim());

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  fx.calls = [];
  fx.ticketError = null;
  fx.nics = [];
  fx.guestError = null;
  localStorage.removeItem('webkvm.createWizardMode');
  fx.vm = {
    id: 'v1',
    name: 'web',
    type: 'vm',
    hypervisor: 'kvm',
    state: 'running',
    node: 'kvm-b',
    node_id: 'n1',
    graphics_type: 'both',
    disks: [],
    networks: [],
  };
  fx.meta = {};
});
afterEach(async () => {
  if (app) await unmount(app);
  target?.remove();
  app = null;
});

describe('VmDetail for a VM on another node', () => {
  it('loads with node+hv and shows the node banner', async () => {
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    expect(fx.calls[0]).toEqual(['getVM', 'v1', 'kvm-b', 'kvm']);
    expect(fx.calls).toContainEqual(['getVMMeta', 'v1', 'kvm-b', 'kvm']);
    const banner = target.querySelector('[data-testid=remote-node-banner]');
    expect(banner.textContent).toContain('This VM lives on node kvm-b');
    expect(target.textContent).not.toContain('only supports');
  });

  it('identifies a remote template as local to its node and hides unsupported central controls', async () => {
    fx.meta = { template: true, shared: true };
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });

    expect(target.textContent).toContain('Instantiate it from that node’s own WebKVM interface');
    expect(
      [...target.querySelectorAll('button')].some((button) =>
        /Turn into template|Unmark as template/.test(button.textContent)
      )
    ).toBe(false);
  });

  it('keeps every tab (no blanket disabling) and shows the notice on L-only tabs', async () => {
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    const labels = tabLabels().join('|');
    for (const expected of ['Overview', 'Hardware', 'Cloud-Init', 'Snapshots'])
      expect(labels).toContain(expected);
    const notice = target.querySelector('[data-testid=remote-unsupported]');
    expect(notice.textContent).toContain("only available from that node's own WebKVM");
    expect(target.querySelector('[data-testid=remote-unsupported-link]').getAttribute('href')).toBe(
      'https://192.168.1.9:8080/'
    );
  });

  it('Guest tab: the local-process 501 becomes the translated notice, never the raw error', async () => {
    fx.guestError = Object.assign(new Error('exec: "virsh": executable file not found in $PATH'), {
      status: 501,
      code: 'remote_unsupported',
      data: { hint: 'open_node_ui', ui_url: 'https://192.168.1.9:8080' },
    });
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    await settle();
    expect(target.textContent).not.toContain('virsh');
    const card = target.querySelector('#guest')?.closest('section,div') || target;
    expect(card.textContent).toContain('Not available from here');
  });

  it('Guest tab: a raw network error is shown translated when it carries a known code', async () => {
    // 8.30: /guest-info is no longer requested for a remote KVM VM (class L),
    // so the translation of a typed error is checked where it is requested.
    fx.vm = { ...fx.vm, node: '', node_id: '' };
    fx.guestError = Object.assign(new Error('raw'), { status: 502, code: 'node_timeout' });
    await open({ vmId: 'v1' });
    await settle();
    // The local page has other text containing "raw" (e.g. disk formats):
    // look only at the Guest card.
    const guest = target.querySelector('section#guest');
    expect(guest.textContent).toContain('took too long to answer');
    expect(guest.textContent).not.toContain('raw');
  });

  it('Metrics: a remote VM shows the notice with the node link, no endless "Collecting"', async () => {
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    await settle();
    expect(target.textContent).not.toContain('Collecting first samples');
    const metricsCard = [...target.querySelectorAll('[data-testid=remote-unsupported]')];
    expect(metricsCard.length).toBeGreaterThan(0);
  });

  it('never links the legacy /console page for a remote VM', async () => {
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    await settle();
    expect(target.querySelector('iframe')).toBeNull();
    expect(target.innerHTML).not.toContain('/console/');
    expect(target.querySelector('[data-testid=remote-vv-link]')).not.toBeNull();
  });

  it('shows the console error and offers Cluster access for ssh errors', async () => {
    fx.ticketError = { code: 'ssh_auth_denied', message: 'raw backend text', status: 502 };
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    await settle();
    expect(target.textContent).toContain('Open Cluster access');
  });
});

describe('NIC attach on a remote VM: free-text bridge name', () => {
  const dlg = () => document.querySelector('[role=dialog]');
  const openDialog = async () => {
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    [...target.querySelectorAll('[role=tab]')]
      .find((b) => b.textContent.includes('Hardware'))
      .click();
    await settle();
    [...document.querySelectorAll('button')]
      .find((b) => b.textContent.includes('Add Interface'))
      .click();
    await settle();
  };
  const type = async (input, v) => {
    input.value = v;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
  };

  it('EASY mode has no free-text field', async () => {
    await openDialog();
    expect(dlg()).not.toBeNull();
    expect(dlg().querySelector('[data-testid=anet-bridge]')).toBeNull();
    expect(dlg().querySelector('[data-testid=anet-advanced]')).not.toBeNull();
  });

  it('ADVANCED explains, validates and sends the typed bridge', async () => {
    await openDialog();
    dlg().querySelector('[data-testid=anet-advanced]').click();
    await settle();
    const input = dlg().querySelector('[data-testid=anet-bridge]');
    expect(input).not.toBeNull();
    expect(dlg().textContent).toContain('This node cannot list its bridges');
    expect(dlg().textContent).toContain('vmbr0 or br0');
    await type(input, 'bad name');
    expect(dlg().textContent).toContain('Use 1 to 15 characters');
    const add = [...dlg().querySelectorAll('button')].find((b) => b.textContent.trim() === 'Add');
    expect(add.disabled).toBe(true);
    await type(input, 'vmbr1');
    expect(add.disabled).toBe(false);
    add.click();
    await settle();
    expect(fx.nics[0].network).toBe('vmbr1');
  });
});

describe('VmDetail for an Incus instance on a cluster member', () => {
  it('offers Move to member and no remote notice', async () => {
    fx.vm = {
      id: 'c1',
      name: 'c1',
      type: 'container',
      hypervisor: 'incus',
      state: 'shutoff',
      node: 'm1',
      disks: [],
      networks: [],
    };
    await open({ vmId: 'c1', node: 'm1', hv: 'incus' });
    expect(target.querySelector('[data-testid=move-member-button]')).not.toBeNull();
    expect(target.querySelector('[data-testid=remote-unsupported]')).toBeNull();
  });
});

// 8.26 minor: opening a detail asked for the console ticket twice and read
// the VM metadata twice.
describe('VmDetail open requests', () => {
  it('asks for one console ticket and one metadata read on open', async () => {
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    const count = (name) => fx.calls.filter((c) => c[0] === name).length;
    expect(count('getVM')).toBe(1);
    expect(count('getVMMeta')).toBe(1);
    expect(count('getVNCTicket')).toBeLessThanOrEqual(1);
  });

  it('asks for exactly one ticket for a running local VM', async () => {
    fx.vm = { ...fx.vm, node: '', node_id: '' };
    await open({ vmId: 'v1' });
    expect(fx.calls.filter((c) => c[0] === 'getVNCTicket').length).toBe(1);
  });
});

// 8.29 #1/#2/#5: a remote VM's class-L panels are read-only notices, their
// endpoints are never requested, and the serial console only opens when its
// tab is shown for a running VM.
describe('VmDetail remote VM: class-L panels and requests', () => {
  const count = (name) => fx.calls.filter((c) => c[0] === name).length;
  const card = (id) => target.querySelector(`section#${id}`);
  const buttons = (el) => [...el.querySelectorAll('button')].map((b) => b.textContent.trim());
  const clickTab = async (label) => {
    [...target.querySelectorAll('[role=tab]')].find((b) => b.textContent.includes(label)).click();
    await settle();
  };

  it('Firewall and Schedule show the notice with the node link and no edit controls', async () => {
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    for (const id of ['firewall', 'schedule']) {
      const el = card(id);
      expect(el, id).not.toBeNull();
      expect(el.querySelector('[data-testid=remote-unsupported]'), id).not.toBeNull();
      expect(el.querySelector('[data-testid=remote-unsupported-link]').getAttribute('href')).toBe(
        'https://192.168.1.9:8080/'
      );
      expect(el.querySelector('select'), id).toBeNull();
      expect(el.querySelector('input'), id).toBeNull();
      const b = buttons(el);
      for (const label of ['Save', 'Add rule', 'Add port forward', 'Apply template'])
        expect(
          b.some((x) => x.includes(label)),
          `${id}: ${label}`
        ).toBe(false);
    }
  });

  it('Snapshots keeps working but its automatic policy is a notice', async () => {
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    const el = card('snaps');
    expect(el.querySelector('[data-testid=remote-unsupported]')).not.toBeNull();
    expect(buttons(el).some((x) => x === 'Save')).toBe(false);
    expect(buttons(el).some((x) => x.includes('Create Snapshot'))).toBe(true);
  });

  it('never requests metrics, firewall, schedule or guest-info for a remote VM', async () => {
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    await clickTab('Guest');
    for (const name of ['getVMMetrics', 'getVMFirewall', 'getVMSchedule', 'getGuestInfo'])
      expect(count(name), name).toBe(0);
    expect(card('guest').querySelector('[data-testid=remote-unsupported]')).not.toBeNull();
  });

  it('a local VM still loads them', async () => {
    fx.vm = { ...fx.vm, node: '', node_id: '' };
    await open({ vmId: 'v1' });
    for (const name of ['getVMMetrics', 'getVMFirewall', 'getVMSchedule', 'getGuestInfo'])
      expect(count(name), name).toBe(1);
    expect(card('firewall').querySelector('[data-testid=remote-unsupported]')).toBeNull();
  });

  it('a stopped VM never opens the serial console, not even on its tab', async () => {
    fx.vm = { ...fx.vm, state: 'shutoff' };
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    expect(count('getConsoleTicket')).toBe(0);
    await clickTab('Serial');
    expect(count('getConsoleTicket')).toBe(0);
    expect(target.querySelector('[data-testid=serial-vm-off]')).not.toBeNull();
  });

  it('a running VM opens the serial console only when its tab is shown', async () => {
    await open({ vmId: 'v1', node: 'kvm-b', hv: 'kvm' });
    expect(count('getConsoleTicket')).toBe(0);
    await clickTab('Serial');
    expect(count('getConsoleTicket')).toBe(1);
    await clickTab('Overview');
    await clickTab('Serial');
    expect(count('getConsoleTicket')).toBe(1);
  });
});
