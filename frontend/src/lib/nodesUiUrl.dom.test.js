import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({ nodes: [], updates: [], creates: [], testResult: { ok: true } }));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = new Proxy(
    {
      listNodes: () => Promise.resolve({ nodes: fx.nodes }),
      pingAllNodes: () => Promise.resolve({ nodes: fx.nodes }),
      testNode: () => Promise.resolve(fx.testResult),
      createNode: (...a) => {
        fx.creates.push(a);
        return Promise.resolve({});
      },
      updateNode: (id, body) => {
        fx.updates.push([id, body]);
        return Promise.resolve({});
      },
      getClusterSSH: () => Promise.resolve({ configured: false, nodes: [] }),
      getClusterSummary: () => Promise.resolve(null),
      getIncusClusterMembers: () => Promise.resolve(null),
      getHAModule: () =>
        Promise.resolve({ enabled: false, busy: false, status: { state: 'installed', steps: [] } }),
      listHAWorkloads: () =>
        Promise.resolve({ workloads: [], preconditions: { ok: true, blockers: [] } }),
    },
    {
      get(target, prop) {
        if (prop in target) return target[prop];
        if (typeof prop !== 'string') return undefined;
        return () => Promise.resolve([]);
      },
    }
  );
  return { api, auth: { isAdmin: () => true, user: 'admin', role: 'admin' } };
});

import Nodes from '../routes/Nodes.svelte';
import NodeInstallGuide from './components/NodeInstallGuide.svelte';
import RemoteUnsupportedNotice from './components/RemoteUnsupportedNotice.svelte';
import { clusterModule } from './stores/clusterModule.svelte.js';

const settle = async () => {
  for (let i = 0; i < 8; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};
const btn = (root, text) =>
  [...root.querySelectorAll('button')].find((b) => (b.textContent || '').trim().includes(text));
const setVal = (root, sel, v) => {
  const el = root.querySelector(sel);
  el.value = v;
  el.dispatchEvent(new Event('input', { bubbles: true }));
};

let mounted = null;
async function render(Comp, props = {}) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(Comp, { target, props });
  mounted = { app, target };
  await settle();
  return target;
}

const node = (over = {}) => ({
  id: 'n1',
  name: 'lab1',
  uri: 'qemu+ssh://root@10.20.30.10/system',
  backend: 'libvirt',
  type: 'remote',
  enabled: true,
  status: 'online',
  latency_ms: 3,
  ...over,
});

