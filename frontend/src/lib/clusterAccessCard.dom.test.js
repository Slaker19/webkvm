import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  calls: [],
  testResult: null,
  pinError: null,
  capture: null,
  rotTest: null,
  commitError: null,
}));

vi.mock('$lib/stores/auth.svelte.js', () => ({
  api: {
    generateClusterSSHKey: () => {
      fx.calls.push(['generate']);
      return Promise.resolve({ configured: true });
    },
    getNodeSSHAuthorize: (id) => {
      fx.calls.push(['authorize', id]);
      return Promise.resolve({
        command: 'echo KEYLINE >> ~/.ssh/authorized_keys',
        authorized_keys_line: 'restrict ssh-ed25519 AAAA',
      });
    },
    captureNodeHostKey: (id) => {
      fx.calls.push(['hostkey', id]);
      return Promise.resolve(
        fx.capture || { fingerprint: 'SHA256:abc', key_type: 'ssh-ed25519', state: 'unknown' }
      );
    },
    pinNodeHostKey: (id, fp, replace) => {
      fx.calls.push(['pin', id, fp, replace]);
      return fx.pinError ? Promise.reject(fx.pinError) : Promise.resolve({ ok: true });
    },
    testNodeSSH: (id) => {
      fx.calls.push(['test', id]);
      return Promise.resolve(fx.testResult);
    },
    startClusterSSHRotation: () => {
      fx.calls.push(['rotate']);
      return Promise.resolve({
        pending: { fingerprint: 'SHA256:new', command: 'NEWCMD', authorized_keys_line: 'l' },
      });
    },
    commitClusterSSHRotation: (opts) => {
      fx.calls.push(['commit', !!opts?.force]);
      return fx.commitError ? Promise.reject(fx.commitError) : Promise.resolve({ ok: true });
    },
    testClusterSSHRotation: () => {
      fx.calls.push(['rotation-test']);
      return Promise.resolve(fx.rotTest);
    },
    rollbackClusterSSHRotation: () => {
      fx.calls.push(['rollback']);
      return Promise.resolve({ ok: true });
    },
  },
}));

import ClusterAccessCard from './components/ClusterAccessCard.svelte';

const settle = async (n = 6) => {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => setTimeout(r, 3));
    flushSync();
  }
};
const btn = (text) =>
  [...document.querySelectorAll('button')].find((b) => (b.textContent || '').trim().includes(text));
const node = (over = {}) => ({
  id: 'n1',
  name: 'kvm-east',
  user: 'root',
  host: '10.0.0.5',
  port: 22,
  host_key: { state: 'unknown', fingerprint: '' },
  authorized: null,
  last_test: null,
  ...over,
});
const overview = (over = {}) => ({
  configured: true,
  algorithm: 'ed25519',
  fingerprint: 'SHA256:cluster-key-fingerprint-long',
  public_key: 'ssh-ed25519 AAAA',
  created_at: '2026-11-12',
  transport: 'libssh2',
  nodes: [node()],
  ...over,
});

let mounted;
const refresh = vi.fn(() => Promise.resolve());
async function show(ov, extra = {}) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(ClusterAccessCard, {
    target,
    props: { overview: ov, onRefresh: refresh, ...extra },
  });
  mounted = { app, target };
  await settle();
  return target;
}

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  fx.calls = [];
  fx.testResult = null;
  fx.pinError = null;
  fx.capture = null;
  fx.rotTest = null;
  fx.commitError = null;
  refresh.mockClear();
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: vi.fn(() => Promise.resolve()) },
  });
});
afterEach(() => {
  if (mounted) {
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
  }
  document.body.innerHTML = '';
});

