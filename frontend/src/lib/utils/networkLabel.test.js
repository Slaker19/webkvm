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

  it('never picks NAT or virtual bridges (virbr, lxdbr, docker, br-)', () => {
    const virtualOnly = [
      { name: 'default', bridge: 'virbr0', forward: 'nat' },
      { name: 'lxdbr0', bridge: 'lxdbr0' },
      { name: 'lxcbr0', bridge: 'lxcbr0' },
      { name: 'docker0', bridge: 'docker0' },
      { name: 'br-custom', bridge: 'br-custom' },
    ];
    expect(preselectNetwork(virtualOnly)).toBe('');
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
