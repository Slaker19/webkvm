import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({
  nodes: [],
  listError: null,
  testResult: null,
  deleteCalls: [],
  deleteError: null,
  updateCalls: 0,
  healingCalls: [],
  healingError: null,
  summary: null,
  ssh: null,
  sshTests: [],
  sshLoads: 0,
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = {
    listNodes: () =>
      fx.listError ? Promise.reject(fx.listError) : Promise.resolve({ nodes: fx.nodes }),
    getClusterSSH: () => {
      fx.sshLoads++;
      return Promise.resolve(fx.ssh || { configured: false, nodes: [] });
    },
    testNodeSSH: (id) => {
      fx.sshTests.push(id);
      return Promise.resolve({ ok: true, ssh: { ok: true }, libvirt: { ok: true } });
    },
    listHAWorkloads: () =>
      Promise.resolve({ workloads: [], preconditions: { ok: true, blockers: [] } }),
    getClusterSummary: () => Promise.resolve(fx.summary),
    getIncusClusterMembers: () => Promise.resolve(null),
    getHAModule: () =>
      Promise.resolve({
        enabled: !!fx.haEnabled,
        busy: false,
        status: { state: 'installed', steps: [] },
      }),
    getHAReadiness: () =>
      Promise.resolve({
        state: 'blocked',
        blockers: ['pacemaker_probe_unavailable', 'cluster_ha_module_disabled', 'brand_new_code'],
        warnings: ['firewall_profile_divergent', 'some_future_warning'],
        pacemaker: { state: 'x', nodes_online: 0, nodes_configured: 0, fence_devices: 0 },
        control_plane: {},
        incus_targets: [],
        fencing: {},
        install: {},
      }),
    getIncusHealing: () =>
      Promise.resolve({
        available: true,
        enabled: false,
        threshold_seconds: 0,
        can_change: true,
        members_online: 1,
        members_total: 1,
        blockers: [],
      }),
    setIncusHealing: (n) => {
      fx.healingCalls.push(n);
      return fx.healingError ? Promise.reject(fx.healingError) : Promise.resolve({ enabled: true });
    },
    testNode: () => Promise.resolve(fx.testResult),
    createNode: () => Promise.resolve({}),
    updateNode: () => {
      fx.updateCalls++;
      return new Promise((r) => setTimeout(r, 20));
    },
    deleteNode: (id, opts) => {
      fx.deleteCalls.push([id, !!opts?.force]);
      return fx.deleteError && !opts?.force ? Promise.reject(fx.deleteError) : Promise.resolve({});
    },
    pingAllNodes: () => Promise.resolve({ nodes: fx.nodes }),
  };
  return { api, auth: { isAdmin: () => true, user: 'admin', role: 'admin' } };
});

import Nodes from '../routes/Nodes.svelte';
import { clusterModule } from './stores/clusterModule.svelte.js';

const settle = async () => {
  for (let i = 0; i < 8; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};
const btn = (root, text) =>
  [...root.querySelectorAll('button')].find((b) => (b.textContent || '').trim().includes(text));
const bodyBtn = (text) => btn(document.body, text);

let mounted = null;
async function mountNodes() {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(Nodes, { target });
  mounted = { app, target };
  await settle();
  return target;
}

const libvirtNode = (over = {}) => ({
  id: 'n1',
  name: 'kvm-east',
  uri: 'qemu+ssh://root@node1.example/system',
  backend: 'libvirt',
  type: 'remote',
  enabled: true,
  status: 'online',
  latency_ms: 3,
  disk_total: 1000,
  disk_free: 1000,
  ram_total: 1000,
  ram_free: 400,
  ...over,
});

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  Object.assign(fx, {
    nodes: [],
    listError: null,
    testResult: null,
    deleteCalls: [],
    deleteError: null,
    updateCalls: 0,
    healingCalls: [],
    healingError: null,
    summary: null,
    ssh: null,
    sshTests: [],
    sshLoads: 0,
    haEnabled: false,
  });
  clusterModule.loaded = false;
  clusterModule.enabled = false;
  clusterModule.status = null;
  clusterModule.error = '';
});

afterEach(() => {
  if (mounted) {
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
  }
  document.body.innerHTML = '';
});

