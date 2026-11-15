import { afterEach, describe, expect, it } from 'vitest';
import { getLocale, setLocale, t } from './i18n.svelte.js';
import { localizedApiErrorMessage } from './apiErrors.js';
import { HA_ERROR_CODES, haErrorText } from './utils/haWorkloads.js';

const originalLocale = getLocale();

afterEach(() => setLocale(originalLocale));

describe('localizedApiErrorMessage', () => {
  it('uses stable error codes for the active locale', () => {
    setLocale('en');
    expect(
      localizedApiErrorMessage({ code: 'invalid_password', error: 'contraseña incorrecta' })
    ).toBe('Incorrect password');

    setLocale('es');
    expect(
      localizedApiErrorMessage({ code: 'invalid_password', error: 'incorrect password' })
    ).toBe('Contraseña incorrecta');
  });

  it('keeps the server message for unknown codes', () => {
    expect(localizedApiErrorMessage({ code: 'future_code', error: 'server message' })).toBe(
      'server message'
    );
    expect(localizedApiErrorMessage({ code: 'constructor', error: 'safe fallback' })).toBe(
      'safe fallback'
    );
  });
});

describe('node error codes', () => {
  const codes = [
    'reserved_name',
    'invalid_name',
    'invalid_uri',
    'invalid_ui_url',
    'name_conflict',
    'local_node',
    'incus_revoke_failed',
    'install_in_progress',
  ];
  it('are translated in en, es and ca (never the raw server text)', () => {
    for (const loc of ['en', 'es', 'ca']) {
      setLocale(loc);
      for (const code of codes) {
        const msg = localizedApiErrorMessage({ code, error: 'RAW' });
        expect(msg, `${loc}/${code}`).not.toBe('RAW');
        expect(msg).not.toContain('apiErrors.');
      }
    }
    setLocale('es');
    expect(localizedApiErrorMessage({ code: 'invalid_uri', error: 'x' })).toContain('qemu+ssh://');
    expect(localizedApiErrorMessage({ code: 'invalid_ui_url', error: 'x' })).toContain('http');
  });
});

describe('remote node error codes', () => {
  it('translates typed node/console errors in every locale', () => {
    for (const [loc, needle] of [
      ['en', 'own WebKVM'],
      ['es', 'WebKVM de ese nodo'],
      ['ca', "WebKVM d'aquest node"],
    ]) {
      setLocale(loc);
      expect(localizedApiErrorMessage({ code: 'remote_unsupported', error: 'raw' })).toContain(
        needle
      );
    }
    setLocale('en');
    expect(localizedApiErrorMessage({ code: 'host_key_changed', error: 'raw' })).not.toBe('raw');
    expect(localizedApiErrorMessage({ code: 'console_busy', error: 'raw' })).toContain('consoles');
  });
});

describe('create-on-node media codes', () => {
  const codes = [
    'cloud_init_remote_unsupported',
    'invalid_media_name',
    'media_not_found',
    'media_ambiguous',
    'existing_disk_not_found',
    'existing_disk_pool_invalid',
    'node_storage_unavailable',
    'pool_inactive',
    'pool_not_found',
    'volumes_unavailable',
  ];
  for (const locale of ['en', 'es', 'ca']) {
    it(`translates every code in ${locale}`, () => {
      setLocale(locale);
      for (const code of codes) {
        const out = localizedApiErrorMessage({ code, error: 'raw backend sentence' });
        expect(out, `${locale}:${code}`).not.toBe('raw backend sentence');
        expect(out).not.toContain('remote.errors.');
      }
    });
  }

  it('guides cloud-init on a remote node to an ISO or the node UI', () => {
    setLocale('en');
    const msg = localizedApiErrorMessage({ code: 'cloud_init_remote_unsupported' });
    expect(msg).toContain('ISO');
    expect(msg).toContain("node's own WebKVM");
  });
});

describe('HA disable safety codes', () => {
  for (const locale of ['en', 'es', 'ca']) {
    it(`translates every HA error code in ${locale}`, () => {
      setLocale(locale);
      for (const code of HA_ERROR_CODES) {
        const out = haErrorText(t, { code, message: 'raw backend sentence' });
        expect(out, `${locale}:${code}`).not.toBe('raw backend sentence');
      }
      expect(HA_ERROR_CODES).toContain('stop_timeout');
      expect(HA_ERROR_CODES).toContain('workload_active');
      expect(HA_ERROR_CODES).toContain('workload_paused');
    });
  }

  it('workload_active lists the active resources it refused to orphan', () => {
    setLocale('en');
    const out = haErrorText(t, {
      code: 'workload_active',
      data: { leftovers: ['ha-kvm-web-data'] },
    });
    expect(out).toContain('Stop it first');
    expect(out).toContain('ha-kvm-web-data');
  });

  it('workload_paused says it is paused and what to do', () => {
    setLocale('en');
    const out = haErrorText(t, { code: 'workload_paused' });
    expect(out).toContain('paused');
    expect(out).toContain('Resume it and then stop it');
    expect(out).not.toContain('use Pause');
  });
});

// 8.30: the RAM rule's typed 400s (backend/internal/api/vm_ram.go).
describe('RAM rule error codes', () => {
  it('are translated in en, es and ca', () => {
    for (const loc of ['en', 'es', 'ca']) {
      setLocale(loc);
      for (const code of ['ram_below_minimum', 'ram_below_floor', 'min_ram_above_ram']) {
        const msg = localizedApiErrorMessage({ code, error: 'RAW' });
        expect(msg, `${loc}/${code}`).not.toBe('RAW');
        expect(msg).not.toContain('apiErrors.');
      }
    }
    setLocale('en');
    expect(localizedApiErrorMessage({ code: 'ram_below_minimum', error: 'x' })).toContain('512 MB');
  });
});
