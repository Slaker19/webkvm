import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

// Mounts the real VmCreate page and reads the storage-pool <select> the
// operator actually sees.
//
// What it protects: every VM-disk selector filtered with usableForDisks,
// which only strips ISO/backup/template pools — a CONTAINER (Incus)
// pool sailed straight through. So "create a KVM VM" offered
// webkvm-incus, and libvirt then failed with "no storage pool with
// matching name". The ISO pool was worse: it IS a libvirt directory
// pool, so a qcow2 was written into the ISO library beside the install
// media (reproduced live before this was fixed).
//
// purpose.test.js covers the predicate. This covers the wiring: that
// the page actually calls it, which is the part a refactor breaks.
const fx = vi.hoisted(() => ({
  // The real pools on the host this was found on.
  pools: [
    { name: 'ISOS', purpose: 'iso', state: 'active' },
    { name: 'Seagate', purpose: 'disk', state: 'inactive' },
    { name: 'webkvm-disks', purpose: 'disk', state: 'active' },
    { name: 'Lexar-Discos', purpose: 'disk', state: 'active' },
    { name: 'Lexar-Contenedores', purpose: 'container', state: 'created' },
    { name: 'Seagate-contenedores', purpose: 'container', state: 'created' },
    { name: 'webkvm-incus', purpose: 'container', state: 'created' },
  ],
  nodes: [],
  inventoryRequests: [],
  incusInventory: {
    node_id: 'remote-incus',
    project: 'containers',
    host_arch: 'x86_64',
    pools: [{ name: 'remote-pool', purpose: 'container', state: 'created' }],
    networks: [{ name: 'incusbr0', bridge: 'incusbr0', active: true }],
    profiles: ['default', 'web'],
    images: [{ ref: 'images:alpine/3.20', label: 'Alpine 3.20' }],
  },
  createVM: vi.fn(() => Promise.resolve({ id: 'remote-container' })),
  baseImages: [{ id: 'ubuntu-24.04', name: 'Ubuntu 24.04', is_cached: true, pool: 'webkvm-disks' }],
  isos: [],
  routeQuery: {},
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      listPools: () => Promise.resolve(fx.pools),
      listNetworks: () => Promise.resolve({ networks: [{ name: 'default', mode: 'nat' }] }),
      listNodes: () => Promise.resolve({ nodes: fx.nodes }),
      getNodeIncusInventory: (id) => {
        fx.inventoryRequests.push(id);
        return Promise.resolve(fx.incusInventory);
      },
      createVM: fx.createVM,
      listBaseCloudImages: () => Promise.resolve(fx.baseImages),
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
      { user: { role: 'admin' }, role: 'admin' },
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

vi.mock('$lib/router.svelte.js', () => ({
  getRoute: () => ({ query: fx.routeQuery }),
  navigate: () => {},
}));

import VmCreate from '../routes/VmCreate.svelte';
import { clusterModule } from '$lib/stores/clusterModule.svelte.js';

const settle = async () => {
  for (let i = 0; i < 8; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};

// The pool selector lives on the wizard's "Resources" step, so the
// test has to walk there the same way an operator does. The tab label
// carries its step number ("3 Resources").
async function openResourcesStep(root) {
  await settle();
  const tab = [...root.querySelectorAll('button')].find((b) => /\bResources\b/.test(b.textContent));
  if (!tab) throw new Error('Resources step tab not found');
  tab.click();
  await settle();
}

async function openIdentityStep(root) {
  await settle();
  const tab = [...root.querySelectorAll('button')].find((button) =>
    /\bIdentity\b/.test(button.textContent)
  );
  if (!tab) throw new Error('Identity step tab not found');
  tab.click();
  await settle();
}

// Every pool name offered anywhere on the page, read from the rendered
// <option> elements rather than from component internals.
function offeredPoolNames(root) {
  const known = new Set(fx.pools.map((p) => p.name));
  const out = new Set();
  for (const opt of root.querySelectorAll('option')) {
    const v = opt.getAttribute('value') || opt.textContent.trim();
    if (known.has(v)) out.add(v);
  }
  return out;
}

describe('VmCreate never offers a pool from another storage world', () => {
  let target, app, originalIncusInventory;

  beforeEach(() => {
    fx.createVM.mockClear();
    fx.routeQuery = {};
    fx.isos = [];
    originalIncusInventory = fx.incusInventory;
    fx.nodes = [
      {
        id: 'remote-incus',
        name: 'incus-west',
        backend: 'incus',
        type: 'remote',
        enabled: true,
      },
      {
        id: 'remote-kvm',
        name: 'kvm-east',
        backend: 'libvirt',
        type: 'remote',
        enabled: true,
      },
    ];
    fx.inventoryRequests = [];
    target = document.createElement('div');
    document.body.appendChild(target);
    app = mount(VmCreate, { target });
  });

  afterEach(async () => {
    if (app) await unmount(app);
    target?.remove();
    fx.incusInventory = originalIncusInventory;
    app = null;
  });

  it('offers the usable disk pools', async () => {
    await openResourcesStep(target);
    const offered = offeredPoolNames(target);
    // Guards the fixture: if nothing rendered, the assertions below
    // would pass vacuously.
    expect(offered.has('webkvm-disks')).toBe(true);
    expect(offered.has('Lexar-Discos')).toBe(true);
  });

  it('never offers an Incus pool for a KVM disk', async () => {
    await openResourcesStep(target);
    const offered = offeredPoolNames(target);
    for (const name of ['webkvm-incus', 'Lexar-Contenedores', 'Seagate-contenedores']) {
      expect(offered.has(name)).toBe(false);
    }
  });

  it('never offers the ISO library for a KVM disk', async () => {
    await openResourcesStep(target);
    expect(offeredPoolNames(target).has('ISOS')).toBe(false);
  });

  it('never offers an inactive pool', async () => {
    await openResourcesStep(target);
    expect(offeredPoolNames(target).has('Seagate')).toBe(false);
  });

  it('offers container pools and excludes VM disk pools when creating an LXC container', async () => {
    await settle();
    const lxcRadio = [...target.querySelectorAll('button[role="radio"]')].find((b) =>
      b.textContent.includes('LXC')
    );
    expect(lxcRadio).toBeTruthy();
    lxcRadio.click();
    await settle();

    await openResourcesStep(target);
    const offered = offeredPoolNames(target);
    expect(offered.has('webkvm-incus')).toBe(true);
    expect(offered.has('Lexar-Contenedores')).toBe(true);
    expect(offered.has('Seagate-contenedores')).toBe(true);

    expect(offered.has('webkvm-disks')).toBe(false);
    expect(offered.has('Lexar-Discos')).toBe(false);
    expect(offered.has('ISOS')).toBe(false);
  });

  it('loads and uses only the selected remote Incus project inventory', async () => {
    await settle();
    const lxcRadio = [...target.querySelectorAll('button[role="radio"]')].find((b) =>
      b.textContent.includes('LXC')
    );
    lxcRadio.click();
    await settle();

    const targetSelect = [...target.querySelectorAll('select')].find((select) =>
      [...select.options].some((option) => option.value === 'remote-incus')
    );
    expect(targetSelect).toBeTruthy();
    expect(targetSelect.disabled).toBe(false);
    targetSelect.value = 'remote-incus';
    targetSelect.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect(fx.inventoryRequests).toEqual(['remote-incus']);
    expect(target.textContent).toContain(
      'Ubuntu 24.04 LTS (Noble) is not listed for this Incus destination'
    );
    const alpine = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Alpine 3.20')
    );
    expect(alpine).toBeTruthy();
    alpine.click();
    await settle();

    await openIdentityStep(target);
    const name = target.querySelector('input[placeholder="my-vm"]');
    name.value = 'remote-test';
    name.dispatchEvent(new Event('input', { bubbles: true }));
    const password = target.querySelector('#lxc-ci-pass');
    password.value = 'testpass';
    password.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();

    await openResourcesStep(target);
    const offered = new Set([...target.querySelectorAll('option')].map((option) => option.value));
    expect(offered.has('remote-pool'), target.textContent).toBe(true);
    expect(offered.has('webkvm-incus')).toBe(false);
    expect(offered.has('incusbr0')).toBe(true);
    const imageStep = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Type and image')
    );
    imageStep.click();
    await settle();
    expect(target.textContent).toContain('Alpine 3.20');

    const resourcesStep = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Resources')
    );
    resourcesStep.click();
    await settle();
    const next = [...target.querySelectorAll('button')].find(
      (button) => button.textContent.trim() === 'Next'
    );
    next.click();
    await settle();
    // Containers now have a Network step too; advance through it to Review.
    const networkNext = [...target.querySelectorAll('button')].find(
      (button) => button.textContent.trim() === 'Next'
    );
    expect(networkNext, target.textContent).toBeTruthy();
    networkNext.click();
    await settle();
    const create = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Create Container')
    );
    expect(create, target.textContent).toBeTruthy();
    create.click();
    await settle();
    expect(fx.createVM).toHaveBeenCalledOnce();
    expect(fx.createVM.mock.calls[0][0]).toMatchObject({
      name: 'remote-test',
      type: 'container',
      node: 'remote-incus',
      storage_pool: 'remote-pool',
      network: 'incusbr0',
      image: 'images:alpine/3.20',
    });
  });

  it('does not carry hidden Incus cloud-init settings into a KVM VM', async () => {
    await settle();
    const containerType = [...target.querySelectorAll('button[role="radio"]')].find((button) =>
      button.textContent.includes('LXC')
    );
    containerType.click();
    await settle();
    await openIdentityStep(target);

    const enableCloudInit = [...target.querySelectorAll('label')].find((label) =>
      label.textContent.includes('Enable cloud-init provisioning')
    );
    expect(enableCloudInit).toBeTruthy();
    const checkbox = enableCloudInit.querySelector('input[type="checkbox"]');
    checkbox.checked = true;
    checkbox.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();

    const password = target.querySelector('#lxc-ci-pass');
    password.value = 'testpass';
    password.dispatchEvent(new Event('input', { bubbles: true }));
    const name = target.querySelector('input[placeholder="my-vm"]');
    name.value = 'switch-to-kvm';
    name.dispatchEvent(new Event('input', { bubbles: true }));

    const imageStep = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Type and image')
    );
    imageStep.click();
    await settle();
    const kvmType = [...target.querySelectorAll('button[role="radio"]')].find((button) =>
      button.textContent.includes('KVM')
    );
    kvmType.click();
    await settle();
    await openResourcesStep(target);
    const next = [...target.querySelectorAll('button')].find(
      (button) => button.textContent.trim() === 'Next'
    );
    next.click();
    await settle();
    const reviewStep = [...target.querySelectorAll('button')].find((button) =>
      /\bReview\b/.test(button.textContent)
    );
    reviewStep.click();
    await settle();
    const create = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Create VM')
    );
    expect(create, target.textContent).toBeTruthy();
    create.click();
    await settle();

    expect(fx.createVM).toHaveBeenCalledOnce();
    expect(fx.createVM.mock.calls[0][0]).not.toHaveProperty('cloud_init');
  });

  it('explains why remote Incus creation is blocked on the review step', async () => {
    const previousInventory = fx.incusInventory;
    fx.incusInventory = {
      ...previousInventory,
      pools: [],
      networks: [],
      profiles: [],
      images: [],
    };

    await settle();
    const containerType = [...target.querySelectorAll('button[role="radio"]')].find((button) =>
      button.textContent.includes('LXC')
    );
    containerType.click();
    await settle();
    await openIdentityStep(target);
    const name = target.querySelector('input[placeholder="my-vm"]');
    name.value = 'remote-without-resources';
    name.dispatchEvent(new Event('input', { bubbles: true }));
    const cloudInitToggle = [...target.querySelectorAll('label')]
      .find((label) => label.textContent.includes('Enable cloud-init provisioning'))
      ?.querySelector('input[type="checkbox"]');
    if (cloudInitToggle?.checked) {
      cloudInitToggle.checked = false;
      cloudInitToggle.dispatchEvent(new Event('change', { bubbles: true }));
    }
    const imageStep = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Type and image')
    );
    imageStep.click();
    await settle();
    const targetSelect = [...target.querySelectorAll('select')].find((select) =>
      [...select.options].some((option) => option.value === 'remote-incus')
    );
    targetSelect.value = 'remote-incus';
    targetSelect.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    fx.incusInventory = previousInventory;

    await openResourcesStep(target);
    const reviewStep = [...target.querySelectorAll('button')].find((button) =>
      /\bReview\b/.test(button.textContent)
    );
    reviewStep.click();
    await settle();

    expect(target.textContent).toContain('This target has no usable container pool');
    const create = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Create Container')
    );
    expect(create).toBeTruthy();
    expect(create.disabled).toBe(true);
  });

  it('does not silently replace an image selected before changing Incus targets', async () => {
    await settle();
    const containerType = [...target.querySelectorAll('button[role="radio"]')].find((button) =>
      button.textContent.includes('LXC')
    );
    containerType.click();
    await settle();
    const selectedImage = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Ubuntu 24.04 LTS (Noble)')
    );
    expect(selectedImage).toBeTruthy();
    selectedImage.click();
    await settle();

    const targetSelect = [...target.querySelectorAll('select')].find((select) =>
      [...select.options].some((option) => option.value === 'remote-incus')
    );
    targetSelect.value = 'remote-incus';
    targetSelect.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();

    expect(target.textContent).toContain(
      'Ubuntu 24.04 LTS (Noble) is not listed for this Incus destination'
    );
  });

  // The legacy "these selectors still show this host's pools" warning
  // contradicted the remote notice (resources are now listed from that
  // node): only the "listed from that node" notice remains.
  it('explains that a remote KVM target lists its own resources', async () => {
    await settle();
    const targetSelect = [...target.querySelectorAll('select')].find((select) =>
      [...select.options].some((option) => option.value === 'remote-kvm')
    );
    expect(targetSelect).toBeTruthy();
    targetSelect.value = 'remote-kvm';
    targetSelect.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();

    expect(target.textContent).not.toContain('These selectors still show this host’s pools');
    expect(target.textContent).toContain('listed from the storage pools of that node');
  });

  it('states that Cloud-Init template deployments run locally', async () => {
    await settle();
    const templateType = [...target.querySelectorAll('button[role="radio"]')].find((button) =>
      button.textContent.includes('Cloud-Init')
    );
    templateType.click();
    await settle();

    expect(target.textContent).toContain('Cloud-Init template deployments run on this host only');
  });

  it('lets the operator drop a base image and keeps HA available for it', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    fx.routeQuery = { base_image: 'ubuntu-24.04', name: 'cow-ha' };
    await unmount(app);
    target.innerHTML = '';
    app = mount(VmCreate, { target });
    await settle();
    clusterModule.enabled = true;
    await settle();

    const banner = target.querySelector('[data-testid="base-image-selection"]');
    expect(banner?.textContent).toContain('Remove base image');
    expect(
      target.querySelector('[role=switch][aria-label="Enable high availability after creating"]'),
      target.textContent
    ).toBeTruthy();

    const remove = [...banner.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Remove base image')
    );
    remove.click();
    await settle();

    expect(target.querySelector('[data-testid="base-image-selection"]')).toBeNull();
    const kvm = [...target.querySelectorAll('[role=radio]')].find((radio) =>
      radio.textContent.includes('Virtual machine (KVM)')
    );
    expect(kvm?.getAttribute('aria-checked')).toBe('true');

    const haSwitch = target.querySelector(
      '[role=switch][aria-label="Enable high availability after creating"]'
    );
    haSwitch.click();
    await settle();

    await openIdentityStep(target);
    const review = [...target.querySelectorAll('button')].find((button) =>
      /\bReview\b/.test(button.textContent)
    );
    review.click();
    await settle();
    const create = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Create VM')
    );
    create.click();
    await settle();

    expect(fx.createVM).toHaveBeenCalledOnce();
    const payload = fx.createVM.mock.calls[0][0];
    expect(payload.ha).toEqual({});
    expect(payload.base_image_id).toBeUndefined();
    clusterModule.enabled = false;
  });

  it('offers create-with-HA for a local target and sends ha in the payload', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    await settle();
    // The store refresh from mount answers with the mocked (empty) module
    // data, so enable it the way the real module response would.
    clusterModule.enabled = true;
    await settle();

    const haSwitch = target.querySelector(
      '[role=switch][aria-label="Enable high availability after creating"]'
    );
    expect(haSwitch, target.textContent).toBeTruthy();
    haSwitch.click();
    await settle();

    await openIdentityStep(target);
    const nameInput = target.querySelector('input[placeholder="my-vm"]');
    nameInput.value = 'ha-vm';
    nameInput.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();

    const review = [...target.querySelectorAll('button')].find((button) =>
      /\bReview\b/.test(button.textContent)
    );
    review.click();
    await settle();
    const create = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Create VM')
    );
    create.click();
    await settle();

    expect(fx.createVM).toHaveBeenCalled();
    expect(fx.createVM.mock.calls[0][0]).toMatchObject({ ha: {} });
    clusterModule.enabled = false;
  });

  it('prefills a cached base image and sends its ID for a local COW VM', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    fx.routeQuery = {
      base_image: 'ubuntu-24.04',
      name: 'quick-ubuntu',
      network: 'default',
    };
    await unmount(app);
    target.innerHTML = '';
    app = mount(VmCreate, { target });
    await settle();

    expect(target.querySelector('[data-testid="base-image-selection"]').textContent).toContain(
      'copy-on-write disk based on Ubuntu 24.04'
    );
    await openIdentityStep(target);
    expect(target.querySelector('input[placeholder="my-vm"]').value).toBe('quick-ubuntu');
    const password = target.querySelector('#ci-pass');
    password.value = 'testpass1';
    password.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();

    const review = [...target.querySelectorAll('button')].find((button) =>
      /\bReview\b/.test(button.textContent)
    );
    expect(review).toBeTruthy();
    review.click();
    await settle();
    const create = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Create VM')
    );
    expect(create, target.textContent).toBeTruthy();
    create.click();
    await settle();

    expect(fx.createVM).toHaveBeenCalledOnce();
    expect(fx.createVM.mock.calls[0][0]).toMatchObject({
      name: 'quick-ubuntu',
      base_image_id: 'ubuntu-24.04',
      network: 'default',
      storage_pool: 'webkvm-disks',
      cloud_init: { user: 'ubuntu', password: 'testpass1' },
    });
  });

  it('keeps cloud-init when a base image is switched to KVM and hides ISOs', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    fx.isos = [{ name: 'ubuntu.iso', path: '/pools/ISOS/ubuntu.iso', pool: 'ISOS' }];
    fx.routeQuery = { base_image: 'ubuntu-24.04', name: 'cow-kvm' };
    await unmount(app);
    target.innerHTML = '';
    app = mount(VmCreate, { target });
    await settle();

    await p_selectKvm();
    async function p_selectKvm() {
      const kvm = [...target.querySelectorAll('[role=radio]')].find((radio) =>
        radio.textContent.includes('Virtual machine (KVM)')
      );
      kvm.click();
      await settle();
    }
    expect(target.querySelector('[data-testid="iso-select"]')).toBeNull();

    await openIdentityStep(target);
    const pass = target.querySelector('#ci-pass');
    expect(pass, target.textContent).toBeTruthy();
    pass.value = 'TestPass123';
    pass.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();

    const review = [...target.querySelectorAll('button')].find((button) =>
      /\bReview\b/.test(button.textContent)
    );
    review.click();
    await settle();
    const create = [...target.querySelectorAll('button')].find((button) =>
      button.textContent.includes('Create VM')
    );
    create.click();
    await settle();

    expect(fx.createVM).toHaveBeenCalledOnce();
    const payload = fx.createVM.mock.calls[0][0];
    expect(payload.base_image_id).toBe('ubuntu-24.04');
    expect(payload.cloud_init).toMatchObject({ user: 'ubuntu', password: 'TestPass123' });
  });

  it('preselects the ISO handed over from Image Hub', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    fx.isos = [{ name: 'ubuntu.iso', path: '/pools/ISOS/ubuntu.iso', pool: 'ISOS' }];
    fx.routeQuery = { iso: '/pools/ISOS/ubuntu.iso' };
    await unmount(app);
    target.innerHTML = '';
    app = mount(VmCreate, { target });
    await settle();

    expect(target.querySelector('[data-testid="iso-select"]').value).toBe('/pools/ISOS/ubuntu.iso');
  });

  it('drops the base image when the operator switches to a container', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    fx.routeQuery = { base_image: 'ubuntu-24.04', name: 'cow-lxc' };
    await unmount(app);
    target.innerHTML = '';
    app = mount(VmCreate, { target });
    await settle();

    const lxc = [...target.querySelectorAll('[role=radio]')].find((radio) =>
      radio.textContent.includes('Container (Incus')
    );
    lxc.click();
    await settle();

    expect(target.querySelector('[data-testid="base-image-selection"]')).toBeNull();
    expect(lxc.getAttribute('aria-checked')).toBe('true');
    expect(target.textContent).not.toContain('Cached base images are stored on this server');
  });

  it('explains why the Review step cannot create yet', async () => {
    localStorage.setItem('webkvm.locale', 'en');
    await settle();
    const review = [...target.querySelectorAll('button')].find((button) =>
      /\bReview\b/.test(button.textContent)
    );
    review.click();
    await settle();

    expect(target.textContent).toContain('Complete these before creating:');
  });
});
