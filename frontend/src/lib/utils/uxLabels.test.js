import { describe, it, expect } from 'vitest';
import { INCUS_IMAGE_PRESETS, CUSTOM_IMAGE, labelForImage } from './incusImages.js';
import { networkLabel, networkLabelFor } from './networkLabel.js';

describe('incusImages', () => {
  it('exposes a custom option', () => {
    expect(CUSTOM_IMAGE.ref).toBe('');
  });

  it('labels known presets with friendly names', () => {
    expect(labelForImage('ubuntu:24.04')).toBe('Ubuntu 24.04 LTS');
    expect(labelForImage('images:alpine/3.20')).toBe('Alpine Linux 3.20');
  });

  it('falls back to the raw ref for custom images', () => {
    expect(labelForImage('proxmox:custom')).toBe('proxmox:custom');
    expect(labelForImage('')).toBe('');
  });

  it('every preset ref is a valid <remote>:<alias> pair', () => {
    for (const p of INCUS_IMAGE_PRESETS) {
      expect(p.ref).toMatch(/^[a-z0-9.\-/]+:[^:]+$/);
    }
  });
});

describe('networkLabel', () => {
  it('shows name with the bridge in parens', () => {
    expect(networkLabel({ name: 'Red Interna', bridge: 'vmbr0' })).toBe('Red Interna (vmbr0)');
    expect(networkLabel({ name: 'default', bridge: 'virbr0' })).toBe('default (virbr0)');
  });

  it('falls back to the name when there is no bridge', () => {
    expect(networkLabel({ name: 'lan' })).toBe('lan');
  });

  it('handles null/undefined', () => {
    expect(networkLabel(null)).toBe('');
  });
});

describe('networkLabelFor', () => {
  const networks = [
    { name: 'default', bridge: 'virbr0' },
    { name: 'Red Interna', bridge: 'vmbr0' },
  ];

  it('matches by network name (KVM ifaces)', () => {
    expect(networkLabelFor('default', networks)).toBe('default (virbr0)');
  });

  it('matches by bridge (LXD ifaces)', () => {
    expect(networkLabelFor('vmbr0', networks)).toBe('Red Interna (vmbr0)');
  });

  it('falls back to the raw identifier', () => {
    expect(networkLabelFor('lxdbr0', networks)).toBe('lxdbr0');
    expect(networkLabelFor('', networks)).toBe('');
  });
});
