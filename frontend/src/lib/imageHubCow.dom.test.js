import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';

const fx = vi.hoisted(() => ({
  navigate: vi.fn(),
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const values = {
    listContainerImages: () => Promise.resolve([]),
    listBaseCloudImages: () =>
      Promise.resolve([
        {
          id: 'ubuntu-24.04',
          name: 'Ubuntu 24.04',
          is_cached: true,
          pool: 'webkvm-disks',
        },
      ]),
    listISOs: () => Promise.resolve([]),
    listNetworks: () => Promise.resolve([{ name: 'br0', active: true }]),
    listPools: () => Promise.resolve([{ name: 'webkvm-disks', purpose: 'disk', state: 'active' }]),
    getIncusImagesVolume: () => Promise.resolve(null),
  };
  return {
    api: new Proxy(values, {
      get(target, prop) {
        return target[prop] || (() => Promise.resolve([]));
      },
    }),
    auth: { role: 'admin', isAdmin: () => true },
  };
});

vi.mock('$lib/router.svelte.js', () => ({ navigate: fx.navigate }));

import ImageHub from '../routes/ImageHub.svelte';

async function settle() {
  for (let i = 0; i < 8; i++) {
    await new Promise((resolve) => setTimeout(resolve, 0));
    flushSync();
  }
}

describe('Image Hub cached base image launch', () => {
  let target;
  let app;

  beforeEach(() => {
    localStorage.setItem('webkvm.locale', 'en');
    fx.navigate.mockClear();
    target = document.createElement('div');
    document.body.appendChild(target);
    app = mount(ImageHub, { target });
  });

  afterEach(async () => {
    if (app) await unmount(app);
    target?.remove();
  });

  it('passes the chosen base image, name, and network to VM creation', async () => {
    await settle();
    const baseTab = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Base Cloud Disks')
    );
    expect(baseTab).toBeTruthy();
    baseTab.click();
    await settle();
    const createButton = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Create VM (CoW)')
    );
    expect(createButton).toBeTruthy();
    createButton.click();
    await settle();

    const configure = [...document.body.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Configure VM from this base image')
    );
    expect(configure, document.body.textContent).toBeTruthy();
    configure.click();

    expect(fx.navigate).toHaveBeenCalledOnce();
    const [path, options] = fx.navigate.mock.calls[0];
    expect(path).toBe('/vms/new');
    expect(options.query).toMatchObject({
      base_image: 'ubuntu-24.04',
      network: 'br0',
    });
    expect(options.query.name).toMatch(/^ubuntu-24\.04-\d{3}$/);
  });
});
