import { describe, it, expect, vi, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

// A9: `selectedPackages` was a plain `Set` inside `$state`. Svelte 5 does
// not proxy Set, so `.add/.delete/.clear` never notified `selectedCount`
// and the "Upgrade Selected" button never appeared. This test toggles a
// checkbox and expects the count to follow.
const fx = vi.hoisted(() => ({
  summary: {
    total: 2,
    security_count: 0,
    reboot_required: false,
    upgrade_running: false,
    packages: [
      { package: 'curl', arch: 'amd64', current_version: '1', new_version: '2', suite: 'noble' },
      { package: 'vim', arch: 'amd64', current_version: '1', new_version: '2', suite: 'noble' },
    ],
  },
  upgradeCalls: [],
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = {
    getHostUpdates: () => Promise.resolve(fx.summary),
    getHostUpdateSchedule: () => Promise.resolve(null),
    upgradeHostUpdates: (packages) => {
      fx.upgradeCalls.push(packages);
      return new Promise(() => {});
    },
  };
  return { api, auth: { user: { role: 'admin' } } };
});

import HostUpdatesTab from './components/HostUpdatesTab.svelte';

const settle = async () => {
  for (let i = 0; i < 6; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

const buttonByText = (root, text) =>
  [...root.querySelectorAll('button')].find((b) => (b.textContent || '').includes(text));

let mounted = null;
afterEach(() => {
  if (mounted) {
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
  }
  document.body.innerHTML = '';
});

async function mountTab() {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(HostUpdatesTab, { target });
  await settle();
  mounted = { target, app };
  return target;
}

describe('HostUpdatesTab package selection', () => {
  it('shows the selected count after toggling a checkbox', async () => {
    const target = await mountTab();
    expect(buttonByText(target, 'Upgrade Selected')).toBeUndefined();

    const box = target.querySelector('input[aria-label="curl"]');
    expect(box).toBeTruthy();
    box.click();
    await settle();

    expect(buttonByText(target, 'Upgrade Selected (1)')).toBeTruthy();

    target.querySelector('input[aria-label="vim"]').click();
    await settle();
    expect(buttonByText(target, 'Upgrade Selected (2)')).toBeTruthy();

    box.click();
    await settle();
    expect(buttonByText(target, 'Upgrade Selected (1)')).toBeTruthy();
  });

  it('asks for confirmation before upgrading the selection', async () => {
    const target = await mountTab();
    target.querySelector('input[aria-label="vim"]').click();
    await settle();

    buttonByText(target, 'Upgrade Selected (1)').click();
    await settle();
    expect(fx.upgradeCalls).toEqual([]);

    const confirm = buttonByText(document.body, 'Proceed with Upgrade');
    expect(confirm).toBeTruthy();
    confirm.click();
    await settle();
    expect(fx.upgradeCalls).toEqual([['vim']]);
  });
});
