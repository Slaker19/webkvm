import { describe, it, expect } from 'vitest';
import {
  webConsoleURL,
  usagePercent,
  healingSecondsValid,
  codeText,
  pingAllOutcome,
  isPrivateHost,
  uriHost,
  suggestUIUrl,
  validateUIUrl,
  nodeConsoleURL,
  nodeAddressInfo,
} from './nodes.js';

describe('webConsoleURL', () => {
  it('derives the host from qemu+ssh URIs with or without a user', () => {
    expect(webConsoleURL('qemu+ssh://root@node1.example/system')).toBe(
      'https://node1.example:8080/'
    );
    expect(webConsoleURL('qemu+ssh://node1.example/system')).toBe('https://node1.example:8080/');
    expect(webConsoleURL('qemu+ssh://root@192.0.2.10:2222/system')).toBe(
      'https://192.0.2.10:8080/'
    );
  });
  it('handles IPv6 literals', () => {
    expect(webConsoleURL('qemu+ssh://u@[::1]/system')).toBe('https://[::1]:8080/');
    expect(webConsoleURL('https://[2001:db8::1]:8443')).toBe('https://[2001:db8::1]:8443/');
  });
  it('keeps http(s) URIs with their own port', () => {
    expect(webConsoleURL('http://10.0.0.5:9000/x')).toBe('http://10.0.0.5:9000/');
  });
  it('returns null when it cannot be derived', () => {
    expect(webConsoleURL('')).toBeNull();
    expect(webConsoleURL('not a uri')).toBeNull();
    expect(webConsoleURL('qemu:///system')).toBeNull();
    expect(webConsoleURL(null)).toBeNull();
  });
});

describe('usagePercent', () => {
  it('is null without a total, 0 for an empty resource, clamped otherwise', () => {
    expect(usagePercent(0, 0)).toBeNull();
    expect(usagePercent(100, 100)).toBe(0);
    expect(usagePercent(100, 25)).toBe(75);
    expect(usagePercent(100, -5)).toBe(100);
  });
});

describe('healingSecondsValid', () => {
  it('accepts 0 and 60..86400 only', () => {
    expect(healingSecondsValid(0)).toBe(true);
    expect(healingSecondsValid('300')).toBe(true);
    expect(healingSecondsValid(59)).toBe(false);
    expect(healingSecondsValid(86401)).toBe(false);
    expect(healingSecondsValid(1.5)).toBe(false);
    expect(healingSecondsValid('')).toBe(false);
  });
});

describe('codeText', () => {
  const t = (k, v) => (k === 'p.known' ? 'Known' : k === 'fb' ? `Unknown ${v?.code}` : k);
  it('falls back when the key is missing', () => {
    expect(codeText(t, 'p', 'known', 'fb')).toBe('Known');
    expect(codeText(t, 'p', 'zzz', 'fb')).toBe('Unknown zzz');
  });
});

describe('pingAllOutcome', () => {
  it('classifies results', () => {
    expect(pingAllOutcome([]).kind).toBe('none');
    expect(pingAllOutcome([{ status: 'online' }]).kind).toBe('ok');
    expect(pingAllOutcome([{ status: 'online' }, { status: 'offline' }])).toEqual({
      kind: 'partial',
      online: 1,
      total: 2,
    });
  });
});

import { migrationTargets } from './nodes.js';
describe('migrationTargets', () => {
  const nodes = [
    { name: 'host', type: 'local', backend: 'libvirt', enabled: true },
    { name: 'b', type: 'remote', backend: 'libvirt', enabled: true },
    { name: 'c', type: 'remote', enabled: true },
    { name: 'off', type: 'remote', backend: 'libvirt', enabled: false },
    { name: 'inc', type: 'remote', backend: 'incus', enabled: true },
  ];
  it('local VM: only enabled libvirt remotes, never the local host', () => {
    expect(migrationTargets(nodes, { node: '' }, 'host').map((n) => n.name)).toEqual(['b', 'c']);
  });
  it('remote VM: excludes its node, includes the local host', () => {
    expect(migrationTargets(nodes, { node: 'b' }, 'host').map((n) => n.name)).toEqual([
      'host',
      'c',
    ]);
  });
});

