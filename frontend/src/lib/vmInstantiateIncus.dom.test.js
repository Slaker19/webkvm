import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';

import VmInstantiateModal from './components/vm-list/VmInstantiateModal.svelte';

const settle = async () => {
  for (let i = 0; i < 5; i++) {
    await new Promise((resolve) => setTimeout(resolve, 0));
    flushSync();
  }
};

describe('Incus template instantiation options', () => {
  let target;
  let app;
  let onInstantiate;

  beforeEach(() => {
    localStorage.setItem('webkvm.locale', 'en');
    target = document.createElement('div');
    document.body.appendChild(target);
    onInstantiate = vi.fn();
    app = mount(VmInstantiateModal, {
      target,
      props: {
        open: true,
        templates: [
          { id: 'kvm-template', name: 'KVM template', hypervisor: 'kvm' },
          { id: 'incus-template', name: 'Incus template', hypervisor: 'incus' },
        ],
        netOptions: [{ name: 'br0', bridge: 'br0' }],
        poolOptions: [
          { name: 'kvm-pool', purpose: 'disk', state: 'active' },
          { name: 'incus-pool', purpose: 'container', state: 'created' },
          { name: 'incus-fast', purpose: 'container', state: 'created' },
        ],
        onInstantiate,
      },
    });
  });

  afterEach(async () => {
    if (app) await unmount(app);
    target?.remove();
  });

  it('offers Incus pools and omits KVM network, linked clone, and cloud-init controls', async () => {
    await settle();
    const template = document.body.querySelector('#inst-template-comp');
    template.value = 'incus-template';
    const otherOptions = [...template.options].filter(
      (option) => option.value !== 'incus-template'
    );
    otherOptions.forEach((option) => (option.disabled = true));
    template.dispatchEvent(new Event('change', { bubbles: true }));
    otherOptions.forEach((option) => (option.disabled = false));
    await settle();

    expect(document.body.querySelector('#inst-net-comp')).toBeNull();
    expect(document.body.textContent).toContain('Copies the Incus instance and its profiles');
    expect(document.body.textContent).not.toContain('Clone type');
    expect(document.body.textContent).not.toContain('Enable cloud-init provisioning');

    const pool = document.body.querySelector('#inst-pool-comp');
    expect([...pool.options].map((option) => option.value)).toEqual([
      '',
      'incus-pool',
      'incus-fast',
    ]);

    const poolNow = document.body.querySelector('#inst-pool-comp');
    poolNow.value = 'incus-pool';
    poolNow.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    const name = document.body.querySelector('#inst-name-comp');
    name.value = 'incus-copy';
    name.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();

    const instantiate = [...document.body.querySelectorAll('button')].find(
      (button) => button.textContent.trim() === 'Instantiate'
    );
    expect(instantiate, document.body.textContent).toBeTruthy();
    instantiate.click();
    await settle();

    expect(onInstantiate).toHaveBeenCalledOnce();
    expect(onInstantiate.mock.calls[0][0]).toMatchObject({
      templateId: 'incus-template',
      name: 'incus-copy',
      linked: false,
      ci: false,
    });
  });
});
