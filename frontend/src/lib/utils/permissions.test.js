import { describe, it, expect } from 'vitest';
import {
  CAPABILITIES,
  capabilitiesForRole,
  defaultFor,
  effectiveCapabilities,
  buildPermissionsPayload,
  applyPreset,
  matchPreset,
  validateUsername,
  validatePassword,
  quotaPercent,
  limitBelowUsage,
} from './permissions.js';

describe('effectiveCapabilities', () => {
  it('inherits role defaults when a capability is not set explicitly', () => {
    const caps = effectiveCapabilities({ role: 'operator', permissions: {} });
    for (const cap of capabilitiesForRole('operator')) {
      expect(caps[cap.key]).toBe(true);
    }
  });

  it('does not show a viewer holding console by default', () => {
    // The old form hardcoded `p.can_console ?? true` for every role, so
    // the dialog claimed viewers had console access while the backend
    // denied it.
    const caps = effectiveCapabilities({ role: 'viewer', permissions: {} });
    expect(caps.console).toBe(false);
  });

  it('honours an explicit false over the role default', () => {
    const caps = effectiveCapabilities({
      role: 'operator',
      permissions: { can_console: false, can_export: false },
    });
    expect(caps.console).toBe(false);
    expect(caps.export).toBe(false);
    expect(caps.snapshots).toBe(true);
  });

  it('honours console explicitly granted to a viewer', () => {
    const caps = effectiveCapabilities({ role: 'viewer', permissions: { can_console: true } });
    expect(caps.console).toBe(true);
  });

  it('treats a missing permissions object as all-defaults', () => {
    expect(effectiveCapabilities({ role: 'operator' }).console).toBe(true);
    expect(effectiveCapabilities({ role: 'viewer' }).console).toBe(false);
    expect(effectiveCapabilities(undefined).console).toBe(true);
  });
});

describe('capabilitiesForRole', () => {
  it('offers a viewer only the capabilities that can apply to them', () => {
    expect(capabilitiesForRole('viewer').map((c) => c.key)).toEqual(['console']);
  });

  it('offers an operator every capability', () => {
    expect(capabilitiesForRole('operator')).toHaveLength(CAPABILITIES.length);
  });
});

describe('buildPermissionsPayload', () => {
  it('sends nothing for admins, who hold everything unconditionally', () => {
    expect(buildPermissionsPayload('admin', { console: false })).toBeUndefined();
  });

  it('sends every capability for an operator', () => {
    const payload = buildPermissionsPayload('operator', {
      create_vm: true,
      delete_vm: false,
      control_power: true,
      console: false,
      snapshots: true,
      backups: false,
      media: true,
      export: false,
    });
    expect(payload).toEqual({
      can_create_vm: true,
      can_delete_vm: false,
      can_control_power: true,
      can_console: false,
      can_snapshots: true,
      can_backups: false,
      can_media: true,
      can_export: false,
    });
  });

  it('omits capabilities a viewer cannot hold instead of denying them', () => {
    // Persisting can_create_vm:false for a viewer would survive a later
    // promotion to operator and silently strip a default capability.
    const payload = buildPermissionsPayload('viewer', { console: true, create_vm: true });
    expect(payload).toEqual({ can_console: true });
  });

  it('coerces missing entries to false rather than undefined', () => {
    const payload = buildPermissionsPayload('viewer', {});
    expect(payload).toEqual({ can_console: false });
  });
});

describe('presets', () => {
  it('full grants every capability available to the role', () => {
    const caps = applyPreset('operator', 'full');
    for (const cap of capabilitiesForRole('operator')) {
      expect(caps[cap.key]).toBe(true);
    }
  });

  it('readonly grants nothing', () => {
    const caps = applyPreset('operator', 'readonly');
    expect(Object.values(caps).every((v) => v === false)).toBe(true);
  });

  it('support grants day-to-day operation but never destruction or export', () => {
    const caps = applyPreset('operator', 'support');
    expect(caps.console).toBe(true);
    expect(caps.control_power).toBe(true);
    expect(caps.delete_vm).toBe(false);
    expect(caps.create_vm).toBe(false);
    expect(caps.export).toBe(false);
  });

  it('round-trips: a preset is recognised as itself', () => {
    for (const name of ['full', 'support', 'readonly']) {
      expect(matchPreset('operator', applyPreset('operator', name))).toBe(name);
    }
  });

  it('reports a hand-tuned set as custom', () => {
    const caps = applyPreset('operator', 'support');
    caps.delete_vm = true;
    expect(matchPreset('operator', caps)).toBeNull();
  });
});

