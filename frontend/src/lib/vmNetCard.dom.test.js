import { describe, it, expect, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';
import { setLocale } from './i18n.svelte.js';
import VmNetCard from './components/vm-detail/VmNetCard.svelte';

// 8.26 defect 5: link state badge and cable toggle were hardcoded Spanish.
let app = null;
let target = null;
const render = (vm) => {
  target = document.createElement('div');
  document.body.appendChild(target);
  app = mount(VmNetCard, { target, props: { vm, toggleLinkState: () => {}, removeNet: () => {} } });
  flushSync();
  return target;
};
afterEach(() => {
  if (app) unmount(app);
  target?.remove();
  app = null;
  setLocale('en');
});

const nic = (link_state) => ({ mac: '52:54:00:00:00:01', network: 'default', link_state });

describe('VmNetCard link texts', () => {
  it('speaks English in the English UI (no hardcoded Spanish)', () => {
    setLocale('en');
    const up = render({ state: 'running', networks: [nic('up')] });
    expect(up.textContent).toContain('Link up');
    expect(up.textContent).toContain('Disconnect cable');
    for (const es of [
      'Enlace activo',
      'Desconectar cable',
      'Conectar cable',
      'Cable desconectado',
    ]) {
      expect(up.textContent).not.toContain(es);
    }
    unmount(app);
    target.remove();
    const down = render({ state: 'running', networks: [nic('down')] });
    expect(down.textContent).toContain('Cable unplugged');
    expect(down.textContent).toContain('Connect cable');
    expect(
      [...down.querySelectorAll('button[title]')].map((b) => b.getAttribute('title'))
    ).toContain('Plug or unplug the virtual cable while the VM runs');
  });

  it('has Spanish and Catalan translations', () => {
    setLocale('es');
    expect(render({ state: 'running', networks: [nic('up')] }).textContent).toContain(
      'Enlace activo'
    );
    unmount(app);
    target.remove();
    setLocale('ca');
    expect(render({ state: 'running', networks: [nic('down')] }).textContent).toContain(
      'Connecta el cable'
    );
  });
});
