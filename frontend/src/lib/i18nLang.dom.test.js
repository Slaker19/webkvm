import { afterEach, describe, expect, it } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import { setLocale } from '$lib/i18n.svelte.js';
import KeyboardShortcuts from '$lib/components/KeyboardShortcuts.svelte';

const settle = async () => {
  for (let i = 0; i < 4; i++) {
    await new Promise((resolve) => setTimeout(resolve, 0));
    flushSync();
  }
};

describe('locale side effects', () => {
  afterEach(() => setLocale('en'));

  it('keeps <html lang> in sync with the selected locale', () => {
    setLocale('es');
    expect(document.documentElement.lang).toBe('es');
    setLocale('ca');
    expect(document.documentElement.lang).toBe('ca');
  });

  it('renders the shortcuts cheatsheet in the selected locale', async () => {
    setLocale('es');
    const target = document.createElement('div');
    document.body.appendChild(target);
    const app = mount(KeyboardShortcuts, { target });
    await settle();
    window.dispatchEvent(new KeyboardEvent('keydown', { key: '?' }));
    await settle();

    const dialog = document.querySelector('[role=dialog]');
    expect(dialog?.textContent).toContain('Atajos de teclado');
    expect(dialog?.textContent).toContain('Ir a Máquinas Virtuales');

    await unmount(app);
    target.remove();
  });
});
