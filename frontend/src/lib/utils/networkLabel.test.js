import { describe, it, expect } from 'vitest';
import { networkLabel, networkLabelFor, preselectNetwork } from './networkLabel.js';

describe('networkLabel', () => {
  it('returns empty string for nil input', () => {
    expect(networkLabel(null)).toBe('');
    expect(networkLabel(undefined)).toBe('');
  });

  it('returns just the name when bridge equals name or is absent', () => {
    expect(networkLabel({ name: 'vmbr0', bridge: 'vmbr0' })).toBe('vmbr0');
    expect(networkLabel({ name: 'vmbr0' })).toBe('vmbr0');
  });

  it('includes the underlying bridge in parentheses when they differ', () => {
    expect(networkLabel({ name: 'LAN', bridge: 'vmbr0' })).toBe('LAN (vmbr0)');
  });
});

describe('networkLabelFor', () => {
  const networks = [
    { name: 'LAN', bridge: 'vmbr0' },
    { name: 'isolated', bridge: 'br-iso' },
  ];

  it('matches by name or bridge', () => {
    expect(networkLabelFor('LAN', networks)).toBe('LAN (vmbr0)');
    expect(networkLabelFor('vmbr0', networks)).toBe('LAN (vmbr0)');
  });

  it('falls back to raw identifier when not found or list is empty', () => {
    expect(networkLabelFor('unknown', networks)).toBe('unknown');
    expect(networkLabelFor('eth0', null)).toBe('eth0');
    expect(networkLabelFor('', networks)).toBe('');
  });
});

describe('preselectNetwork', () => {
  it('defensively handles non-array inputs without throwing', () => {
    // This is the exact bug reported: Promise.allSettled produces
    // { status: 'fulfilled', value: [...] } instead of an array.
    // Calling .find() directly on it threw "v.find is not a function",
    // aborting onMount and blanking the VM creation screen.
    expect(preselectNetwork(null)).toBe('');
    expect(preselectNetwork(undefined)).toBe('');
    expect(preselectNetwork({})).toBe('');
    expect(preselectNetwork({ status: 'fulfilled', value: [] })).toBe('');
    expect(preselectNetwork('vmbr0')).toBe('');
    expect(preselectNetwork(42)).toBe('');
  });

  it('prefers vmbr0 over other bridges', () => {
    const nets = [
      { name: 'br0', bridge: 'br0' },
      { name: 'vmbr0', bridge: 'vmbr0' },
      { name: 'virbr0', bridge: 'virbr0' },
    ];
    expect(preselectNetwork(nets)).toBe('vmbr0');
  });

  it('prefers a network wired to bridge vmbr0 even if named differently', () => {
    const nets = [
      { name: 'DefaultNet', bridge: 'vmbr0', forward: 'direct' },
      { name: 'br0', bridge: 'br0' },
    ];
    expect(preselectNetwork(nets)).toBe('DefaultNet');
  });

  it('falls back to br0 when vmbr0 is not present', () => {
    const nets = [
      { name: 'br0', bridge: 'br0' },
      { name: 'virbr0', bridge: 'virbr0' },
    ];
    expect(preselectNetwork(nets)).toBe('br0');
  });

  it('never picks NAT or virtual bridges (virbr, lxdbr, incusbr, docker, br-)', () => {
    const virtualOnly = [
      { name: 'default', bridge: 'virbr0', forward: 'nat' },
      { name: 'lxdbr0', bridge: 'lxdbr0' },
      { name: 'lxcbr0', bridge: 'lxcbr0' },
      { name: 'incusbr0', bridge: 'incusbr0' },
      { name: 'docker0', bridge: 'docker0' },
      { name: 'br-custom', bridge: 'br-custom' },
    ];
    expect(preselectNetwork(virtualOnly)).toBe('');
  });

  it('preselects the Incus bridge only for containers that allow NAT', () => {
    const nets = [{ name: 'incusbr0', bridge: 'incusbr0' }];
    expect(preselectNetwork(nets, { allowIncus: true })).toBe('incusbr0');
    expect(preselectNetwork(nets)).toBe('');
  });

  it('still prefers a physical bridge over an Incus bridge', () => {
    const nets = [
      { name: 'incusbr0', bridge: 'incusbr0' },
      { name: 'vmbr0', bridge: 'vmbr0' },
    ];
    expect(preselectNetwork(nets, { allowIncus: true })).toBe('vmbr0');
  });

  it('picks physical bridge when physical bridge has non-standard name', () => {
    const nets = [
      { name: 'virbr0', bridge: 'virbr0' },
      { name: 'corp_lan', bridge: 'eth0_br' },
    ];
    expect(preselectNetwork(nets)).toBe('corp_lan');
  });

  it('skips invalid items in the array gracefully', () => {
    const nets = [null, undefined, {}, { name: 123 }, { name: 'vmbr0' }];
    expect(preselectNetwork(nets)).toBe('vmbr0');
  });
});

// 8.29 #6 / 8.30: the wizard names host bridges and libvirt networks apart.
describe('networkKind / networkGroups / networkOptionLabel', () => {
  it('tells host bridges from libvirt NAT/isolated networks', async () => {
    const { networkKind } = await import('./networkLabel.js');
    expect(networkKind({ name: 'vmbr0', kind: 'direct', bridge: 'vmbr0' })).toBe('host');
    expect(networkKind({ name: 'vmbr1', bridge: 'vmbr1' })).toBe('host');
    expect(networkKind({ name: 'br0', forward: 'bridge' })).toBe('host');
    expect(networkKind({ name: 'virbr0', kind: 'nat', bridge: 'virbr0' })).toBe('nat');
    expect(networkKind({ name: 'virbr0' })).toBe('nat');
    expect(networkKind({ name: 'iso1', kind: 'isolated' })).toBe('isolated');
  });
  it('names Incus bridges as Incus, not as libvirt networks', async () => {
    const { networkKind } = await import('./networkLabel.js');
    expect(networkKind({ name: 'incusbr0', kind: 'nat', bridge: 'incusbr0' })).toBe('incus');
    expect(networkKind({ name: 'incusbr1' })).toBe('incus');
    expect(networkKind({ name: 'lxdbr0' })).toBe('incus');
  });
  it('groups host bridges first', async () => {
    const { networkGroups } = await import('./networkLabel.js');
    const g = networkGroups([
      { name: 'virbr0', kind: 'nat' },
      { name: 'vmbr0', kind: 'direct' },
      { name: 'iso1', kind: 'isolated' },
    ]);
    expect(g.host.map((n) => n.name)).toEqual(['vmbr0']);
    expect(g.virtual.map((n) => n.name)).toEqual(['virbr0', 'iso1']);
    expect(networkGroups(null)).toEqual({ host: [], virtual: [] });
  });
  it('labels say what the VM gets', async () => {
    const { networkOptionLabel } = await import('./networkLabel.js');
    const t = (k, v) => `${k}|${v.name}`;
    expect(networkOptionLabel(t, { name: 'vmbr0', kind: 'direct' })).toBe(
      'vmCreate.netOptHost|vmbr0'
    );
    expect(networkOptionLabel(t, { name: 'virbr0', kind: 'nat' })).toBe(
      'vmCreate.netOptNat|virbr0'
    );
    expect(networkOptionLabel(t, { name: 'iso1', kind: 'isolated' })).toBe(
      'vmCreate.netOptIsolated|iso1'
    );
    expect(networkOptionLabel(t, { name: 'incusbr0', kind: 'nat' })).toBe(
      'vmCreate.netOptIncus|incusbr0'
    );
  });
});
