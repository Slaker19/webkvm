import { describe, it, expect } from 'vitest';
import {
  isIP,
  validateNodes,
  validateClusterName,
  buildPlanBody,
  buildSetupBody,
  rowsFromRegistered,
  manualStepCommands,
  validateTimeout,
} from './clusterWizard.js';

const n = (name, address) => ({ name, address });

describe('clusterWizard validation', () => {
  it('accepts IPv4/IPv6 and rejects junk', () => {
    expect(isIP('10.0.0.1')).toBe(true);
    expect(isIP('fd00::1')).toBe(true);
    expect(isIP('300.1.1.1')).toBe(false);
    expect(isIP('host.example')).toBe(false);
    expect(isIP('')).toBe(false);
  });
  it('validates cluster names', () => {
    expect(validateClusterName('prod-1')).toBeNull();
    expect(validateClusterName('bad name')).toBe('name_invalid');
    expect(validateClusterName('-x')).toBe('name_invalid');
  });
  it('flags duplicates and bad charset per row', () => {
    const r = validateNodes([n('a', '10.0.0.1'), n('a', '10.0.0.1'), n('b_c', 'x')]);
    expect(r.rows[1]).toEqual({ name: 'name_duplicate', address: 'address_duplicate' });
    expect(r.rows[2]).toEqual({ name: 'name_invalid', address: 'address_invalid' });
    expect(r.valid).toBe(false);
  });
  it('gates two nodes on allowTwo and <2 always', () => {
    const two = [n('a', '10.0.0.1'), n('b', '10.0.0.2')];
    expect(validateNodes(two).valid).toBe(false);
    expect(validateNodes(two).count).toBe('two_nodes');
    expect(validateNodes(two, { allowTwo: true }).valid).toBe(true);
    expect(validateNodes([n('a', '10.0.0.1')], { allowTwo: true }).count).toBe('too_few');
    expect(validateNodes([...two, n('c', '10.0.0.3')]).valid).toBe(true);
  });
  it('validates the sbd timeout', () => {
    expect(validateTimeout('')).toBeNull();
    expect(validateTimeout('30')).toBeNull();
    expect(validateTimeout('2')).toBe('timeout_invalid');
    expect(validateTimeout('1.5')).toBe('timeout_invalid');
  });
});

describe('clusterWizard bodies', () => {
  it('omits empty optional fields', () => {
    const b = buildPlanBody({ clusterName: ' c1 ', nodes: [n(' a ', '10.0.0.1')] });
    expect(b).toEqual({ cluster_name: 'c1', nodes: [{ name: 'a', address: '10.0.0.1' }], sbd: {} });
  });
  it('includes advanced options and setup flags, never a password', () => {
    const b = buildPlanBody({
      clusterName: 'c1',
      nodes: [n('a', '10.0.0.1')],
      watchdogDevice: '/dev/watchdog1',
      timeoutSeconds: '20',
      fencing: ['pve'],
      allowTwo: true,
    });
    expect(b.sbd).toEqual({ watchdog_device: '/dev/watchdog1', timeout_seconds: 20 });
    expect(b.fencing).toEqual(['pve']);
    expect(b.allow_two_nodes).toBe(true);
    const s = buildSetupBody(b, { sbdEnable: true, enableOnBoot: false });
    expect(s.confirm).toBe(true);
    expect(s.enable_on_boot).toBe(false);
    expect(JSON.stringify(s)).not.toMatch(/password/i);
  });
  it('prefills from registered nodes', () => {
    const rows = rowsFromRegistered([
      { name: 'Node 1', uri: 'qemu+ssh://root@10.0.0.5/system' },
      { name: 'off', uri: 'qemu+ssh://root@10.0.0.6/system', enabled: false },
      { name: 'dns', uri: 'qemu+ssh://root@host.example/system' },
    ]);
    expect(rows).toEqual([{ name: 'node-1', address: '10.0.0.5' }]);
  });
  it('builds manual step commands without the password', () => {
    const c = manualStepCommands([n('a', '1.1.1.1'), n('b', '1.1.1.2')]);
    expect(c.hostAuth).toBe('sudo pcs host auth a b -u hacluster');
    expect(c.passwd).toBe('sudo passwd hacluster');
  });
});

describe('prefillRows / existingCluster', () => {
  const local = { id: 'local', type: 'local', name: 'node-b', uri: 'qemu:///system' };
  const dup = {
    id: 'n1',
    type: 'remote',
    name: 'node-b',
    is_self: true,
    uri: 'qemu+ssh://operator@10.20.30.11/system',
  };
  const ten = {
    id: 'n2',
    type: 'remote',
    name: 'node-a',
    uri: 'qemu+ssh://u@10.20.30.10/system',
  };

  it('names the local node, takes its address from a self entry and never twice', async () => {
    const { prefillRows } = await import('./clusterWizard.js');
    const rows = prefillRows({ nodes: [local, dup, ten] });
    expect(rows).toEqual([
      { name: 'node-b', address: '10.20.30.11' },
      { name: 'node-a', address: '10.20.30.10' },
    ]);
  });

  it('never uses the browser host (it may be the VIP) and leaves the local address empty', async () => {
    const { prefillRows } = await import('./clusterWizard.js');
    expect(prefillRows({ nodes: [local], browserHost: '192.168.50.31' })).toEqual([
      { name: 'node-b', address: '' },
    ]);
  });

  it('adds every Pacemaker member that is not a registered node', async () => {
    const { prefillRows } = await import('./clusterWizard.js');
    const rows = prefillRows({
      nodes: [local, dup, ten],
      pacemakerNames: ['node-a', 'node-b', 'node-c'],
    });
    expect(rows.map((r) => r.name)).toEqual(['node-b', 'node-a', 'node-c']);
    expect(rows[2]).toEqual({ name: 'node-c', address: '' });
  });

  it('existingCluster reports the running membership', async () => {
    const { existingCluster } = await import('./clusterWizard.js');
    expect(existingCluster(null)).toBeNull();
    expect(existingCluster({ pacemaker: { state: 'not_configured', node_names: [] } })).toBeNull();
    expect(
      existingCluster({
        pacemaker: { state: 'observed', cluster_name: 'lab', node_names: ['a', 'b', 'c'] },
      })
    ).toEqual({ name: 'lab', nodes: ['a', 'b', 'c'] });
    expect(existingCluster({ pacemaker: { node_states: [{ name: 'x' }, { name: 'y' }] } })).toEqual(
      { name: '', nodes: ['x', 'y'] }
    );
  });
});