describe('isPrivateHost', () => {
  it('flags RFC1918, ULA, link-local and loopback addresses', () => {
    for (const h of [
      '10.0.0.1',
      '10.255.255.255',
      '172.16.0.1',
      '172.31.255.255',
      '192.168.50.40',
      '169.254.10.10',
      '127.0.0.1',
      'fc00::1',
      'fd12:3456::1',
      '[fd12:3456::1]',
      'fe80::1',
      '::1',
    ]) {
      expect(isPrivateHost(h), h).toBe(true);
    }
  });
  it('does not flag public addresses, boundaries or names', () => {
    for (const h of [
      '172.15.0.1',
      '172.32.0.1',
      '11.0.0.1',
      '192.169.1.1',
      '8.8.8.8',
      '2001:db8::1',
      'fe00::1',
      'node1.example',
      'host.lan',
      '',
    ]) {
      expect(isPrivateHost(h), h).toBe(false);
    }
  });
});

describe('uriHost', () => {
  it('extracts the host of qemu+ssh and http URIs, with IPv6 brackets stripped', () => {
    expect(uriHost('qemu+ssh://u@10.20.30.10/system')).toBe('10.20.30.10');
    expect(uriHost('qemu+ssh://node1.example:2222/system')).toBe('node1.example');
    expect(uriHost('qemu+ssh://u@[fd00::5]/system')).toBe('fd00::5');
    expect(uriHost('qemu:///system')).toBe('');
    expect(uriHost('nonsense')).toBe('');
  });
});

describe('suggestUIUrl', () => {
  it('suggests https://<host>:8080 only for non-private hosts', () => {
    expect(suggestUIUrl('qemu+ssh://u@node1.example/system')).toBe('https://node1.example:8080');
    expect(suggestUIUrl('qemu+ssh://u@203.0.113.9/system')).toBe('https://203.0.113.9:8080');
    expect(suggestUIUrl('qemu+ssh://u@[2001:db8::1]/system')).toBe('https://[2001:db8::1]:8080');
  });
  it('returns null for private ranges and undetectable hosts', () => {
    for (const u of [
      'qemu+ssh://u@10.20.30.10/system',
      'qemu+ssh://u@172.20.1.1/system',
      'qemu+ssh://u@192.168.1.5/system',
      'qemu+ssh://u@[fd00::5]/system',
      'qemu+ssh://u@169.254.1.1/system',
      'qemu:///system',
      '',
    ]) {
      expect(suggestUIUrl(u), u).toBeNull();
    }
  });
});

describe('validateUIUrl', () => {
  it('accepts empty and normalizes valid addresses', () => {
    expect(validateUIUrl('')).toEqual({ ok: true, value: '' });
    expect(validateUIUrl('  ')).toEqual({ ok: true, value: '' });
    expect(validateUIUrl('https://192.168.50.31:8080')).toEqual({
      ok: true,
      value: 'https://192.168.50.31:8080',
    });
    expect(validateUIUrl('HTTP://Host.LAN/')).toEqual({ ok: true, value: 'http://host.lan' });
    expect(validateUIUrl('https://[2001:db8::1]:8080').ok).toBe(true);
  });
  it('rejects what the backend rejects', () => {
    for (const bad of [
      'ftp://h',
      'javascript:alert(1)',
      'host.lan',
      'https://',
      'https://u@h',
      'https://u:p@h',
      'https://h?x=1',
      'https://h#f',
      'https://h/path',
      'https://localhost',
      'https://127.0.0.1',
      'https://[::1]',
      'http://0.0.0.0',
      'https://169.254.1.1',
      'https://[fe80::1]',
      'http://ho st',
      'https://h:99999',
      'https://h:0',
      'https://' + 'a'.repeat(250) + '.lan',
    ]) {
      const r = validateUIUrl(bad);
      expect(r.ok, bad).toBe(false);
      expect(r.error, bad).toBeTruthy();
    }
  });
  it('accepts private LAN addresses (they are valid on the operator network)', () => {
    expect(validateUIUrl('https://192.168.50.40:8080').ok).toBe(true);
  });
});

