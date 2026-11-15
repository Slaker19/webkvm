import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({ calls: [], failWith: null }));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const ok =
    (name) =>
    (...args) => {
      fx.calls.push([name, ...args]);
      return fx.failWith ? Promise.reject(fx.failWith) : Promise.resolve({ status: 'ok' });
    };
  const api = {
    moveVM: ok('moveVM'),
    evacuateIncusMember: ok('evacuate'),
    restoreIncusMember: ok('restore'),
  };
  return { api, auth: { isAdmin: () => true, canMutate: () => true, role: 'admin' } };
});
vi.mock('$lib/router.svelte.js', () => ({ navigate: vi.fn(), getRoute: () => ({ query: {} }) }));

import NodeHealthBanner from './components/NodeHealthBanner.svelte';
import RemoteNodeBanner from './components/RemoteNodeBanner.svelte';
import RemoteUnsupportedNotice from './components/RemoteUnsupportedNotice.svelte';
import ClusterOverviewStrip from './components/ClusterOverviewStrip.svelte';
import MoveMemberDialog from './components/MoveMemberDialog.svelte';
import IncusMemberOps from './components/IncusMemberOps.svelte';
import VmNodeCell from './components/vm-list/VmNodeCell.svelte';
import { navigate } from '$lib/router.svelte.js';

const settle = async () => {
  for (let i = 0; i < 6; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};
const btn = (root, text) =>
  [...root.querySelectorAll('button')].find((b) => (b.textContent || '').trim().includes(text));

let mounted = null;
async function render(Comp, props) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(Comp, { target, props });
  mounted = { app, target };
  await settle();
  return target;
}

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  fx.calls = [];
  fx.failWith = null;
  navigate.mockClear?.();
});
afterEach(() => {
  if (mounted) unmount(mounted.app);
  mounted?.target.remove();
  mounted = null;
  document.body.innerHTML = '';
});

describe('NodeHealthBanner', () => {
  const issues = [
    { id: 'a', name: 'kvm-a', kind: 'stale', code: 'host_key_changed', access: true },
    { id: 'b', name: 'kvm-b', kind: 'offline', code: 'node_unreachable', access: false },
  ];
  it('lists nodes with translated codes and links to Cluster access', async () => {
    const root = await render(NodeHealthBanner, { issues });
    expect(root.textContent).toContain('kvm-a: showing last known data');
    expect(root.textContent).toContain('identity of the node changed');
    expect(root.textContent).toContain('kvm-b: offline');
    btn(root, 'Fix in Cluster access').click();
    expect(navigate).toHaveBeenCalledWith('/nodes', { query: { focus: 'access' } });
  });
  it('is dismissible and renders nothing without issues', async () => {
    const root = await render(NodeHealthBanner, { issues });
    btn(root, 'Dismiss').click();
    await settle();
    expect(root.querySelector('[data-testid=node-health-banner]')).toBeNull();
    unmount(mounted.app);
    const empty = await render(NodeHealthBanner, { issues: [] });
    expect(empty.querySelector('[data-testid=node-health-banner]')).toBeNull();
  });
});

describe('RemoteNodeBanner', () => {
  const vm = { id: 'v', name: 'web', node: 'kvm-a', node_id: 'n1', hypervisor: 'kvm' };
  it('shows the node, its state and the access shortcut on ssh errors', async () => {
    const root = await render(RemoteNodeBanner, {
      vm,
      node: { id: 'n1', name: 'kvm-a', status: 'online', enabled: true },
      error: { code: 'ssh_auth_denied', data: { node: 'kvm-a' } },
    });
    expect(root.textContent).toContain('This VM lives on node kvm-a');
    expect(root.textContent).toContain('refused WebKVM');
    btn(root, 'Open Cluster access').click();
    expect(navigate).toHaveBeenCalled();
  });
  it('has no access button for unrelated errors', async () => {
    const root = await render(RemoteNodeBanner, { vm, error: { code: 'node_timeout' } });
    expect(btn(root, 'Open Cluster access')).toBeUndefined();
  });
});

describe('RemoteUnsupportedNotice', () => {
  it('shows the translated notice with a link when the node UI is derivable', async () => {
    const root = await render(RemoteUnsupportedNotice, {
      node: { name: 'kvm-a', uri: 'qemu+ssh://root@node9.example/system' },
    });
    expect(root.textContent).toContain("only available from that node's own WebKVM");
    expect(root.querySelector('a').getAttribute('href')).toBe('https://node9.example:8080/');
  });
  it('has no link without a node address', async () => {
    const root = await render(RemoteUnsupportedNotice, { nodeName: 'x' });
    expect(root.querySelector('a')).toBeNull();
  });
});

describe('ClusterOverviewStrip', () => {
  it('shows the counts and the effect of disabling', async () => {
    const root = await render(ClusterOverviewStrip, {
      nodes: [
        { enabled: true, status: 'online' },
        { enabled: false, status: 'offline' },
      ],
      vms: [{ managed: false }],
    });
    expect(root.querySelector('[data-testid=overview-enabled]').textContent.trim()).toBe('1');
    expect(root.querySelector('[data-testid=overview-disabled]').textContent.trim()).toBe('1');
    expect(root.querySelector('[data-testid=overview-unmanaged]').textContent.trim()).toBe('1');
    expect(root.textContent).toContain('hidden from lists and refuse operations');
  });
});

