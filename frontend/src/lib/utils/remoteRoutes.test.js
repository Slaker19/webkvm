import { describe, it, expect } from 'vitest';
import {
  tabSupport,
  isRemoteVm,
  vmQuery,
  nodeIssues,
  nodeHealth,
  moveTargets,
  moveNeedsStop,
  memberOpBlock,
  clusterOverview,
  remoteErrorText,
  isRemoteUnsupported,
  needsClusterAccess,
  REMOTE_ERROR_CODES,
} from './remoteRoutes.js';
import { messages } from '../i18n.svelte.js';

const kvm = { node: 'n2', node_id: 'id2', hypervisor: 'kvm' };

describe('tabSupport', () => {
  it('local VMs and Incus members are fully supported', () => {
    expect(tabSupport('cloudinit', { node: 'host', hypervisor: 'kvm' })).toBe('full');
    expect(tabSupport('alerts', { node: 'm1', hypervisor: 'incus' })).toBe('full');
  });
  it('remote KVM follows the route-class map', () => {
    expect(tabSupport('console', kvm)).toBe('full');
    expect(tabSupport('terminal', kvm)).toBe('full');
    expect(tabSupport('snaps', kvm)).toBe('full');
    expect(tabSupport('cloudinit', kvm)).toBe('none');
    expect(tabSupport('alerts', kvm)).toBe('none');
    expect(tabSupport('hardware', kvm)).toBe('partial');
  });
  it('registered remote Incus keeps the narrow list', () => {
    const inc = { node: 'x', node_id: 'i', hypervisor: 'incus' };
    expect(tabSupport('snaps', inc)).toBe('partial');
    expect(tabSupport('console', inc)).toBe('none');
  });
});

describe('selectors', () => {
  it('vmQuery carries node and hv', () => {
    expect(vmQuery(kvm)).toEqual({ node: 'n2', hv: 'kvm' });
    expect(vmQuery({ node: 'm1', hypervisor: 'incus' })).toEqual({ node: 'm1', hv: 'incus' });
    expect(isRemoteVm({ node: 'm1' })).toBe(false);
  });
});

describe('node health', () => {
  it('stale wins over online', () => {
    expect(nodeHealth({ status: 'online', enabled: true }, [{ stale: true }])).toBe('stale');
    expect(nodeHealth({ status: 'degraded', enabled: true }, [])).toBe('degraded');
    expect(nodeHealth(null, [])).toBe('online');
  });
  it('nodeIssues lists offline nodes and stale-only nodes with codes', () => {
    const nodes = [
      { id: 'a', name: 'A', type: 'remote', enabled: true, status: 'offline' },
      { id: 'b', name: 'B', type: 'remote', enabled: true, status: 'online' },
      { id: 'c', name: 'C', type: 'local', enabled: true, status: 'online' },
    ];
    const vms = [{ node: 'B', node_id: 'b', stale: true }];
    const issues = nodeIssues(vms, nodes, { b: { code: 'host_key_changed' } });
    expect(issues.map((i) => [i.id, i.kind, i.code, i.access])).toEqual([
      ['a', 'offline', 'node_unreachable', false],
      ['b', 'stale', 'host_key_changed', true],
    ]);
  });
});

describe('incus cluster helpers', () => {
  const members = [
    { name: 'm1', status: 'Online' },
    { name: 'm2', status: 'Online' },
    { name: 'm3', status: 'Offline' },
  ];
  it('moveTargets excludes the current and offline members', () => {
    expect(moveTargets(members, { node: 'M1' }).map((m) => m.name)).toEqual(['m2']);
  });
  it('a running instance needs stop unless live', () => {
    expect(moveNeedsStop({ state: 'running' }, false)).toBe(true);
    expect(moveNeedsStop({ state: 'running' }, true)).toBe(false);
    expect(moveNeedsStop({ state: 'shutoff' }, false)).toBe(false);
  });
  it('memberOpBlock gives a reason', () => {
    expect(memberOpBlock({ status: 'Offline' }, 'evacuate')).toBe('offline');
    expect(memberOpBlock({ status: 'Online' }, 'restore')).toBe('notEvacuated');
    expect(memberOpBlock({ status: 'Online' }, 'evacuate', true)).toBe('ha');
    expect(memberOpBlock({ status: 'Online' }, 'evacuate')).toBe('');
  });
});

describe('clusterOverview', () => {
  it('counts', () => {
    const o = clusterOverview(
      [
        { enabled: true, status: 'online' },
        { enabled: true, status: 'degraded' },
        { enabled: false, status: 'offline' },
      ],
      [{ managed: false }, { managed: true }, {}]
    );
    expect(o).toEqual({ enabled: 2, disabled: 1, degraded: 1, unmanaged: 1 });
  });
});

describe('error translation', () => {
  it('every known code has en, es and ca text', () => {
    for (const loc of ['en', 'es', 'ca']) {
      for (const code of REMOTE_ERROR_CODES) {
        expect(messages[loc].remote.errors[code], `${loc}.${code}`).toBeTruthy();
      }
    }
  });
  it('remoteErrorText falls back to the backend message for unknown codes', () => {
    const t = (k) => k;
    expect(remoteErrorText(t, { code: 'zzz', message: 'raw' })).toBe('raw');
  });
  it('detects remote_unsupported and access codes', () => {
    expect(isRemoteUnsupported({ code: 'remote_unsupported' })).toBe(true);
    expect(isRemoteUnsupported({ code: 'x' })).toBe(false);
    expect(needsClusterAccess('no_key')).toBe(true);
    expect(needsClusterAccess('node_timeout')).toBe(false);
  });
});

// 8.29 #2: the detail page must not ask a remote node for class-L reads.
describe('remoteLoadUnsupported', () => {
  it('is true for the class-L reads of a remote VM only', async () => {
    const { remoteLoadUnsupported, REMOTE_UNSUPPORTED_LOADS } = await import('./remoteRoutes.js');
    expect(REMOTE_UNSUPPORTED_LOADS).toEqual(['metrics', 'firewall', 'schedule', 'guest-info']);
    for (const w of REMOTE_UNSUPPORTED_LOADS) expect(remoteLoadUnsupported(kvm, w)).toBe(true);
    expect(remoteLoadUnsupported(kvm, 'snapshots')).toBe(false);
    expect(remoteLoadUnsupported({ node: 'host', hypervisor: 'kvm' }, 'metrics')).toBe(false);
    expect(remoteLoadUnsupported(null, 'metrics')).toBe(false);
  });
});
