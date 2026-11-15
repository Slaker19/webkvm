import { describe, it, expect } from 'vitest';
import {
  accessState,
  accessBlocksCreate,
  currentStep,
  withProbeAccess,
  ACCESS_FRESH_MS,
} from './clusterAccess.js';

const now = Date.parse('2026-11-12T12:00:00Z');
const ago = (ms) => new Date(now - ms).toISOString();
const pinned = { host_key: { state: 'pinned' } };

describe('accessState', () => {
  it('is ready for a pinned node with a fresh passing test', () => {
    expect(accessState({ ...pinned, last_test: { ok: true, at: ago(60_000) } }, now)).toBe('ready');
  });
  it('is unverified (not "not set up") when the test is missing or stale', () => {
    expect(accessState(pinned, now)).toBe('unverified');
    const stale = { ...pinned, last_test: { ok: true, at: ago(ACCESS_FRESH_MS + 1000) } };
    expect(accessState(stale, now)).toBe('unverified');
    expect(accessState({ ...pinned, last_test: { ok: true } }, now)).toBe('unverified');
  });
  it('treats only auth/key/host-key codes as setup problems', () => {
    for (const code of ['ssh_auth_denied', 'no_key', 'host_key_unpinned']) {
      const n = { ...pinned, last_test: { ok: false, code, at: ago(1000) } };
      expect(accessState(n, now)).toBe('not_set_up');
    }
    for (const code of ['node_unreachable', 'node_timeout', 'libvirt_unavailable']) {
      const n = { ...pinned, last_test: { ok: false, code, at: ago(1000) } };
      expect(accessState(n, now)).toBe('unverified');
    }
  });
  it('flags host key problems and an unpinned key', () => {
    expect(accessState({ host_key: { state: 'changed' } }, now)).toBe('changed');
    expect(
      accessState({ ...pinned, last_test: { ok: false, code: 'host_key_changed' } }, now)
    ).toBe('changed');
    expect(accessState({ host_key: { state: 'unknown' } }, now)).toBe('not_set_up');
    expect(accessState(null, now)).toBe('not_set_up');
  });
  it('only setup problems block creation', () => {
    expect(accessBlocksCreate('ready')).toBe(false);
    expect(accessBlocksCreate('unverified')).toBe(false);
    expect(accessBlocksCreate('not_set_up')).toBe(true);
    expect(accessBlocksCreate('changed')).toBe(true);
  });
  // 8.26 defect 1: the periodic node probe (GET /nodes access:'ok') already
  // logs in over SSH. It is the same truth for every screen: never "not
  // verified" next to a probe that just succeeded.
  it('is ready when the node-list probe says access ok, even with a stale or missing test', () => {
    const stale = { ...pinned, last_test: { ok: true, at: ago(ACCESS_FRESH_MS * 30) } };
    expect(accessState({ ...stale, access: 'ok' }, now)).toBe('ready');
    expect(accessState({ ...pinned, access: 'ok' }, now)).toBe('ready');
  });
  it('is not set up when the probe says so, and a changed key still wins', () => {
    expect(accessState({ ...pinned, access: 'not_set_up' }, now)).toBe('not_set_up');
    expect(accessState({ host_key: { state: 'changed' }, access: 'ok' }, now)).toBe('changed');
  });
});

describe('currentStep with the probe', () => {
  it('is done when the probe proves access, even without an explicit test', () => {
    expect(currentStep({ configured: true }, { ...pinned, access: 'ok' })).toBe('done');
    expect(currentStep({ configured: true }, pinned)).toBe('test');
  });
});

describe('withProbeAccess', () => {
  it('merges the registry probe verdict into an SSH overview row', () => {
    const row = { id: 'n1', ...pinned };
    expect(withProbeAccess(row, { id: 'n1', access: 'ok' }).access).toBe('ok');
    // The overview's own value is kept when the registry has none.
    expect(withProbeAccess({ ...row, access: 'ok' }, { id: 'n1' }).access).toBe('ok');
    expect(withProbeAccess(null, { access: 'ok' })).toBe(null);
  });
});
