import { describe, it, expect } from 'vitest';
import { remoteMediaFields } from './remoteMedia.js';

const isos = [
  { name: 'debian.iso', pool: 'east-isos', path: '/srv/isos/debian.iso' },
  { name: 'dup.iso', pool: 'east-extra', path: '/srv/extra/dup.iso' },
  { name: 'virtio-win.iso', pool: 'east-isos', path: '/srv/isos/virtio-win.iso' },
];

describe('remoteMediaFields', () => {
  it('sends volume names and pools, never paths', () => {
    expect(remoteMediaFields(isos, '/srv/extra/dup.iso', '/srv/isos/virtio-win.iso')).toEqual({
      iso: 'dup.iso',
      iso_pool: 'east-extra',
      virtio_iso: 'virtio-win.iso',
      virtio_iso_pool: 'east-isos',
    });
  });

  it('drops values that are not in the node listing', () => {
    const out = remoteMediaFields(isos, '/etc/passwd', '');
    expect(out.iso).toBeUndefined();
    expect(out.virtio_iso).toBeUndefined();
    expect(JSON.stringify(out)).not.toContain('/');
  });

  it('tolerates a missing listing', () => {
    expect(remoteMediaFields(null, '/srv/isos/debian.iso', undefined)).toEqual({
      iso: undefined,
      virtio_iso: undefined,
    });
  });
});
