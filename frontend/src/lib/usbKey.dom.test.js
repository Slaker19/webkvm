import { describe, it, expect } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';
import UsbByFingerprint from './fixtures/UsbByFingerprint.svelte';
import UsbByIdentity from './fixtures/UsbByIdentity.svelte';

// Two identical USB devices are an ordinary thing to plug into a VM:
// same vendor, same product, same product string. That is exactly the
// shape VmDetail's key expression collapses.
//
// Svelte 5 aborts the WHOLE render pass on a repeated key — it does not
// skip the item. So this is not a duplicated row, it is a page that
// stops rendering. These tests pin both halves: the production key
// expression does collide, and a positional key does not.

const IDENTICAL_PAIR = [
  { vendor_id: '046d', product_id: 'c52b', name: 'Logitech USB Receiver' },
  { vendor_id: '046d', product_id: 'c52b', name: 'Logitech USB Receiver' },
];

function render(Component, items) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  let error = null;
  let app = null;
  try {
    app = mount(Component, { target, props: { items } });
    flushSync();
  } catch (e) {
    error = e;
  }
  // Read the DOM BEFORE unmounting: unmount tears the content down, so
  // counting afterwards would always report zero and every assertion
  // here would pass for the wrong reason.
  const rows = target.querySelectorAll('li').length;
  const html = target.innerHTML;
  try {
    if (app) unmount(app);
  } catch {
    /* unmount can throw for the same reason mount did */
  }
  target.remove();
  return { error, html, rows };
}

describe('USB device list keying', () => {
  it('the production key expression collides on two identical devices', () => {
    const { error, rows } = render(UsbByFingerprint, IDENTICAL_PAIR);

    // Collide: either Svelte raises, or the list does not end up
    // showing both devices. Neither outcome is acceptable, and this
    // documents which one actually happens.
    expect(error !== null || rows < 2).toBe(true);
  });

  it('positional keying renders both identical devices', () => {
    const { error, rows } = render(UsbByIdentity, IDENTICAL_PAIR);
    expect(error).toBe(null);
    expect(rows).toBe(2);
  });

  it('distinct devices still render under the production key', () => {
    const { error, rows } = render(UsbByFingerprint, [
      { vendor_id: '046d', product_id: 'c52b', name: 'Logitech USB Receiver' },
      { vendor_id: '0781', product_id: '5583', name: 'SanDisk Ultra' },
    ]);
    expect(error).toBe(null);
    expect(rows).toBe(2);
  });
});