describe('VmNodeCell', () => {
  it('offline member and stale badges, managed toggle calls back', async () => {
    const onToggle = vi.fn();
    const vm = {
      id: 'v',
      name: 'c1',
      node: 'm2',
      member_online: false,
      stale: true,
      managed: true,
    };
    const root = await render(VmNodeCell, {
      vm,
      nodeLabel: 'm2',
      health: 'stale',
      onToggleManaged: onToggle,
    });
    expect(root.querySelector('[data-testid=vm-member-offline]').textContent).toContain(
      'Member offline'
    );
    expect(root.querySelector('[data-testid=vm-stale-badge]')).not.toBeNull();
    const sw = root.querySelector('[data-testid=vm-managed-toggle]');
    expect(sw.getAttribute('aria-checked')).toBe('true');
    sw.click();
    expect(onToggle).toHaveBeenCalledWith(vm, false);
  });
});

describe('MoveMemberDialog', () => {
  const members = [
    { name: 'm1', status: 'Online' },
    { name: 'm2', status: 'Online' },
    { name: 'm3', status: 'Offline' },
  ];
  it('offers only other online members and explains a running non-live move', async () => {
    await render(MoveMemberDialog, {
      open: true,
      vm: {
        id: 'c1',
        name: 'c1',
        node: 'm1',
        type: 'container',
        state: 'running',
        hypervisor: 'incus',
      },
      members,
    });
    const opts = [...document.querySelectorAll('#move-member option')].map((o) => o.value);
    expect(opts).toEqual(['m2']);
    expect(document.body.textContent).toContain('Stop it first');
    expect(document.querySelector('#move-live')).toBeNull(); // containers: no live
  });
  it('moves a stopped VM with live=false and translates 409 codes', async () => {
    const vm = {
      id: 'v1',
      name: 'v1',
      node: 'm1',
      type: 'vm',
      state: 'shutoff',
      hypervisor: 'incus',
    };
    await render(MoveMemberDialog, { open: true, vm, members });
    fx.failWith = { code: 'member_offline', message: 'raw', status: 409 };
    btn(document.body, 'Move').click();
    await settle();
    expect(fx.calls[0]).toEqual(['moveVM', 'v1', 'm2', false, 'm1', 'incus']);
    expect(document.querySelector('[data-testid=move-error]').textContent).toContain('offline');
  });
  it('live toggle exists for VMs', async () => {
    await render(MoveMemberDialog, {
      open: true,
      vm: { id: 'v1', name: 'v1', node: 'm1', type: 'vm', state: 'running', hypervisor: 'incus' },
      members,
    });
    expect(document.body.textContent).toContain(
      'Stop it first'.slice(0, 0) + 'turn on live migration'
    );
    const live = document.querySelector('#move-live');
    live.click();
    await settle();
    expect(document.querySelector('[data-testid=move-must-stop]')).toBeNull();
  });
});

describe('IncusMemberOps', () => {
  it('disables both actions with a reason when offline', async () => {
    const root = await render(IncusMemberOps, { member: { name: 'm3', status: 'Offline' } });
    expect(btn(root, 'Evacuate').disabled).toBe(true);
    expect(btn(root, 'Restore').disabled).toBe(true);
    expect(root.textContent).toContain('member is offline');
  });
  it('evacuate asks for confirmation, advanced mode picks the mode', async () => {
    const root = await render(IncusMemberOps, { member: { name: 'm1', status: 'Online' } });
    expect(btn(root, 'Restore').disabled).toBe(true);
    btn(root, 'Evacuate').click();
    await settle();
    expect(document.body.textContent).toContain('Evacuate member m1?');
    document.querySelector('[data-testid=member-advanced]').click();
    await settle();
    const sel = document.querySelector('#evac-mode-m1');
    [...sel.options].find((o) => o.value === 'stop').selected = true;
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    const confirm = [...document.querySelectorAll('button')].filter(
      (b) => (b.textContent || '').trim() === 'Evacuate'
    );
    confirm[confirm.length - 1].click();
    await settle();
    expect(fx.calls[0]).toEqual(['evacuate', 'm1', 'stop', false]);
  });
  it('shows the Pacemaker conflict and offers force only in advanced mode', async () => {
    const root = await render(IncusMemberOps, { member: { name: 'm1', status: 'Online' } });
    btn(root, 'Evacuate').click();
    await settle();
    fx.failWith = {
      code: 'healing_managed_by_pacemaker',
      status: 409,
      data: { workloads: ['incus:default/web'] },
    };
    document.querySelector('[data-testid=member-advanced]').click();
    await settle();
    const confirm = [...document.querySelectorAll('button')].filter(
      (b) => (b.textContent || '').trim() === 'Evacuate'
    );
    confirm[confirm.length - 1].click();
    await settle();
    expect(document.querySelector('[data-testid=member-error]').textContent).toContain('Pacemaker');
    expect(document.body.textContent).toContain('incus:default/web');
    expect(document.body.textContent).toContain('Force anyway');
  });
});
