import { describe, it, expect } from 'vitest';
import {
  buildTemplate,
  buildVMTemplate,
  moveRule,
  newInputRule,
  newForwardRule,
  duplicateItem,
  mergeFirewall,
  findConflicts,
  FIREWALL_TEMPLATES,
  VM_TEMPLATE_PRESETS,
  PROTECTED_RAIL_PORTS,
} from './firewallTemplates.js';

describe('firewallTemplates', () => {
  it('web-server template builds HTTP/HTTPS input + forwards', () => {
    const tpl = buildTemplate('web-server');
    expect(tpl.input.map((r) => r.port)).toEqual([80, 443]);
    expect(tpl.forwards.map((f) => [f.host_port, f.guest_port])).toEqual([
      [80, 80],
      [443, 443],
    ]);
    expect(tpl.forwards.every((f) => f.guest_ip === '10.0.0.10')).toBe(true);
  });

  it('ssh-gateway template publishes host 2222 → guest 22', () => {
    const tpl = buildTemplate('ssh-gateway');
    expect(tpl.input).toHaveLength(0);
    expect(tpl.forwards).toHaveLength(1);
    expect(tpl.forwards[0].host_port).toBe(2222);
    expect(tpl.forwards[0].guest_port).toBe(22);
  });

  it('unknown template returns empty rules', () => {
    expect(buildTemplate('nope')).toEqual({ input: [], forwards: [] });
  });

  it('every template has an id, labelKey and descKey', () => {
    for (const t of FIREWALL_TEMPLATES) {
      expect(typeof t.id).toBe('string');
      expect(t.id.length).toBeGreaterThan(0);
      expect(typeof t.labelKey).toBe('string');
      expect(typeof t.descKey).toBe('string');
    }
  });

  it('moveRule moves an item up and down without mutating the input', () => {
    const list = [{ id: 'a' }, { id: 'b' }, { id: 'c' }];
    const up = moveRule(list, 2, -1);
    expect(up.map((x) => x.id)).toEqual(['a', 'c', 'b']);
    const down = moveRule(list, 0, 1);
    expect(down.map((x) => x.id)).toEqual(['b', 'a', 'c']);
    // out-of-range moves are no-ops
    expect(moveRule(list, 0, -1)).toBe(list);
    expect(moveRule(list, 2, 1)).toBe(list);
    // original untouched
    expect(list.map((x) => x.id)).toEqual(['a', 'b', 'c']);
  });

  it('no host forward steals a protected rail port (VNC footgun)', () => {
    for (const t of FIREWALL_TEMPLATES) {
      for (const f of t.build().forwards) {
        expect(PROTECTED_RAIL_PORTS).not.toContain(f.host_port);
      }
    }
  });

  it('merge appends only non-duplicates and counts them', () => {
    const current = {
      input: [{ id: 'a', proto: 'tcp', port: 80, src: '', action: 'allow' }],
      forwards: [],
    };
    const incoming = buildTemplate('web-server');
    const merged = mergeFirewall(current, incoming);
    // port 80/tcp/allow already there; 443 is new
    expect(merged.input.map((r) => r.port)).toEqual([80, 443]);
    expect(merged.addedInput).toBe(1);
    expect(merged.addedForwards).toBe(2);
    // merging twice is a no-op the second time
    const again = mergeFirewall(merged, incoming);
    expect(again.addedInput).toBe(0);
    expect(again.addedForwards).toBe(0);
    // inputs untouched
    expect(current.input).toHaveLength(1);
  });

  it('findConflicts flags duplicates, shadows, and ignores drafts/disabled', () => {
    const input = [
      { id: 'a', proto: 'tcp', port: 80, src: '', action: 'allow' },
      { id: 'b', proto: 'tcp', port: 80, src: '', action: 'allow' }, // duplicate
      { id: 'c', proto: 'tcp', port: 80, src: '', action: 'drop' }, // shadowed by a
      { id: 'd', proto: 'udp', port: 80, src: '', action: 'drop' }, // different proto: fine
      { id: 'e', proto: 'tcp', port: 0, src: '', action: 'allow' }, // draft: ignored
      { id: 'f', proto: 'tcp', port: 81, src: '', action: 'drop', disabled: true }, // ignored
      { id: 'g', proto: 'tcp', port: 81, src: '', action: 'allow' }, // disabled above: no shadow
    ];
    const found = findConflicts(input, []);
    expect(found).toContainEqual(
      expect.objectContaining({ kind: 'duplicate', scope: 'input', index: 1 })
    );
    expect(found).toContainEqual(
      expect.objectContaining({ kind: 'shadowed', scope: 'input', index: 2, byIndex: 0 })
    );
    expect(found.filter((c) => c.index === 3)).toHaveLength(0);
    expect(found.filter((c) => c.index === 4 || c.index === 6)).toHaveLength(0);
    // proto 'both' overlaps tcp
    const both = findConflicts(
      [
        { id: 'a', proto: 'both', port: 53, src: '', action: 'allow' },
        { id: 'b', proto: 'tcp', port: 53, src: '', action: 'drop' },
      ],
      []
    );
    expect(both).toContainEqual(expect.objectContaining({ kind: 'shadowed', index: 1 }));
    // duplicate forwards flagged
    const fwd = findConflicts(
      [],
      [
        { id: 'a', proto: 'tcp', host_port: 8080, guest_ip: '10.0.0.2', guest_port: 80 },
        { id: 'b', proto: 'tcp', host_port: 8080, guest_ip: '10.0.0.2', guest_port: 80 },
      ]
    );
    expect(fwd).toContainEqual(expect.objectContaining({ kind: 'duplicate', scope: 'forward' }));
  });

  it('duplicateItem clones with a fresh id', () => {
    const src = { id: 'x', proto: 'tcp', port: 80, action: 'allow' };
    const dup = duplicateItem(src);
    expect(dup.id).not.toBe('x');
    expect({ ...dup, id: 'x' }).toEqual(src);
  });

  it('VM presets build inbound rules with ids, label/desc keys', () => {
    for (const t of VM_TEMPLATE_PRESETS) {
      expect(typeof t.id).toBe('string');
      expect(typeof t.labelKey).toBe('string');
      expect(typeof t.descKey).toBe('string');
      const built = t.build();
      expect(built.rules.length).toBeGreaterThan(0);
      expect(built.forwards).toEqual([]);
      for (const r of built.rules) {
        expect(r.action).toBe('allow');
        expect(r.port).toBeGreaterThan(0);
      }
    }
    expect(buildVMTemplate('vm-web').rules.map((r) => r.port)).toEqual([80, 443]);
    expect(buildVMTemplate('nope')).toEqual({ rules: [], forwards: [] });
  });

  it('new rules get unique ids', () => {
    const a = newInputRule();
    const b = newInputRule();
    expect(a.id).not.toBe(b.id);
    const f1 = newForwardRule();
    const f2 = newForwardRule();
    expect(f1.id).not.toBe(f2.id);
  });
});