describe('nodeConsoleURL', () => {
  const priv = 'qemu+ssh://u@10.20.30.10/system';
  it('prefers ui_url over the URI host', () => {
    expect(nodeConsoleURL({ uri: priv, ui_url: 'https://192.168.50.31:8080' })).toBe(
      'https://192.168.50.31:8080/'
    );
    expect(nodeConsoleURL({ uri: 'qemu+ssh://u@pub.example/system', ui_url: 'http://x.lan' })).toBe(
      'http://x.lan/'
    );
  });
  it('falls back to the URI host only when it is not private', () => {
    expect(nodeConsoleURL({ uri: 'qemu+ssh://u@pub.example/system' })).toBe(
      'https://pub.example:8080/'
    );
    expect(nodeConsoleURL({ uri: 'qemu+ssh://u@[2001:db8::1]/system' })).toBe(
      'https://[2001:db8::1]:8080/'
    );
  });
  it('is null for private hosts without ui_url or for bad data', () => {
    expect(nodeConsoleURL({ uri: priv })).toBeNull();
    expect(nodeConsoleURL({ uri: priv, ui_url: '' })).toBeNull();
    expect(nodeConsoleURL({ uri: priv, ui_url: 'javascript:alert(1)' })).toBeNull();
    expect(nodeConsoleURL(null)).toBeNull();
  });
});

describe('webConsoleURL with ui_url', () => {
  it('prefers a valid ui_url and keeps the old derivation otherwise', () => {
    expect(webConsoleURL('qemu+ssh://u@10.20.30.10/system', 'https://192.168.50.40:8080')).toBe(
      'https://192.168.50.40:8080/'
    );
    expect(webConsoleURL('qemu+ssh://u@[::1]/system', '')).toBe('https://[::1]:8080/');
  });
});

describe('nodeAddressInfo', () => {
  it('reports both addresses and the private-network state', () => {
    const a = nodeAddressInfo({ uri: 'qemu+ssh://u@10.20.30.10/system' });
    expect(a).toMatchObject({
      cluster: '10.20.30.10',
      uiUrl: '',
      privateBadge: true,
      needsHint: true,
    });
    const b = nodeAddressInfo({
      uri: 'qemu+ssh://u@10.20.30.10/system',
      ui_url: 'https://192.168.50.40:8080',
    });
    expect(b).toMatchObject({
      uiUrl: 'https://192.168.50.40:8080',
      privateBadge: false,
      needsHint: false,
    });
    const c = nodeAddressInfo({ uri: 'qemu+ssh://u@pub.example/system' });
    expect(c).toMatchObject({ privateBadge: false, needsHint: false });
  });
});

describe('isSshPortOnly', () => {
  it('flags TCP-only probes (new and legacy value) of remote libvirt nodes only', async () => {
    const { isSshPortOnly } = await import('./nodes.js');
    expect(isSshPortOnly({ libvirt_status: 'tcp_reachable', backend: 'libvirt' })).toBe(true);
    expect(isSshPortOnly({ libvirt_status: 'reachable' })).toBe(true);
    expect(isSshPortOnly({ libvirt_status: 'ok' })).toBe(false);
    expect(isSshPortOnly({ libvirt_status: 'tcp_reachable', backend: 'incus' })).toBe(false);
    expect(isSshPortOnly({ libvirt_status: 'tcp_reachable', type: 'local' })).toBe(false);
    expect(isSshPortOnly(null)).toBe(false);
  });
});

describe('self-registration helpers', () => {
  const local = { id: 'local', name: 'h1', type: 'local', status: 'online' };
  const dup = { id: 'n1', name: 'h1', type: 'remote', is_self: true, status: 'online' };
  const other = { id: 'n2', name: 'h2', type: 'remote', status: 'online', access: 'ok' };
  it('isSelfNode / selfDuplicates / withoutSelfDuplicates', async () => {
    const { isSelfNode, selfDuplicates, withoutSelfDuplicates } = await import('./nodes.js');
    expect(isSelfNode(dup)).toBe(true);
    expect(isSelfNode(local)).toBe(false);
    expect(isSelfNode(null)).toBe(false);
    expect(selfDuplicates([local, dup, other])).toEqual([dup]);
    expect(withoutSelfDuplicates([local, dup, other])).toEqual([local, other]);
    expect(withoutSelfDuplicates(null)).toEqual([]);
  });
  it('nodeHealth separates "reachable, access not set up" from online', async () => {
    const { nodeHealth } = await import('./nodes.js');
    expect(nodeHealth(local)).toBe('online');
    expect(nodeHealth(other)).toBe('online');
    expect(nodeHealth({ ...other, access: 'not_set_up' })).toBe('no_access');
    expect(nodeHealth({ ...other, status: 'offline', access: 'not_set_up' })).toBe('offline');
    expect(nodeHealth({ ...other, status: 'degraded' })).toBe('degraded');
    expect(nodeHealth({ ...other, enabled: false })).toBe('disabled');
    expect(nodeHealth({ status: '' })).toBe('unknown');
  });
});
