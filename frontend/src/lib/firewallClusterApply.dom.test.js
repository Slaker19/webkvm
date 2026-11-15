import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({ applyCalls: 0, pending: false, confirmCalls: 0 }));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const base = {
    getHAModule: () =>
      Promise.resolve({ enabled: true, busy: false, status: { state: 'installed', steps: [] } }),
    listVMs: () => Promise.resolve([]),
    getHostFirewall: () => Promise.resolve({ firewall: { input: [], forwards: [] } }),
    getClusterFirewall: () =>
      Promise.resolve({
        profile: { default: 'deny' },
        pending: fx.pending,
        deadline: fx.pending ? Math.floor(Date.now() / 1000) + 30 : undefined,
        peers: [{ peer: 'b' }],
        sync: {},
      }),
    previewClusterFirewall: () => Promise.resolve({ rendered: 'table inet webkvm {\n}\n' }),
    applyClusterFirewall: () => {
      fx.applyCalls++;
      fx.pending = true;
      return Promise.resolve({ window_secs: 30, deadline: Math.floor(Date.now() / 1000) + 30 });
    },
    confirmClusterFirewall: () => {
      fx.confirmCalls++;
      fx.pending = false;
      return Promise.resolve({});
    },
    rollbackClusterFirewall: () => Promise.resolve({ status: 'no_pending' }),
  };
  const api = new Proxy(base, {
    get: (o, k) => (k in o ? o[k] : () => Promise.resolve({})),
  });
  return { api, auth: { isAdmin: () => true, canMutate: () => true, user: 'a', role: 'admin' } };
});

import Firewall from '../routes/Firewall.svelte';

const settle = async () => {
  for (let i = 0; i < 8; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};
const btn = (root, text) =>
  [...root.querySelectorAll('button')].find((b) => (b.textContent || '').trim().startsWith(text));

let mounted = null;
beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  fx.applyCalls = 0;
  fx.pending = false;
  fx.confirmCalls = 0;
});
afterEach(() => {
  if (mounted) {
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
  }
  document.body.innerHTML = '';
});

describe('cluster firewall apply (M21)', () => {
  it('asks for confirmation with a preview and enables Confirm/Rollback only while pending', async () => {
    const target = document.createElement('div');
    document.body.appendChild(target);
    const app = mount(Firewall, { target });
    mounted = { app, target };
    await settle();

    expect(btn(target, 'Confirm').disabled).toBe(true);
    expect(btn(target, 'Rollback').disabled).toBe(true);

    btn(target, 'Apply (30').click();
    await settle();
    expect(fx.applyCalls).toBe(0);
    expect(document.body.textContent).toContain('Apply the cluster firewall?');
    expect(document.body.textContent).toContain('table inet webkvm');
    expect(document.body.textContent).toContain('1 other node');

    const confirm = [...document.body.querySelectorAll('button')]
      .filter((b) => b.textContent.trim().startsWith('Apply (30'))
      .pop();
    confirm.click();
    await settle();
    expect(fx.applyCalls).toBe(1);
    expect(btn(target, 'Confirm').disabled).toBe(false);
    expect(btn(target, 'Rollback').disabled).toBe(false);
    expect(target.textContent).toContain('confirm within');
  });
});
