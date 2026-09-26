import { describe, it, expect } from 'vitest';
import { hasModel, pickModel, flagState, withFlagState } from './capabilities.js';

describe('hasModel', () => {
  const supported = ['vga', 'virtio', 'bochs'];

  it('matches an exact value', () => {
    expect(hasModel(supported, 'virtio')).toBe(true);
  });

  it('matches case-insensitively (UI shows "VGA", XML wants "vga")', () => {
    expect(hasModel(supported, 'VGA')).toBe(true);
    expect(hasModel(supported, 'Vga')).toBe(true);
  });

  it('rejects a value the host does not provide', () => {
    expect(hasModel(supported, 'qxl')).toBe(false);
  });

  it('does not match on a prefix', () => {
    expect(hasModel(supported, 'virt')).toBe(false);
  });

  // Permissive degradation: an unprobeable host must not empty the UI.
  it('treats an empty supported list as unknown (permissive)', () => {
    expect(hasModel([], 'qxl')).toBe(true);
    expect(hasModel(null, 'qxl')).toBe(true);
    expect(hasModel(undefined, 'qxl')).toBe(true);
  });

  // "" and "none" mean "omit the device", never "validate this device".
  it('always allows empty and "none"', () => {
    expect(hasModel(supported, '')).toBe(true);
    expect(hasModel(supported, 'none')).toBe(true);
    expect(hasModel(supported, null)).toBe(true);
  });
});

describe('pickModel', () => {
  it('returns the preferred model when available', () => {
    expect(pickModel(['qxl', 'virtio'], 'qxl', ['virtio'])).toBe('qxl');
  });

  // The core QXL fix: a host without qxl falls through to virtio.
  it('falls back to the first available alternative', () => {
    expect(pickModel(['vga', 'virtio', 'bochs'], 'qxl', ['virtio', 'vmvga', 'vga'])).toBe('virtio');
  });

  it('skips unavailable alternatives in order', () => {
    expect(pickModel(['vga'], 'qxl', ['virtio', 'vmvga', 'vga'])).toBe('vga');
  });

  it('returns the preferred value when nothing matches', () => {
    expect(pickModel(['only'], 'qxl', ['virtio', 'vga'])).toBe('qxl');
  });

  it('ignores a fallback that duplicates the preferred value', () => {
    expect(pickModel(['virtio'], 'virtio', ['virtio', 'vga'])).toBe('virtio');
  });

  it('is permissive when capabilities are unknown', () => {
    expect(pickModel([], 'qxl', ['virtio'])).toBe('qxl');
  });

  it('handles a preference chain with no fallbacks', () => {
    expect(pickModel(['virtio'], 'virtio')).toBe('virtio');
    expect(pickModel(['vga'], 'qxl')).toBe('qxl');
  });
});

describe('flagState', () => {
  it('reads "on" from a +flag entry', () => {
    expect(flagState(['+aes', '-hypervisor'], 'aes')).toBe('on');
  });

  it('reads "off" from a -flag entry', () => {
    expect(flagState(['+aes', '-hypervisor'], 'hypervisor')).toBe('off');
  });

  it('defaults to "auto" when the flag is absent', () => {
    expect(flagState(['+aes'], 'avx2')).toBe('auto');
  });

  it('defaults to "auto" for a non-array (unset cpu_flags)', () => {
    expect(flagState(undefined, 'aes')).toBe('auto');
    expect(flagState(null, 'aes')).toBe('auto');
  });

  it('defaults to "auto" for an empty array', () => {
    expect(flagState([], 'aes')).toBe('auto');
  });
});

describe('withFlagState', () => {
  it('adds a +flag entry for "on"', () => {
    expect(withFlagState([], 'aes', 'on')).toEqual(['+aes']);
  });

  it('adds a -flag entry for "off"', () => {
    expect(withFlagState([], 'aes', 'off')).toEqual(['-aes']);
  });

  it('removes any existing entry for "auto"', () => {
    expect(withFlagState(['+aes', '+avx2'], 'aes', 'auto')).toEqual(['+avx2']);
    expect(withFlagState(['-aes'], 'aes', 'auto')).toEqual([]);
  });

  // Switching directly from "on" to "off" must replace, not duplicate.
  it('replaces on with off without leaving a stale entry', () => {
    expect(withFlagState(['+aes'], 'aes', 'off')).toEqual(['-aes']);
  });

  it('replaces off with on without leaving a stale entry', () => {
    expect(withFlagState(['-aes'], 'aes', 'on')).toEqual(['+aes']);
  });

  it('leaves other flags untouched', () => {
    expect(withFlagState(['+aes', '-topoext'], 'avx2', 'on')).toEqual([
      '+aes',
      '-topoext',
      '+avx2',
    ]);
  });

  it('treats a non-array input as empty', () => {
    expect(withFlagState(undefined, 'aes', 'on')).toEqual(['+aes']);
    expect(withFlagState(null, 'aes', 'off')).toEqual(['-aes']);
  });

  // Round-trip: setting then reading back should agree.
  it('round-trips through flagState for every state', () => {
    for (const s of ['on', 'off', 'auto']) {
      const result = withFlagState(['+preexisting'], 'aes', s);
      expect(flagState(result, 'aes')).toBe(s);
    }
  });
});