describe('Nodes screen', () => {
  it('shows only the error banner with Retry when listing fails', async () => {
    fx.listError = new Error('boom');
    const target = await mountNodes();
    const text = target.textContent || '';
    expect(text).toContain('Could not load nodes: boom');
    expect(text).not.toContain('No nodes are registered yet.');
    expect(text).not.toContain('Multi-host libvirt management is active');
    fx.listError = null;
    fx.nodes = [libvirtNode()];
    btn(target, 'Retry').click();
    await settle();
    expect(target.textContent).toContain('kvm-east');
  });

  it('does not claim 100% reachable with no data', async () => {
    const target = await mountNodes();
    expect(target.textContent).not.toContain('100% reachable');
    expect(target.textContent).toContain('No reachability data yet');
  });

  it('labels icon buttons with the node name and exposes usage bars', async () => {
    fx.nodes = [libvirtNode({ disk_free: 1000 })];
    const target = await mountNodes();
    expect(target.querySelector('[aria-label="Edit node kvm-east"]')).toBeTruthy();
    expect(target.querySelector('[aria-label="Delete node kvm-east"]')).toBeTruthy();
    const toggle = target.querySelector('[aria-label="Enable node kvm-east"]');
    expect(toggle.getAttribute('aria-pressed')).toBe('true');
    const bars = [...target.querySelectorAll('[role=progressbar]')];
    expect(bars.length).toBe(2);
    // An empty disk is 0%, not the old forced 5% minimum.
    expect(bars[0].getAttribute('aria-valuenow')).toBe('0');
    expect(bars[0].firstElementChild.getAttribute('style')).toContain('width: 0%');
    expect(bars[1].getAttribute('aria-valuenow')).toBe('60');
  });

  it('derives the web console from the URI and disables it when it cannot', async () => {
    fx.nodes = [libvirtNode(), libvirtNode({ id: 'n2', name: 'odd', uri: 'qemu:///system' })];
    const target = await mountNodes();
    expect(target.querySelector('a[href="https://node1.example:8080/"]')).toBeTruthy();
    const disabled = target.querySelector('button[title^="The web address"]');
    expect(disabled).toBeTruthy();
    expect(disabled.disabled).toBe(true);
  });

  it('requires the fingerprint confirmation before saving an Incus node', async () => {
    fx.testResult = {
      ok: true,
      server: { name: 'inc', version: '6' },
      server_fingerprint: 'AB:CD',
    };
    const target = await mountNodes();
    btn(target, 'Add node').click();
    await settle();
    btn(target, 'Incus').click();
    await settle();
    const set = (id, v) => {
      const el = target.querySelector('#' + id);
      el.value = v;
      el.dispatchEvent(new Event('input', { bubbles: true }));
    };
    set('node-add-name', 'inc-1');
    set('node-add-host', 'incus.example');
    set('node-incus-token', 'tok');
    await settle();
    btn(target, 'Test').click();
    await settle();
    const save = [...target.querySelectorAll('button')].filter(
      (b) => b.textContent.trim() === 'Save'
    )[0];
    expect(target.textContent).toContain('AB:CD');
    expect(save.disabled).toBe(true);
    const box = target.querySelector('[data-testid=fingerprint-verified]');
    box.checked = true;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect(save.disabled).toBe(false);
  });

  it('lets a KVM node be saved when only its managed SSH access is pending', async () => {
    fx.testResult = {
      ok: false,
      setup_pending: true,
      code: 'host_key_unpinned',
      error: "the node's SSH host key has not been confirmed",
    };
    const target = await mountNodes();
    btn(target, 'Add node').click();
    await settle();
    const set = (id, v) => {
      const el = target.querySelector('#' + id);
      el.value = v;
      el.dispatchEvent(new Event('input', { bubbles: true }));
    };
    set('node-add-name', 'kvm-2');
    set('node-add-host', 'kvm2.example');
    set('node-add-user', 'operator');
    await settle();
    const save = () =>
      [...target.querySelectorAll('button')].filter((b) => b.textContent.trim() === 'Save')[0];
    expect(save().disabled).toBe(true);
    btn(target, 'Test connection').click();
    await settle();
    expect(target.textContent).toContain('Cluster access');
    expect(save().disabled).toBe(false);
  });

  it('keeps the typed URI when switching easy to advanced and warns about qemu+tcp', async () => {
    const target = await mountNodes();
    btn(target, 'Add node').click();
    await settle();
    const set = (id, v) => {
      const el = target.querySelector('#' + id);
      el.value = v;
      el.dispatchEvent(new Event('input', { bubbles: true }));
    };
    set('node-add-host', 'node9.example');
    set('node-add-user', 'ops');
    await settle();
    btn(target, 'Advanced').click();
    await settle();
    expect(target.querySelector('#node-add-uri').value).toBe('qemu+ssh://ops@node9.example/system');
    set('node-add-uri', 'qemu+tcp://node9.example/system');
    await settle();
    expect(target.textContent).toContain('qemu+tcp:// is not allowed');
  });

  it('offers a forced delete when the Incus revoke fails', async () => {
    fx.nodes = [
      libvirtNode({ id: 'i1', name: 'inc-west', backend: 'incus', uri: 'https://i.example:8443' }),
    ];
    fx.deleteError = Object.assign(new Error('revoke failed'), {
      status: 502,
      code: 'incus_revoke_failed',
    });
    const target = await mountNodes();
    target.querySelector('[aria-label="Delete node inc-west"]').click();
    await settle();
    bodyBtn('Delete').click();
    await settle();
    expect(fx.deleteCalls).toEqual([['i1', false]]);
    expect(document.body.textContent).toContain('Remove inc-west anyway?');
    expect(document.body.textContent).toContain('client certificate');
    bodyBtn('Remove anyway (force)').click();
    await settle();
    expect(fx.deleteCalls).toEqual([
      ['i1', false],
      ['i1', true],
    ]);
  });

  it('ignores a second click on the enabled toggle while one is in flight', async () => {
    fx.nodes = [libvirtNode()];
    const target = await mountNodes();
    const toggle = target.querySelector('[aria-label="Enable node kvm-east"]');
    toggle.click();
    toggle.click();
    await settle();
    expect(fx.updateCalls).toBe(1);
  });

  it('translates every blocker/warning, with a fallback for unknown codes', async () => {
    fx.haEnabled = true;
    const target = await mountNodes();
    btn(target, 'Check HA readiness').click();
    await settle();
    const text = target.textContent || '';
    expect(text).toContain('The Pacemaker probe is unavailable');
    expect(text).toContain('The Cluster/HA module is disabled on this node');
    expect(text).toContain('Unrecognized requirement: brand_new_code');
    expect(text).toContain('Unrecognized warning: some_future_warning');
    expect(text).not.toContain('nodes.haBlockers');
  });

  it('confirms before enabling Incus healing and lists translated 409 blockers', async () => {
    fx.haEnabled = true;
    fx.healingError = Object.assign(new Error('blocked'), {
      status: 409,
      code: 'incus_healing_blocked',
      data: { blockers: ['pacemaker_probe_unavailable'] },
    });
    const target = await mountNodes();
    btn(target, 'Check HA readiness').click();
    await settle();
    [...target.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Advanced').click();
    await settle();
    const input = target.querySelector('input[type=number][max="86400"]');
    input.value = '30';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    btn(target, 'Save').click();
    await settle();
    expect(fx.healingCalls).toEqual([]);
    expect(target.textContent).toContain('Enter 0 (off) or a value between 60 and 86400');
    input.value = '300';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    btn(target, 'Save').click();
    await settle();
    expect(document.body.textContent).toContain('Enable automatic healing?');
    expect(fx.healingCalls).toEqual([]);
    bodyBtn('Enable healing').click();
    await settle();
    expect(fx.healingCalls).toEqual([300]);
    expect(document.body.textContent).toContain('Healing is blocked by unmet HA requirements');
  });

  it('renders degraded nodes with translated text and disables the local toggle', async () => {
    fx.nodes = [
      libvirtNode({ id: 'l', name: 'host', type: 'local' }),
      libvirtNode({ status: 'degraded', last_error: 'insecure qemu+tcp' }),
      libvirtNode({ id: 'n3', name: 'off', enabled: false, status: 'unknown' }),
    ];
    const target = await mountNodes();
    expect(target.textContent).toContain('Degraded');
    expect(target.textContent).toContain('Re-add it with qemu+ssh://');
    expect(target.querySelector('[aria-label="Enable node host"]').disabled).toBe(true);
    // 1 enabled node is degraded -> counted as offline; the disabled one is ignored.
    expect(target.textContent).toContain('1 offline');
    expect(target.textContent).not.toContain('100% reachable');
  });
});

describe('Nodes screen visual/UX regressions', () => {
  it('puts the load-error banner right under the header and hides zeros and the install guide', async () => {
    fx.listError = new Error('boom');
    const target = await mountNodes();
    expect(target.querySelector('[data-testid=summary-cards]')).toBeNull();
    expect(target.textContent).not.toContain('Install on each node');
    const alert = target.querySelector('[role=alert]');
    const h1 = target.querySelector('h1');
    expect(alert.textContent).toContain('Could not load nodes: boom');
    expect(h1.compareDocumentPosition(alert) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    const cluster = [...target.querySelectorAll('h2')].find((h) =>
      h.textContent.includes('Cluster & HA')
    );
    expect(alert.compareDocumentPosition(cluster) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('dims (does not hide) the summary when a refresh fails but nodes are known', async () => {
    fx.nodes = [libvirtNode()];
    const target = await mountNodes();
    expect(target.querySelector('[data-testid=summary-cards]').className).not.toContain(
      'opacity-60'
    );
    fx.listError = new Error('later');
    target.querySelector('[aria-label="Enable node kvm-east"]').click();
    await new Promise((r) => setTimeout(r, 60));
    await settle();
    expect(target.textContent).toContain('Could not load nodes: later');
    expect(target.querySelector('[data-testid=summary-cards]').className).toContain('opacity-60');
    expect(target.textContent).toContain('kvm-east');
  });

  it('total card uses summary.total_nodes and hints how many nodes are disabled', async () => {
    fx.nodes = [
      libvirtNode(),
      libvirtNode({ id: 'n2', name: 'two' }),
      libvirtNode({ id: 'n3', name: 'off', enabled: false, status: 'offline' }),
    ];
    fx.summary = { total_nodes: 2, online_nodes: 2, offline_nodes: 0, avg_latency_ms: 4 };
    const target = await mountNodes();
    const card = target.querySelector('[data-testid=summary-total]');
    expect(card.textContent).toContain('2');
    expect(card.textContent).not.toContain('3');
    expect(target.querySelector('[data-testid=disabled-hint]').textContent).toContain(
      '(1 disabled)'
    );
  });

  it('shows an em dash instead of 0 ms when no node answered', async () => {
    fx.nodes = [libvirtNode({ status: 'offline' })];
    fx.summary = { total_nodes: 1, online_nodes: 0, offline_nodes: 1, avg_latency_ms: 0 };
    const target = await mountNodes();
    const card = target.querySelector('[data-testid=summary-latency]');
    expect(card.textContent).toContain('—');
    expect(card.textContent).not.toContain('0 ms');
  });

  it('opens the add form right under the header, focuses the name field and restores focus on cancel', async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    fx.nodes = [libvirtNode()];
    const target = await mountNodes();
    btn(target, 'Add node').click();
    await settle();
    const card = target.querySelector('[data-testid=add-node-card]');
    expect(card).toBeTruthy();
    const summary = target.querySelector('[data-testid=summary-cards]');
    expect(card.compareDocumentPosition(summary) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(scroll).toHaveBeenCalledWith(expect.objectContaining({ block: 'start' }));
    expect(document.activeElement.id).toBe('node-add-name');
    btn(card, 'Cancel').click();
    await settle();
    expect(target.querySelector('[data-testid=add-node-card]')).toBeNull();
    expect(document.activeElement).toBe(btn(target, 'Add node'));
    delete Element.prototype.scrollIntoView;
  });

  it('uses instant scrolling when the user prefers reduced motion', async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    const mm = window.matchMedia;
    window.matchMedia = () => ({ matches: true });
    const target = await mountNodes();
    btn(target, 'Add node').click();
    await settle();
    expect(scroll).toHaveBeenCalledWith({ block: 'start', behavior: 'auto' });
    window.matchMedia = mm;
    delete Element.prototype.scrollIntoView;
  });

  it('translates the node status badges in Spanish and Catalan', async () => {
    fx.nodes = [
      libvirtNode({ status: 'offline', latency_ms: 0 }),
      libvirtNode({ id: 'n2', name: 'u', status: 'unknown' }),
    ];
    for (const [loc, offline, unknown] of [
      ['es', 'Sin conexión', 'Desconocido'],
      ['ca', 'Sense connexió', 'Desconegut'],
    ]) {
      localStorage.setItem('webkvm.locale', loc);
      const { setLocale } = await import('./i18n.svelte.js');
      setLocale(loc);
      const target = await mountNodes();
      expect(target.textContent).toContain(offline);
      expect(target.textContent).toContain(unknown);
      expect(target.textContent).not.toContain('Offline');
      unmount(mounted.app);
      mounted.target.remove();
      mounted = null;
    }
    const { setLocale } = await import('./i18n.svelte.js');
    setLocale('en');
  });

  it('reaches the healing seconds field and Save by label and confirms when seconds > 0', async () => {
    fx.haEnabled = true;
    const target = await mountNodes();
    btn(target, 'Check HA readiness').click();
    await settle();
    [...target.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Advanced').click();
    await settle();
    const input = target.querySelector('[aria-label="Evacuation threshold (seconds)"]');
    expect(input).toBeTruthy();
    expect(target.querySelector('label[for=healing-seconds]')).toBeTruthy();
    input.value = '120';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    target.querySelector('#healing-save').click();
    await settle();
    expect(document.body.textContent).toContain('Enable automatic healing?');
    expect(document.body.textContent).toContain('120');
    expect(fx.healingCalls).toEqual([]);
  });
});

describe('Nodes cluster access', () => {
  const ssh = (hk, last) => ({
    configured: true,
    fingerprint: 'SHA256:k',
    transport: 'libssh',
    nodes: [
      {
        id: 'n1',
        name: 'kvm-east',
        user: 'root',
        host: 'node1.example',
        port: 22,
        host_key: hk,
        last_test: last,
      },
    ],
  });

  it('shows the card and a "not set up" badge with a Pin shortcut', async () => {
    fx.nodes = [libvirtNode()];
    fx.ssh = ssh({ state: 'unknown', fingerprint: '' }, null);
    const target = await mountNodes();
    expect(target.textContent).toContain('Cluster access (managed SSH)');
    const badge = target.querySelector('[data-testid="node-access-n1"]');
    expect(badge.textContent).toContain('Access not set up');
    expect(btn(badge, 'Pin key')).toBeTruthy();
  });

  it('shows ready and the Test shortcut runs the SSH test', async () => {
    fx.nodes = [libvirtNode()];
    fx.ssh = ssh(
      { state: 'pinned', fingerprint: 'SHA256:a' },
      { ok: true, at: new Date().toISOString() }
    );
    const target = await mountNodes();
    const badge = target.querySelector('[data-testid="node-access-n1"]');
    expect(badge.textContent).toContain('Access ready');
    btn(badge, 'Test').click();
    await settle();
    expect(fx.sshTests).toEqual(['n1']);
  });

  it('a pinned node never tested is "not verified" (neutral) with a Test now button', async () => {
    fx.nodes = [libvirtNode()];
    fx.ssh = ssh({ state: 'pinned', fingerprint: 'SHA256:a' }, null);
    const target = await mountNodes();
    const badge = target.querySelector('[data-testid="node-access-n1"]');
    expect(badge.textContent).toContain('Access not verified — test it');
    expect(badge.textContent).not.toContain('not set up');
    btn(badge, 'Test now').click();
    await settle();
    expect(fx.sshTests).toEqual(['n1']);
  });

  // 8.26 defect 1: the node-list probe already logged in (access:'ok'); the
  // badge and the Cluster access card must agree with it, never "not verified".
  it('a node whose probe says access ok is ready everywhere, with the last test as secondary info', async () => {
    fx.nodes = [{ ...libvirtNode(), status: 'online', access: 'ok' }];
    fx.ssh = ssh(
      { state: 'pinned', fingerprint: 'SHA256:a' },
      { ok: true, at: new Date(Date.now() - 4 * 3600_000).toISOString() }
    );
    const target = await mountNodes();
    const badge = target.querySelector('[data-testid="node-access-n1"]');
    expect(badge.textContent).toContain('Access ready');
    const card = target.querySelector('[data-testid="cluster-access-card"]');
    expect(card.textContent).not.toContain('not verified');
    expect(card.querySelector('[data-testid="access-state-n1"]').textContent).toContain(
      'Access ready'
    );
    const secondary = card.querySelector('[data-testid="access-secondary-n1"]');
    expect(secondary.textContent).toContain('Last explicit test');
  });

  // 8.26 defect 2: the self-duplicate entry is this same server.
  it('never lists the is_self entry in Cluster access', async () => {
    fx.nodes = [libvirtNode()];
    const o = ssh({ state: 'pinned', fingerprint: 'SHA256:a' }, null);
    o.nodes.push({
      id: 'dup',
      name: 'this-box',
      user: 'operator',
      host: '10.20.30.12',
      port: 22,
      host_key: { state: 'unknown' },
      is_self: true,
    });
    fx.ssh = o;
    const target = await mountNodes();
    const card = target.querySelector('[data-testid="cluster-access-card"]');
    expect(card.textContent).toContain('kvm-east');
    expect(card.textContent).not.toContain('this-box');
    expect(card.querySelector('#access-node-dup')).toBeNull();
  });

  it('refreshes the Cluster access card when a node is deleted', async () => {
    fx.nodes = [libvirtNode()];
    fx.ssh = ssh({ state: 'pinned', fingerprint: 'SHA256:a' }, null);
    const target = await mountNodes();
    expect(target.querySelector('[data-testid="cluster-access-card"]').textContent).toContain(
      'kvm-east'
    );
    const before = fx.sshLoads;
    // The backend forgets the node: the next overview no longer lists it.
    fx.ssh = { configured: true, nodes: [] };
    fx.nodes = [];
    target.querySelector('[aria-label="Delete node kvm-east"]').click();
    await settle();
    bodyBtn('Delete').click();
    await settle();
    expect(fx.sshLoads).toBeGreaterThan(before);
    expect(target.querySelector('[data-testid="cluster-access-card"]').textContent).not.toContain(
      'kvm-east'
    );
  });

  it('refreshes the Cluster access card when a node is edited', async () => {
    fx.nodes = [libvirtNode()];
    fx.ssh = ssh({ state: 'pinned', fingerprint: 'SHA256:a' }, null);
    const target = await mountNodes();
    const before = fx.sshLoads;
    target.querySelector('[aria-label="Edit node kvm-east"]').click();
    await settle();
    bodyBtn('Save').click();
    await new Promise((r) => setTimeout(r, 60));
    await settle();
    expect(fx.updateCalls).toBe(1);
    expect(fx.sshLoads).toBeGreaterThan(before);
  });

  it('shows a red "Host key changed" badge', async () => {
    fx.nodes = [libvirtNode()];
    fx.ssh = ssh({ state: 'changed', fingerprint: 'SHA256:a' }, null);
    const target = await mountNodes();
    const b = target.querySelector('[data-testid="access-state-n1"]');
    expect(b.className).toContain('text-destructive');
  });

  it.each(['tcp_reachable', 'reachable'])(
    'a bare TCP handshake (%s) says "SSH port open", never plain reachable, next to "not set up"',
    async (status) => {
      fx.nodes = [libvirtNode({ libvirt_status: status })];
      fx.ssh = ssh({ state: 'unknown', fingerprint: '' }, null);
      const target = await mountNodes();
      const chip = target.querySelector('[data-testid="node-ssh-port-open-n1"]');
      expect(chip.textContent).toContain('SSH port open');
      expect(target.querySelector('[data-testid="node-access-n1"]').textContent).toContain(
        'Access not set up'
      );
    }
  );

  it('a node answered by its WebKVM API has no "SSH port open" chip', async () => {
    fx.nodes = [libvirtNode({ libvirt_status: 'ok' })];
    fx.ssh = ssh({ state: 'unknown', fingerprint: '' }, null);
    const target = await mountNodes();
    expect(target.querySelector('[data-testid="node-ssh-port-open-n1"]')).toBeNull();
  });

  it('mounts the HA workloads panel only when the module is enabled', async () => {
    fx.nodes = [libvirtNode()];
    let target = await mountNodes();
    expect(target.querySelector('[data-testid="ha-workloads-panel"]')).toBeNull();
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
    fx.haEnabled = true;
    target = await mountNodes();
    expect(target.querySelector('[data-testid="ha-workloads-panel"]')).toBeTruthy();
  });
});