describe('defaultFor', () => {
  it('gives admins everything', () => {
    for (const cap of CAPABILITIES) {
      expect(defaultFor('admin', cap.key)).toBe(true);
    }
  });

  it('gives viewers nothing, including console', () => {
    for (const cap of CAPABILITIES) {
      expect(defaultFor('viewer', cap.key)).toBe(false);
    }
  });
});

describe('validateUsername', () => {
  it('rejects names that render identically to an existing account', () => {
    // " admin " previously passed validation and was stored untrimmed,
    // producing a second administrator indistinguishable in every list.
    expect(validateUsername(' admin ')).toBe('whitespace');
    expect(validateUsername('admin ')).toBe('whitespace');
    expect(validateUsername(' admin')).toBe('whitespace');
  });

  it('rejects empty and blank names', () => {
    expect(validateUsername('')).toBe('required');
    expect(validateUsername('   ')).toBe('whitespace');
  });

  it('rejects characters outside the allowed set', () => {
    expect(validateUsername('ad min')).toBe('charset');
    expect(validateUsername('\u0430dmin')).toBe('charset'); // Cyrillic a
    expect(validateUsername('admin\u200b')).toBe('charset');
    expect(validateUsername('-admin')).toBe('charset');
    expect(validateUsername('admin-')).toBe('charset');
  });

  it('accepts ordinary names', () => {
    for (const n of ['demo', 'jane.doe', 'ops-team', 'user_1', 'a', 'A1']) {
      expect(validateUsername(n)).toBeNull();
    }
  });

  it('rejects names beyond the length limit', () => {
    expect(validateUsername('a'.repeat(65))).toBe('tooLong');
    expect(validateUsername('a'.repeat(64))).toBeNull();
  });
});

describe('validatePassword', () => {
  it('rejects passwords the backend would reject for length', () => {
    expect(validatePassword('')).toBe('required');
    expect(validatePassword('Ab1!xy')).toBe('tooShort');
  });

  it('rejects short passwords with too few character classes', () => {
    // The old form only checked length >= 8, so this reached the API and
    // came back as an error after the whole form had been filled in.
    expect(validatePassword('password')).toBe('complexity');
    expect(validatePassword('password123')).toBe('complexity');
    expect(validatePassword('PASSWORD123')).toBe('complexity');
  });

  it('accepts a short password with three classes', () => {
    expect(validatePassword('Str0ng-Pass')).toBeNull();
    expect(validatePassword('Passw0rd')).toBeNull();
  });

  it('drops the complexity rule at 16 characters, like the backend', () => {
    expect(validatePassword('correcthorsebatterystaple')).toBeNull();
    expect(validatePassword('a'.repeat(16))).toBeNull();
    expect(validatePassword('a'.repeat(15))).toBe('complexity');
  });

  it('rejects passwords beyond the length limit', () => {
    expect(validatePassword('a'.repeat(129))).toBe('tooLong');
  });
});

describe('quota helpers', () => {
  it('treats a zero limit as unlimited rather than as full', () => {
    expect(quotaPercent(10, 0)).toBe(0);
    expect(limitBelowUsage(10, 0)).toBe(false);
  });

  it('computes consumption as a percentage', () => {
    expect(quotaPercent(6, 8)).toBe(75);
    expect(quotaPercent(0, 8)).toBe(0);
  });

  it('caps at 100 when usage already exceeds the limit', () => {
    expect(quotaPercent(20, 8)).toBe(100);
  });

  it('flags a limit set below what is already used', () => {
    expect(limitBelowUsage(6, 4)).toBe(true);
    expect(limitBelowUsage(6, 6)).toBe(false);
    expect(limitBelowUsage(6, 8)).toBe(false);
  });
});
