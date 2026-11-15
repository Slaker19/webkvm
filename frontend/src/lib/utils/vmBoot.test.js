import { describe, it, expect } from 'vitest';
import { effectiveBootOrder } from './vmBoot.js';

describe('effectiveBootOrder', () => {
  it('drops an empty CD-ROM from the boot order', () => {
    expect(effectiveBootOrder('cdrom', '')).toBe('disk');
    expect(effectiveBootOrder('cdrom', '   ')).toBe('disk');
    expect(effectiveBootOrder('cdrom', undefined)).toBe('disk');
  });

  it('keeps the requested order when there is a real ISO', () => {
    expect(effectiveBootOrder('cdrom', '/var/lib/libvirt/images/install.iso')).toBe('cdrom');
  });

  it('leaves explicit disk and network boot untouched', () => {
    expect(effectiveBootOrder('disk', '')).toBe('disk');
    expect(effectiveBootOrder('network', '')).toBe('network');
  });

  it('falls back to disk for an empty value', () => {
    expect(effectiveBootOrder('', '')).toBe('disk');
  });
});