beforeEach(() => {
  localStorage.setItem('webkvm.locale', 'en');
  Object.assign(fx, { nodes: [], updates: [], creates: [], testResult: { ok: true } });
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

describe('node card addresses', () => {
  it('private node without ui_url: badge, hint and a disabled Open WebKVM with the reason', async () => {
    fx.nodes = [node()];
    const t = await render(Nodes);
    const card = t.querySelector('[data-testid=node-addresses-n1]');
    expect(card.textContent).toContain('Cluster address');
    expect(card.textContent).toContain('10.20.30.10');
    expect(card.textContent).toContain('Your network');
    expect(card.textContent).toContain('Not set');
    expect(t.querySelector('[data-testid=node-private-n1]').textContent).toContain(
      'Private network'
    );
    expect(t.textContent).toContain(
      'Set the address of this node on your network to open it from your PC'
    );
    expect(t.querySelector('a[href^="https://10.20.30.10"]')).toBeNull();
    const open = btn(t, 'Open WebKVM');
    expect(open.disabled).toBe(true);
    expect(open.title).toContain('private cluster address');
  });

  it('with ui_url: shows both addresses with copy buttons, no badge, link uses ui_url', async () => {
    fx.nodes = [node({ ui_url: 'https://192.168.50.40:8080' })];
    const t = await render(Nodes);
    expect(t.querySelector('[data-testid=node-ui-url-n1]').textContent).toContain(
      'https://192.168.50.40:8080'
    );
    expect(t.querySelector('[data-testid=node-private-n1]')).toBeNull();
    expect(t.querySelector('a[href="https://192.168.50.40:8080/"]')).toBeTruthy();
    expect(t.querySelector('[aria-label="Copy the Cluster address of lab1"]')).toBeTruthy();
    expect(t.querySelector('[aria-label="Copy the Your network of lab1"]')).toBeTruthy();
  });

  it('public host without ui_url keeps the derived link and shows no badge', async () => {
    fx.nodes = [node({ uri: 'qemu+ssh://root@node1.example/system' })];
    const t = await render(Nodes);
    expect(t.querySelector('a[href="https://node1.example:8080/"]')).toBeTruthy();
    expect(t.querySelector('[data-testid=node-private-n1]')).toBeNull();
  });
});

describe('edit round-trip', () => {
  it('prefills ui_url, sends the edited value and clears with an empty one', async () => {
    fx.nodes = [node({ ui_url: 'https://192.168.50.40:8080' })];
    const t = await render(Nodes);
    t.querySelector('[aria-label="Edit node lab1"]').click();
    await settle();
    const input = t.querySelector('#node-edit-ui-url-n1');
    expect(input.value).toBe('https://192.168.50.40:8080');
    setVal(t, '#node-edit-ui-url-n1', 'http://lab.lan/');
    await settle();
    btn(t, 'Save').click();
    await settle();
    expect(fx.updates[0][0]).toBe('n1');
    expect(fx.updates[0][1]).toMatchObject({ name: 'lab1', ui_url: 'http://lab.lan' });

    t.querySelector('[aria-label="Edit node lab1"]').click();
    await settle();
    setVal(t, '#node-edit-ui-url-n1', '');
    await settle();
    btn(t, 'Save').click();
    await settle();
    expect(fx.updates[1][1].ui_url).toBe('');
  });

  it('blocks an invalid address client-side with an inline, associated error', async () => {
    fx.nodes = [node()];
    const t = await render(Nodes);
    t.querySelector('[aria-label="Edit node lab1"]').click();
    await settle();
    setVal(t, '#node-edit-ui-url-n1', 'ftp://x');
    await settle();
    const input = t.querySelector('#node-edit-ui-url-n1');
    expect(input.getAttribute('aria-invalid')).toBe('true');
    expect(t.querySelector('[data-testid=ui-url-error]').textContent).toContain('http://');
    btn(t, 'Save').click();
    await settle();
    expect(fx.updates).toHaveLength(0);
  });

  it('shows the private explanation, never a suggestion, for a private URI', async () => {
    fx.nodes = [node()];
    const t = await render(Nodes);
    t.querySelector('[aria-label="Edit node lab1"]').click();
    await settle();
    expect(t.querySelector('[data-testid=ui-url-suggest]')).toBeNull();
    expect(t.querySelector('[data-testid=ui-url-private]').textContent).toContain('10.20.30.10');
  });

  it('offers a one-click suggestion for a public URI host', async () => {
    fx.nodes = [node({ uri: 'qemu+ssh://root@node1.example/system' })];
    const t = await render(Nodes);
    t.querySelector('[aria-label="Edit node lab1"]').click();
    await settle();
    const s = t.querySelector('[data-testid=ui-url-suggest]');
    expect(s.textContent).toContain('Use https://node1.example:8080');
    s.click();
    await settle();
    expect(t.querySelector('#node-edit-ui-url-n1').value).toBe('https://node1.example:8080');
    expect(t.querySelector('[data-testid=ui-url-suggest]')).toBeNull();
  });
});

describe('add form', () => {
  it('sends ui_url to createNode and suggests only for a public host', async () => {
    const t = await render(Nodes);
    btn(t, 'Add node').click();
    await settle();
    setVal(t, '#node-add-name', 'lab2');
    setVal(t, '#node-add-user', 'root');
    setVal(t, '#node-add-host', '10.20.30.11');
    await settle();
    expect(t.querySelector('[data-testid=ui-url-suggest]')).toBeNull();
    expect(t.querySelector('[data-testid=ui-url-private]')).toBeTruthy();
    setVal(t, '#node-add-host', 'pub.example');
    await settle();
    expect(t.querySelector('[data-testid=ui-url-suggest]').textContent).toContain(
      'https://pub.example:8080'
    );
    setVal(t, '#node-add-host', '10.20.30.11');
    setVal(t, '#node-add-ui-url', 'https://192.168.1.41:8080');
    await settle();
    btn(t, 'Test').click();
    await settle();
    btn(t, 'Save').click();
    await settle();
    expect(fx.creates).toHaveLength(1);
    expect(fx.creates[0][0]).toBe('lab2');
    expect(fx.creates[0][5]).toBe('https://192.168.1.41:8080');
  });
});

describe('links in other components', () => {
  it('NodeInstallGuide links ui_url, and explains private nodes without a link', async () => {
    const t = await render(NodeInstallGuide, {
      nodes: [
        {
          id: 'a',
          name: 'a',
          type: 'remote',
          uri: 'qemu+ssh://u@10.0.0.2/system',
          ui_url: 'https://192.168.1.5:8080',
        },
        { id: 'b', name: 'b', type: 'remote', uri: 'qemu+ssh://u@10.0.0.3/system' },
      ],
    });
    expect(t.querySelector('[data-testid=install-node-a] a').getAttribute('href')).toBe(
      'https://192.168.1.5:8080/'
    );
    expect(t.querySelector('[data-testid=install-node-b] a')).toBeNull();
    expect(t.querySelector('[data-testid=install-node-nourl-b]').textContent).toContain(
      'private cluster address'
    );
  });

  it('RemoteUnsupportedNotice prefers ui_url (registry or 501 body) over a private URI', async () => {
    let t = await render(RemoteUnsupportedNotice, {
      node: { name: 'x', uri: 'qemu+ssh://u@10.0.0.9/system', ui_url: 'https://192.168.1.9:8080' },
    });
    expect(t.querySelector('a').getAttribute('href')).toBe('https://192.168.1.9:8080/');
    unmount(mounted.app);
    t.remove();
    t = await render(RemoteUnsupportedNotice, { nodeName: 'x', uiUrl: 'http://x.lan' });
    expect(t.querySelector('a').getAttribute('href')).toBe('http://x.lan/');
    unmount(mounted.app);
    t.remove();
    t = await render(RemoteUnsupportedNotice, {
      node: { name: 'x', uri: 'qemu+ssh://u@10.0.0.9/system' },
    });
    expect(t.querySelector('a')).toBeNull();
    expect(t.querySelector('[data-testid=remote-unsupported-nouiurl]')).toBeTruthy();
  });
});
