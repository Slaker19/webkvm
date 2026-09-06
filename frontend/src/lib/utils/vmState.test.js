import { describe, it, expect } from 'vitest';
import { stateDotClass, stateBadgeClass } from './vmState.js';

describe('stateDotClass', () => {
  it('maps the known libvirt states', () => {
    expect(stateDotClass('running')).toBe('bg-status-running');
    expect(stateDotClass('shutoff')).toBe('bg-status-shutoff');
    expect(stateDotClass('paused')).toBe('bg-status-paused');
    expect(stateDotClass('crashed')).toBe('bg-status-crashed');
  });

  it('falls back to the neutral grey for unknown states', () => {
    // Unknown states must never render as "crashed" (red).
    expect(stateDotClass('blocked')).toBe('bg-status-shutoff');
    expect(stateDotClass('idle')).toBe('bg-status-shutoff');
    expect(stateDotClass('pmsuspended')).toBe('bg-status-shutoff');
    expect(stateDotClass(undefined)).toBe('bg-status-shutoff');
    expect(stateDotClass(null)).toBe('bg-status-shutoff');
  });
});

describe('stateBadgeClass', () => {
  it('maps the known states', () => {
    expect(stateBadgeClass('running')).toBe('badge-running');
    expect(stateBadgeClass('crashed')).toBe('badge-crashed');
  });

  it('falls back safely for unknown/null states (V12-FE-04)', () => {
    expect(stateBadgeClass('bogus')).toBe('badge-shutoff');
    expect(stateBadgeClass(null)).toBe('badge-shutoff');
    expect(stateBadgeClass(undefined)).toBe('badge-shutoff');
  });
});
