import { describe, it, expect, vi, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';
import SwitchControlled from './fixtures/SwitchControlled.svelte';
import SwitchBound from './fixtures/SwitchBound.svelte';
import SearchBound from './fixtures/SearchBound.svelte';
import DataTable from './components/DataTable.svelte';

const settle = async () => {
  for (let i = 0; i < 4; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

let cleanup = [];
function render(Component, props = {}) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(Component, { target, props });
  flushSync();
  cleanup.push(() => {
    unmount(app);
    target.remove();
  });
  return target;
}

afterEach(() => {
  cleanup.forEach((fn) => fn());
  cleanup = [];
});

const sw = (root) => root.querySelector('[role="switch"]');
const parent = (root) => root.querySelector('[data-testid="parent"]').textContent;

describe('Switch', () => {
  // The bug: the Switch flipped its own `checked`, which shadows the
  // (unbound) prop. A failed save restored the parent to the value it
  // already had, the prop never changed, and the toggle stayed flipped.
  it('snaps back when a controlled save fails', async () => {
    let reject;
    const save = vi.fn(
      () =>
        new Promise((_, rej) => {
          reject = rej;
        })
    );
    const root = render(SwitchControlled, { save });

    sw(root).click();
    flushSync();
    expect(save).toHaveBeenCalledWith(true);
    // Optimistic while the request is in flight.
    expect(sw(root).getAttribute('aria-checked')).toBe('true');

    reject(new Error('boom'));
    await settle();
    expect(parent(root)).toBe('false');
    expect(sw(root).getAttribute('aria-checked')).toBe('false');
  });

  it('stays on when a controlled save succeeds', async () => {
    const save = vi.fn(() => Promise.resolve());
    const root = render(SwitchControlled, { save });

    sw(root).click();
    await settle();
    expect(parent(root)).toBe('true');
    expect(sw(root).getAttribute('aria-checked')).toBe('true');

    // And can be toggled back (the prop, not a stale local, drives it).
    sw(root).click();
    await settle();
    expect(save).toHaveBeenLastCalledWith(false);
    expect(sw(root).getAttribute('aria-checked')).toBe('false');
  });

  it('bind:checked still writes through to the parent', async () => {
    const root = render(SwitchBound);
    sw(root).click();
    await settle();
    expect(parent(root)).toBe('true');
    expect(sw(root).getAttribute('aria-checked')).toBe('true');
    sw(root).click();
    await settle();
    expect(parent(root)).toBe('false');
  });
});

describe('SearchInput', () => {
  const type = (input, text) => {
    input.value = text;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    flushSync();
  };

  // The bug: the input was bound to a local `$derived(value)` copy, so
  // the parent's bind:value never changed and no list ever filtered.
  it('writes typed text back through bind:value', () => {
    const root = render(SearchBound);
    type(root.querySelector('input'), 'web');
    expect(parent(root)).toBe('web');
  });

  it('clear button empties the bound value', () => {
    const onInput = vi.fn();
    const root = render(SearchBound, { onInput });
    type(root.querySelector('input'), 'web');
    root.querySelector('button').click();
    flushSync();
    expect(parent(root)).toBe('');
    expect(onInput).toHaveBeenLastCalledWith('');
  });

  it('debounces only the onInput callback', async () => {
    vi.useFakeTimers();
    try {
      const onInput = vi.fn();
      const root = render(SearchBound, { onInput, debounce: 250 });
      type(root.querySelector('input'), 'ab');
      expect(parent(root)).toBe('ab');
      expect(onInput).not.toHaveBeenCalled();
      vi.advanceTimersByTime(250);
      expect(onInput).toHaveBeenCalledWith('ab');
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('DataTable', () => {
  // minmax(0, 1fr) let the flexible column collapse to nothing on a
  // phone; it must keep a real floor and the rows a matching min-width.
  it('gives flexible columns a minimum width', () => {
    const root = render(DataTable, {
      columns: [
        { key: 'name', label: 'Name' },
        { key: 'role', label: 'Role', width: '110px' },
      ],
      rows: [{ id: 1, name: 'alice', role: 'admin' }],
    });
    const grid = root.querySelector('.grid');
    expect(grid.getAttribute('style')).toContain('minmax(8rem, 1fr) 110px');
    expect(grid.parentElement.getAttribute('style')).toContain('calc(8rem + 110px)');
  });
});
