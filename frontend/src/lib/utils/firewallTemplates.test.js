import { describe, it, expect } from 'vitest';
import {
  buildTemplate,
  moveRule,
  newInputRule,
  newForwardRule,
  FIREWALL_TEMPLATES,
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

  it('new rules get unique ids', () => {
    const a = newInputRule();
    const b = newInputRule();
    expect(a.id).not.toBe(b.id);
    const f1 = newForwardRule();
    const f2 = newForwardRule();
    expect(f1.id).not.toBe(f2.id);
  });
});
