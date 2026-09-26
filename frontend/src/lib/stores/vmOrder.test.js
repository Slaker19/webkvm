import { describe, it, expect, beforeEach } from 'vitest';
import {
  vmOrder,
  setSortMode,
  togglePin,
  isPinned,
  moveInCustomOrder,
  applySort,
} from './vmOrder.svelte.js';

function resetOrder() {
  vmOrder.sortMode = 'custom';
  vmOrder.customOrder = [];
  vmOrder.pinned = [];
}

describe('vmOrder store', () => {
  beforeEach(resetOrder);

  it('defaults to custom sort mode with no ordering', () => {
    expect(vmOrder.sortMode).toBe('custom');
    expect(vmOrder.customOrder).toEqual([]);
    expect(vmOrder.pinned).toEqual([]);
  });

  it('setSortMode updates the mode', () => {
    setSortMode('alpha');
    expect(vmOrder.sortMode).toBe('alpha');
  });

  it('togglePin adds and removes a VM from the pinned list', () => {
    expect(isPinned('vm1')).toBe(false);
    togglePin('vm1');
    expect(isPinned('vm1')).toBe(true);
    expect(vmOrder.pinned).toEqual(['vm1']);
    togglePin('vm1');
    expect(isPinned('vm1')).toBe(false);
    expect(vmOrder.pinned).toEqual([]);
  });

  it('applySort with alpha mode sorts by alias/name ascending', () => {
    const vms = [
      { id: 'b', name: 'Bravo' },
      { id: 'a', name: 'Alpha' },
      { id: 'c', name: 'Charlie' },
    ];
    setSortMode('alpha');
    const sorted = applySort(vms);
    expect(sorted.map((v) => v.id)).toEqual(['a', 'b', 'c']);
  });

  it('applySort with alpha-desc mode sorts descending', () => {
    const vms = [
      { id: 'b', name: 'Bravo' },
      { id: 'a', name: 'Alpha' },
      { id: 'c', name: 'Charlie' },
    ];
    setSortMode('alpha-desc');
    const sorted = applySort(vms);
    expect(sorted.map((v) => v.id)).toEqual(['c', 'b', 'a']);
  });

  it('applySort with state mode ranks running > paused > shutoff > crashed', () => {
    const vms = [
      { id: 'a', state: 'shutoff' },
      { id: 'b', state: 'running' },
      { id: 'c', state: 'paused' },
      { id: 'd', state: 'crashed' },
    ];
    setSortMode('state');
    const sorted = applySort(vms);
    expect(sorted.map((v) => v.id)).toEqual(['b', 'c', 'a', 'd']);
  });

  it('applySort with cpu mode sorts by highest CPU usage first', () => {
    const vms = [{ id: 'a' }, { id: 'b' }, { id: 'c' }];
    setSortMode('cpu');
    const metrics = { a: { cpu: 10 }, b: { cpu: 90 }, c: { cpu: 50 } };
    const sorted = applySort(vms, metrics);
    expect(sorted.map((v) => v.id)).toEqual(['b', 'c', 'a']);
  });

  it('applySort keeps pinned VMs first regardless of sort mode', () => {
    const vms = [
      { id: 'a', name: 'Alpha' },
      { id: 'b', name: 'Bravo' },
      { id: 'c', name: 'Charlie' },
    ];
    togglePin('c');
    setSortMode('alpha');
    const sorted = applySort(vms);
    expect(sorted.map((v) => v.id)).toEqual(['c', 'a', 'b']);
  });

  it('applySort with custom mode and empty customOrder preserves input order', () => {
    const vms = [{ id: 'z' }, { id: 'a' }, { id: 'm' }];
    setSortMode('custom');
    const sorted = applySort(vms);
    expect(sorted.map((v) => v.id)).toEqual(['z', 'a', 'm']);
  });

  it('applySort with custom mode respects customOrder', () => {
    const vms = [{ id: 'a' }, { id: 'b' }, { id: 'c' }];
    vmOrder.customOrder = ['c', 'a', 'b'];
    setSortMode('custom');
    const sorted = applySort(vms);
    expect(sorted.map((v) => v.id)).toEqual(['c', 'a', 'b']);
  });

  it('applySort with custom mode appends new (unordered) VMs at the end', () => {
    const vms = [{ id: 'a' }, { id: 'b' }, { id: 'new' }];
    vmOrder.customOrder = ['b', 'a'];
    setSortMode('custom');
    const sorted = applySort(vms);
    expect(sorted.map((v) => v.id)).toEqual(['b', 'a', 'new']);
  });

  it('moveInCustomOrder inserts the dragged id before the target', () => {
    const allIds = ['a', 'b', 'c'];
    moveInCustomOrder(allIds, 'c', 'a', false);
    expect(vmOrder.customOrder).toEqual(['c', 'a', 'b']);
    expect(vmOrder.sortMode).toBe('custom');
  });

  it('moveInCustomOrder inserts the dragged id after the target', () => {
    const allIds = ['a', 'b', 'c'];
    moveInCustomOrder(allIds, 'a', 'c', true);
    expect(vmOrder.customOrder).toEqual(['b', 'c', 'a']);
  });

  it('moveInCustomOrder drops stale IDs no longer present in allIds', () => {
    vmOrder.customOrder = ['x', 'a', 'b', 'c'];
    const allIds = ['a', 'b', 'c'];
    moveInCustomOrder(allIds, 'b', 'a', false);
    expect(vmOrder.customOrder).toEqual(['b', 'a', 'c']);
  });
});
