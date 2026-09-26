import { describe, it, expect, vi, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({ role: 'admin', navigated: [] }));

vi.mock('$lib/stores/auth.svelte.js', () => ({
  api: {
    listVMs: () => Promise.resolve([]),
    listPools: () => Promise.resolve([]),
    listNetworks: () => Promise.resolve([]),
  },
  auth: {
    get role() {
      return fx.role;
    },
    isAdmin: () => fx.role === 'admin',
  },
}));
vi.mock('$lib/router.svelte.js', () => ({
  navigate: (p) => fx.navigated.push(p),
}));

import CommandPalette from './components/CommandPalette.svelte';

const settle = async (ms = 0) => {
  for (let i = 0; i < 4; i++) {
    await new Promise((r) => setTimeout(r, ms));
    flushSync();
  }
};

let cleanup = null;
afterEach(() => {
  cleanup?.();
  cleanup = null;
  fx.navigated = [];
});

async function openPalette(role) {
  fx.role = role;
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(CommandPalette, { target });
  cleanup = () => {
    unmount(app);
    target.remove();
  };
  await settle();
  window.dispatchEvent(new Event('open-command-palette'));
  await settle();
  return target;
}

const labels = (root) =>
  [...root.querySelectorAll('[role="dialog"] button')].map((b) => b.textContent || '');

const press = (el, key) => {
  el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
  flushSync();
};

describe('CommandPalette', () => {
  // The dialog stopped keydown propagation, so the window-level handler
  // never saw keys typed in the search box: Esc / arrows / Enter were dead.
  it('Esc typed in the search box closes the palette', async () => {
    const root = await openPalette('admin');
    const input = root.querySelector('[role="dialog"] input');
    expect(input).toBeTruthy();
    press(input, 'Escape');
    await settle(50);
    expect(root.querySelector('[role="dialog"]')).toBe(null);
  });

  it('arrows + Enter in the search box run the selected command', async () => {
    const root = await openPalette('admin');
    const input = root.querySelector('[role="dialog"] input');
    press(input, 'ArrowDown');
    press(input, 'Enter');
    await settle();
    // Second entry of the default list for an admin is "Create VM".
    expect(fx.navigated).toEqual(['/vms/new']);
  });

  it('hides admin-only destinations from non-admins', async () => {
    const root = await openPalette('viewer');
    const all = labels(root).join('\n');
    for (const hidden of ['Users', 'Firewall', 'Settings', 'Audit', 'Backup', 'Nodes']) {
      expect(all).not.toContain(hidden);
    }
    expect(all).toContain('Storage');
  });

  it('shows them to admins', async () => {
    const root = await openPalette('admin');
    const all = labels(root).join('\n');
    expect(all).toContain('Users');
    expect(all).toContain('Settings');
  });
});
