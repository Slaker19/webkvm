import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';

vi.mock('$lib/stores/auth.svelte.js', () => ({
  api: new Proxy(
    {
      listAppliances: () => Promise.resolve({ appliances: [] }),
      listNetworks: () => Promise.resolve([]),
      listPools: () => Promise.resolve([]),
      getCapabilities: () => Promise.resolve(null),
      listIncusProfiles: () => Promise.resolve({ profiles: [] }),
      me: () => Promise.resolve({}),
    },
    {
      get(target, prop) {
        return target[prop] || (() => Promise.resolve([]));
      },
    }
  ),
  auth: { role: 'operator', isAdmin: () => false },
}));

vi.mock('$lib/router.svelte.js', () => ({ navigate: vi.fn() }));

import AppStore from '../routes/AppStore.svelte';

async function settle() {
  for (let i = 0; i < 6; i++) {
    await new Promise((resolve) => setTimeout(resolve, 0));
    flushSync();
  }
}

describe('App Store placement notice', () => {
  let target;
  let app;

  beforeEach(() => {
    localStorage.setItem('webkvm.locale', 'en');
    target = document.createElement('div');
    document.body.appendChild(target);
    app = mount(AppStore, { target });
  });

  afterEach(async () => {
    if (app) await unmount(app);
    target?.remove();
  });

  it('states that app deploys do not select remote KVM nodes or enable HA', async () => {
    await settle();
    expect(target.textContent).toContain('App deployments use this WebKVM host');
    expect(target.textContent).toContain('do not select a remote KVM node');
    expect(target.textContent).toContain('enable Pacemaker HA');
  });
});
