import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

const fx = vi.hoisted(() => ({ writes: [], ticketError: null, locale: 'en' }));

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    constructor() {
      this.options = {};
      this.unicode = {};
      this.cols = 80;
      this.rows = 24;
    }
    loadAddon() {}
    open() {}
    write(s) {
      fx.writes.push(String(s));
    }
    focus() {}
    onData() {}
    onBinary() {}
    onResize() {}
    attachCustomKeyEventHandler() {}
    hasSelection() {
      return false;
    }
    dispose() {}
    reset() {}
    clear() {}
  },
}));
const Addon = vi.hoisted(
  () =>
    class {
      onContextLoss() {}
      fit() {}
      dispose() {}
      activate() {}
    }
);
vi.mock('@xterm/addon-fit', () => ({ FitAddon: Addon }));
vi.mock('@xterm/addon-webgl', () => ({ WebglAddon: Addon }));
vi.mock('@xterm/addon-unicode11', () => ({ Unicode11Addon: Addon }));
vi.mock('@xterm/addon-web-links', () => ({ WebLinksAddon: Addon }));
vi.mock('@xterm/addon-clipboard', () => ({ ClipboardAddon: Addon }));
vi.mock('@xterm/xterm/css/xterm.css', () => ({}));
vi.mock('$lib/stores/auth.svelte.js', () => ({
  api: {
    getConsoleTicket: () => Promise.reject(fx.ticketError),
    getSerialWsUrl: () => 'ws://x/serial',
  },
  auth: { isAdmin: () => true },
}));

import TerminalPanel from './components/TerminalPanel.svelte';
import { setLocale } from './i18n.svelte.js';

const settle = async () => {
  for (let i = 0; i < 10; i++) {
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
};
let app, target;
beforeEach(() => {
  fx.writes = [];
  target = document.createElement('div');
  document.body.appendChild(target);
});
afterEach(async () => {
  setLocale('en');
  if (app) await unmount(app);
  target.remove();
});

describe('serial console ticket errors are translated', () => {
  it.each([
    ['en', 'console_busy', 'Too many consoles'],
    ['en', 'ticket_scope', 'ticket belongs to another node'],
    ['en', 'vm_not_running', 'not running'],
    ['en', 'remote_unsupported', "node's own WebKVM"],
    ['en', 'host_key_unpinned', 'Pin it in Cluster access'],
    ['es', 'host_key_unpinned', 'Fíjala en Acceso al clúster'],
    ['ca', 'console_busy', 'massa consoles'],
  ])('%s %s', async (locale, code, text) => {
    setLocale(locale);
    fx.ticketError = Object.assign(new Error('RAWERR dial tcp 10.0.0.9:22'), { code, status: 502 });
    app = mount(TerminalPanel, { target, props: { mode: 'vm', vmId: 'v1', node: 'kvm-b' } });
    await settle();
    const out = fx.writes.join('');
    expect(out).toContain(text);
    expect(out).not.toContain('RAWERR');
    expect(out).not.toContain('Error obtaining ticket');
  });
});
