import { describe, it, expect } from 'vitest';
import {
  computeTypeBadgeClass,
  computeTypeLabel,
  isContainer,
  provisionChip,
} from './computeType.js';

describe('computeTypeBadgeClass', () => {
  it('maps vm to the neutral accent styling', () => {
    expect(computeTypeBadgeClass('vm')).toContain('text-accent');
  });

  it('maps container to the amber styling', () => {
    expect(computeTypeBadgeClass('container')).toContain('#d97706');
  });

  it('falls back to vm styling for unknown types', () => {
    expect(computeTypeBadgeClass('weird')).toBe(computeTypeBadgeClass('vm'));
  });
});

describe('computeTypeLabel', () => {
  it('returns KVM / LXC labels', () => {
    expect(computeTypeLabel('vm')).toBe('KVM');
    expect(computeTypeLabel('container')).toBe('LXC');
  });
});

describe('isContainer', () => {
  it('detects a container by type', () => {
    expect(isContainer({ type: 'container', hypervisor: 'lxd' })).toBe(true);
  });

  it('detects a container by hypervisor', () => {
    expect(isContainer({ type: 'vm', hypervisor: 'lxd' })).toBe(true);
  });

  it('returns false for KVM VMs', () => {
    expect(isContainer({ type: 'vm', hypervisor: 'kvm' })).toBe(false);
  });

  it('returns false for null/undefined', () => {
    expect(isContainer(null)).toBe(false);
    expect(isContainer(undefined)).toBe(false);
  });
});

describe('provisionChip', () => {
  it('maps cloud-init to the cloud-cog icon', () => {
    expect(provisionChip('cloud-init')).toEqual({ icon: 'cloud-cog', label: 'cloud-init' });
  });

  it('maps script and seed-iso', () => {
    expect(provisionChip('script').icon).toBe('square-terminal');
    expect(provisionChip('seed-iso').icon).toBe('box');
  });

  it('returns null when there is no provisioning', () => {
    expect(provisionChip('')).toBeNull();
    expect(provisionChip('none')).toBeNull();
    expect(provisionChip(undefined)).toBeNull();
  });
});