import { describe, it, expect, beforeEach } from 'vitest';
import {
  sidebarOrder,
  setSidebarMode,
  toggleSidebarHidden,
  isSidebarHidden,
  resetSidebarOrder,
  moveSidebarItem,
  applySidebarOrder,
} from './sidebarOrder.svelte.js';

beforeEach(() => {
  resetSidebarOrder();
});

describe('sidebarOrder store', () => {
  it('defaults to default mode with no hidden items', () => {
    expect(sidebarOrder.mode).toBe('default');
    expect(sidebarOrder.order).toEqual([]);
    expect(sidebarOrder.hidden).toEqual([]);
  });

  it('setSidebarMode updates the mode', () => {
    setSidebarMode('alpha');
    expect(sidebarOrder.mode).toBe('alpha');
  });

  it('toggleSidebarHidden hides and unhides an item', () => {
    expect(isSidebarHidden('media')).toBe(false);
    toggleSidebarHidden('media');
    expect(isSidebarHidden('media')).toBe(true);
    toggleSidebarHidden('media');
    expect(isSidebarHidden('media')).toBe(false);
  });

  it('applySidebarOrder filters out hidden items', () => {
    const items = [
      { id: 'vms', label: 'VMs' },
      { id: 'media', label: 'Media' },
    ];
    toggleSidebarHidden('media');
    const result = applySidebarOrder(items);
    expect(result.map((i) => i.id)).toEqual(['vms']);
  });

  it('applySidebarOrder with alpha mode sorts by label', () => {
    const items = [
      { id: 'storage', label: 'Storage' },
      { id: 'apps', label: 'Apps' },
      { id: 'media', label: 'Media' },
    ];
    setSidebarMode('alpha');
    const result = applySidebarOrder(items);
    expect(result.map((i) => i.id)).toEqual(['apps', 'media', 'storage']);
  });

  it('applySidebarOrder with default mode preserves input order', () => {
    const items = [
      { id: 'z', label: 'Z' },
      { id: 'a', label: 'A' },
    ];
    const result = applySidebarOrder(items);
    expect(result.map((i) => i.id)).toEqual(['z', 'a']);
  });

  it('moveSidebarItem reorders and switches to custom mode', () => {
    const allIds = ['vms', 'apps', 'storage'];
    moveSidebarItem(allIds, 'storage', 'vms', false);
    expect(sidebarOrder.order).toEqual(['storage', 'vms', 'apps']);
    expect(sidebarOrder.mode).toBe('custom');
  });

  // The keyboard reordering in Sidebar.svelte (Alt+Arrow) relies on
  // these two directions behaving as single-step moves. If "after" were
  // wrong for one of them, the item would appear stuck: pressing Down
  // would drop it right back into the same slot.
  it('moveSidebarItem moves one step down when after=true', () => {
    const allIds = ['vms', 'apps', 'storage'];
    sidebarOrder.order = [];
    moveSidebarItem(allIds, 'vms', 'apps', true);
    expect(sidebarOrder.order).toEqual(['apps', 'vms', 'storage']);
  });

  it('moveSidebarItem moves one step up when after=false', () => {
    const allIds = ['vms', 'apps', 'storage'];
    sidebarOrder.order = [];
    moveSidebarItem(allIds, 'storage', 'apps', false);
    expect(sidebarOrder.order).toEqual(['vms', 'storage', 'apps']);
  });

  it('applySidebarOrder with custom mode respects the saved order', () => {
    const items = [
      { id: 'vms', label: 'VMs' },
      { id: 'apps', label: 'Apps' },
      { id: 'storage', label: 'Storage' },
    ];
    sidebarOrder.order = ['storage', 'apps', 'vms'];
    setSidebarMode('custom');
    const result = applySidebarOrder(items);
    expect(result.map((i) => i.id)).toEqual(['storage', 'apps', 'vms']);
  });

  it('resetSidebarOrder clears mode, order and hidden', () => {
    setSidebarMode('alpha');
    toggleSidebarHidden('media');
    resetSidebarOrder();
    expect(sidebarOrder.mode).toBe('default');
    expect(sidebarOrder.order).toEqual([]);
    expect(sidebarOrder.hidden).toEqual([]);
  });
});