describe('ClusterAccessCard', () => {
  it('offers to generate the key first and explains WebKVM never pushes it', async () => {
    const t = await show(overview({ configured: false, nodes: [node()] }));
    expect(t.textContent).toContain('never');
    btn('Generate cluster key').click();
    await settle();
    expect(fx.calls).toContainEqual(['generate']);
    expect(refresh).toHaveBeenCalled();
  });

  it('shows the authorize command with where to run it, and copies it', async () => {
    const t = await show(overview());
    btn('Set up access').click();
    await settle();
    expect(t.textContent).toContain('echo KEYLINE');
    expect(t.textContent).toContain('Run it ON the node 10.0.0.5, as user root');
    btn('Copy').click();
    await settle();
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(
      'echo KEYLINE >> ~/.ssh/authorized_keys'
    );
  });

  it('requires the fingerprint checkbox before pinning', async () => {
    const t = await show(overview({ nodes: [node({ authorized: true })] }));
    btn('Set up access').click();
    await settle();
    btn('Check host key').click();
    await settle();
    expect(t.textContent).toContain('SHA256:abc');
    // The key type is always shown (not only in Advanced) so the admin compares
    // the right line of ssh_host_*_key.pub.
    expect(t.textContent).toContain('ssh-ed25519');
    const pin = btn('Pin host key');
    expect(pin.disabled).toBe(true);
    const box = t.querySelector('input[type=checkbox]');
    box.checked = true;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect(btn('Pin host key').disabled).toBe(false);
    btn('Pin host key').click();
    await settle();
    expect(fx.calls).toContainEqual(['pin', 'n1', 'SHA256:abc', false]);
  });

  it('changed host key shows a red warning and replaces only after confirmation', async () => {
    fx.capture = { fingerprint: 'SHA256:new', key_type: 'ssh-ed25519', state: 'changed' };
    const t = await show(
      overview({ nodes: [node({ host_key: { state: 'changed', fingerprint: 'SHA256:old' } })] })
    );
    expect(t.textContent).toContain('Host key changed');
    btn('Set up access').click();
    await settle();
    expect(t.textContent).toContain('The host key of kvm-east changed');
    btn('Check host key').click();
    await settle();
    const box = t.querySelector('input[type=checkbox]');
    box.checked = true;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    btn('Replace pinned key').click();
    await settle();
    expect(fx.calls.some((c) => c[0] === 'pin')).toBe(false);
    expect(document.body.textContent).toContain('Replace the pinned host key of kvm-east?');
    btn('Replace the key').click();
    await settle();
    expect(fx.calls).toContainEqual(['pin', 'n1', 'SHA256:new', true]);
  });

  it('shows translated test codes, hint and the transport warning', async () => {
    fx.testResult = {
      ok: false,
      code: 'transport_unsupported',
      transport: 'libssh2',
      ssh: { ok: true },
      libvirt: { ok: false, code: 'transport_unsupported' },
      hint: 'backend hint',
    };
    const t = await show(
      overview({ nodes: [node({ host_key: { state: 'pinned', fingerprint: 'SHA256:x' } })] })
    );
    btn('Set up access').click();
    await settle();
    btn('Test connection').click();
    await settle();
    expect(fx.calls).toContainEqual(['test', 'n1']);
    expect(t.textContent).toContain('does not support the chosen transport');
    expect(t.textContent).toContain('cannot use the configured SSH transport');
    expect(t.textContent).toContain('Set cluster.libvirt_transport to libssh2, libssh or ssh');
  });

  it('falls back for unknown codes without showing the raw key', async () => {
    fx.testResult = {
      ok: false,
      code: 'brand_new',
      ssh: { ok: false, code: 'brand_new' },
      libvirt: { ok: false },
      hint: 'Some hint',
    };
    const t = await show(
      overview({
        nodes: [node({ host_key: { state: 'pinned', fingerprint: 'SHA256:x' }, authorized: true })],
      })
    );
    btn('Set up access').click();
    await settle();
    btn('Test connection').click();
    await settle();
    expect(t.textContent).toContain('Unrecognized result: brand_new');
    expect(t.textContent).toContain('Some hint');
    expect(t.textContent).not.toContain('clusterAccess.');
  });

  it('says when the libvirt client forced the libssh fallback', async () => {
    const t = await show(
      overview({
        transport: 'libssh',
        transport_configured: 'libssh2',
        transport_fallback: 'libssh2_unsupported',
      })
    );
    const note = t.querySelector('[data-testid="transport-fallback"]');
    expect(note?.textContent).toContain('no libssh2 support');
    const plain = await show(overview());
    expect(plain.querySelector('[data-testid="transport-fallback"]')).toBeNull();
  });

  it('advanced mode shows raw line, port and transport', async () => {
    const t = await show(overview());
    btn('Advanced').click();
    await settle();
    expect(t.textContent).toContain('root@10.0.0.5:22');
    expect(t.textContent).toContain('libssh');
    btn('Set up access').click();
    await settle();
    expect(t.textContent).toContain('restrict ssh-ed25519 AAAA');
  });

  it('rotation: start asks the API and shows the new key per node', async () => {
    const t = await show(overview());
    t.querySelector('details').open = true;
    btn('Start rotation').click();
    await settle();
    expect(fx.calls).toContainEqual(['rotate']);
  });

  const pendingOv = (over = {}) =>
    overview({
      pending: { fingerprint: 'SHA256:new', public_key: 'ssh-ed25519 NEWKEY' },
      nodes: [
        node({ id: 'a', name: 'kvm-a', enabled: true, pending_verified: false }),
        node({ id: 'b', name: 'kvm-b', enabled: true, pending_verified: false }),
      ],
      ...over,
    });
  const commitBtn = () => btn('Switch to the new key');
  const confirmCommit = async () => {
    commitBtn().click();
    await settle();
    [...document.querySelectorAll('button')]
      .filter((b) => b.textContent.includes('Switch to the new key'))
      .pop()
      .click();
    await settle();
  };

  it('rotation: the switch stays disabled until the new key passed on every node', async () => {
    const t = await show(pendingOv());
    t.querySelector('details').open = true;
    await settle();
    // (a) one authorize block per node
    expect(t.textContent).toContain('kvm-a (root@10.0.0.5)');
    expect(t.textContent).toContain('kvm-b (root@10.0.0.5)');
    expect(commitBtn().disabled).toBe(true);
    expect(t.querySelector('[data-testid="rotation-blocked"]')).toBeTruthy();
    // (b) test: one node refuses the key
    fx.rotTest = {
      ok: false,
      nodes: [
        { id: 'a', name: 'kvm-a', ok: true },
        { id: 'b', name: 'kvm-b', ok: false, code: 'ssh_auth_denied' },
      ],
    };
    btn('Test the new key on all nodes').click();
    await settle();
    expect(fx.calls).toContainEqual(['rotation-test']);
    const res = t.querySelector('[data-testid="rotation-results"]').textContent;
    expect(res).toContain('New key accepted');
    expect(res).toContain('The node refused the key');
    expect(res).not.toContain('ssh_auth_denied');
    expect(commitBtn().disabled).toBe(true);
    // EASY mode never offers to force
    expect(t.textContent).not.toContain('switch anyway');
    commitBtn().click();
    await settle();
    expect(fx.calls.some((c) => c[0] === 'commit')).toBe(false);
    // (c) all passed: enabled, and commit is NOT forced
    fx.rotTest = {
      ok: true,
      nodes: [
        { id: 'a', name: 'kvm-a', ok: true },
        { id: 'b', name: 'kvm-b', ok: true },
      ],
    };
    btn('Test the new key on all nodes').click();
    await settle();
    expect(commitBtn().disabled).toBe(false);
    await confirmCommit();
    expect(fx.calls).toContainEqual(['commit', false]);
  });

  it('rotation ADVANCED: forcing needs an explicit, warned checkbox listing the failing nodes', async () => {
    const t = await show(pendingOv());
    t.querySelector('details').open = true;
    btn('Advanced').click();
    await settle();
    expect(t.textContent).toContain('DANGER');
    expect(commitBtn().disabled).toBe(true);
    const box = [...t.querySelectorAll('input[type=checkbox]')].pop();
    box.checked = true;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    box.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    expect(commitBtn().disabled).toBe(false);
    await confirmCommit();
    expect(fx.calls).toContainEqual(['commit', true]);
  });

  it('rotation: a server-side rotation_unverified refusal is translated and updates the list', async () => {
    const t = await show(
      pendingOv({
        nodes: [
          node({ id: 'a', name: 'kvm-a', enabled: true, pending_verified: true }),
          node({ id: 'b', name: 'kvm-b', enabled: true, pending_verified: true }),
        ],
      })
    );
    t.querySelector('details').open = true;
    await settle();
    fx.commitError = Object.assign(new Error('raw english'), {
      code: 'rotation_unverified',
      data: { failing: [{ id: 'b', name: 'kvm-b', code: 'node_timeout' }] },
    });
    await confirmCommit();
    expect(t.textContent).toContain('has not been accepted by every node');
    expect(t.textContent).not.toContain('raw english');
    expect(t.querySelector('[data-testid="rotation-results"]').textContent).toContain(
      'did not answer in time'
    );
  });

  it('after a switch the previous key can be rolled back, with its limit explained', async () => {
    const t = await show(overview({ previous: { fingerprint: 'SHA256:oldkey' } }));
    t.querySelector('details').open = true;
    await settle();
    const box = t.querySelector('[data-testid="rotation-rollback"]');
    expect(box.textContent).toContain('SHA256:oldkey');
    expect(box.textContent).toContain('until the next successful rotation');
    btn('Roll back to the previous key').click();
    await settle();
    [...document.querySelectorAll('button')]
      .filter((b) => b.textContent.includes('Roll back to the previous key'))
      .pop()
      .click();
    await settle();
    expect(fx.calls).toContainEqual(['rollback']);
  });
});
