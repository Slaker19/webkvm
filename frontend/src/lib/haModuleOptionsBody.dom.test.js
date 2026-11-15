import { describe, expect, it } from 'vitest';
import { haModuleOptionsBody } from './stores/auth.svelte.js';

describe('haModuleOptionsBody', () => {
  it('keeps the legacy boolean form', () => {
    expect(JSON.parse(haModuleOptionsBody(true))).toEqual({ linstor: true });
    expect(JSON.parse(haModuleOptionsBody(false))).toEqual({ linstor: false });
    expect(JSON.parse(haModuleOptionsBody(undefined))).toEqual({ linstor: false });
  });

  it('only sends opt-ins that are explicitly set', () => {
    expect(JSON.parse(haModuleOptionsBody({ linstor: true }))).toEqual({ linstor: true });
    expect(JSON.parse(haModuleOptionsBody({ accept_experimental: false }))).toEqual({
      linstor: false,
    });
    expect(JSON.parse(haModuleOptionsBody({ accept_experimental: true }))).toEqual({
      linstor: false,
      accept_experimental: true,
    });
  });

  it('drops unknown or duplicated fencing kinds', () => {
    const body = JSON.parse(
      haModuleOptionsBody({ fencing: ['sbd', 'pve', 'pve', 'rm -rf', 'ipmi'] })
    );
    expect(body.fencing).toEqual(['sbd', 'pve', 'ipmi']);
    expect(JSON.parse(haModuleOptionsBody({ fencing: [] }))).toEqual({ linstor: false });
  });
});
