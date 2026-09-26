import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

// This file needs a DOM, so it runs under vitest.repro.config.js rather
// than the default node config — see that file for why the harness
// patch is required.
//
// What it protects: the move dialog used <Switch> and <Label> without
// importing either. Svelte 5 compiles an unresolved capitalised tag
// into a call to a bare identifier, so the page built, type-checked
// and shipped — and then threw ReferenceError the moment the dialog
// rendered. The button was inert and nothing on the page said why.
// scripts/check-undefined-components.mjs catches the static form of
// this; this test catches the behavioural consequence, so a future
// rename or refactor cannot quietly reintroduce it.
const fx = vi.hoisted(() => ({
  pools: [],
  isos: [{ name: 'ubuntu-26.04.1-desktop-amd64.iso', size: 123456, pool: 'ISOS' }],
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      listPools: () => Promise.resolve(fx.pools),
      listISOs: () => Promise.resolve(fx.isos),
    },
    {
      get(target, prop) {
        if (prop in target) return target[prop];
        if (typeof prop !== 'string') return undefined;
        return () => Promise.resolve([]);
      },
    }
  );
  return {
    api,
    auth: new Proxy(
      { user: { role: 'admin' } },
      {
        get(target, prop) {
          if (prop in target) return target[prop];
          if (typeof prop !== 'string') return undefined;
          return () => true;
        },
      }
    ),
  };
});

vi.mock('$lib/router.svelte.js', () => ({ navigate: () => {} }));

import Storage from '../routes/Storage.svelte';

const settle = async () => {
  for (let i = 0; i < 6; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

const buttonByLabel = (root, label) =>
  [...root.querySelectorAll('button')].find((b) =>
    (b.getAttribute('aria-label') || '').startsWith(label)
  );

async function openIsosTab() {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(Storage, { target });
  await settle();
  const tab = [...target.querySelectorAll('button')].find((b) =>
    (b.textContent || '').includes('ISO Images')
  );
  tab.click();
  await settle();
  return { target, app };
}

const MOVE = 'Move to another pool';

describe('Storage: the move control only exists when there is a destination', () => {
  let target, app;

  beforeEach(() => {
    fx.isos = [{ name: 'ubuntu-26.04.1-desktop-amd64.iso', size: 123456, pool: 'ISOS' }];
  });

  afterEach(async () => {
    if (app) await unmount(app);
    target?.remove();
    app = null;
  });

  it('hides the move button for an ISO when it is the only ISO pool', async () => {
    fx.pools = [
      { name: 'ISOS', purpose: 'iso', state: 'active' },
      { name: 'webkvm-disks', purpose: 'disk', state: 'active' },
    ];
    ({ target, app } = await openIsosTab());

    // Rename and delete are still offered, so the card is rendered and
    // the move button is absent for a real reason rather than a broken
    // fixture.
    expect(buttonByLabel(target, 'Rename ubuntu')).toBeTruthy();
    expect(buttonByLabel(target, 'Delete ubuntu')).toBeTruthy();
    expect(buttonByLabel(target, MOVE)).toBeFalsy();
  });

  it('opens the move dialog when a second ISO pool exists', async () => {
    fx.pools = [
      { name: 'ISOS', purpose: 'iso', state: 'active' },
      { name: 'Lexar-ISOS', purpose: 'iso', state: 'active' },
    ];
    ({ target, app } = await openIsosTab());

    const move = buttonByLabel(target, MOVE);
    expect(move).toBeTruthy();

    move.click();
    await settle();

    const dialogs = [...document.body.querySelectorAll('[data-slot="dialog-content"]')];
    const moveDialog = dialogs.find((d) => (d.textContent || '').includes(MOVE));
    expect(moveDialog).toBeTruthy();
    // The destination the user would move into has to be offered.
    expect(moveDialog.textContent).toContain('Lexar-ISOS');
  });

  it('does not offer a move into a pool that is not active', async () => {
    fx.pools = [
      { name: 'ISOS', purpose: 'iso', state: 'active' },
      { name: 'Seagate-ISOS', purpose: 'iso', state: 'inactive' },
    ];
    ({ target, app } = await openIsosTab());

    // An inactive pool has no mounted path: writing there would fill
    // the root filesystem under an empty mount point.
    expect(buttonByLabel(target, MOVE)).toBeFalsy();
  });

  it('never offers a disk pool as the destination for an ISO', async () => {
    fx.pools = [
      { name: 'ISOS', purpose: 'iso', state: 'active' },
      { name: 'Lexar-ISOS', purpose: 'iso', state: 'active' },
      { name: 'webkvm-disks', purpose: 'disk', state: 'active' },
    ];
    ({ target, app } = await openIsosTab());

    buttonByLabel(target, MOVE).click();
    await settle();

    const dialogs = [...document.body.querySelectorAll('[data-slot="dialog-content"]')];
    const moveDialog = dialogs.find((d) => (d.textContent || '').includes(MOVE));
    expect(moveDialog.textContent).toContain('Lexar-ISOS');
    expect(moveDialog.textContent).not.toContain('webkvm-disks');
  });
});
