import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

const fx = vi.hoisted(() => ({ calls: 0, resolvers: [], fail: null }));
vi.mock('./auth.svelte.js', () => ({
  api: {
    listHAWorkloads: () => {
      fx.calls++;
      if (fx.fail) return Promise.reject(fx.fail);
      return new Promise((r) =>
        fx.resolvers.push(() => r({ workloads: [{ key: 'kvm:a' }], summary: { managed: 1 } }))
      );
    },
  },
}));

import {
  haWorkloads,
  refreshHaWorkloads,
  watchHaWorkloads,
  resetHaWorkloads,
  POLL_MS,
} from './haWorkloads.svelte.js';
import { notifySessionCleared } from '../sessionHooks.js';

const flush = async () => {
  for (let i = 0; i < 5; i++) await Promise.resolve();
};
const answer = async () => {
  fx.resolvers.splice(0).forEach((r) => r());
  await flush();
};

beforeEach(() => {
  vi.useFakeTimers();
  fx.calls = 0;
  fx.resolvers = [];
  fx.fail = null;
  resetHaWorkloads();
});
afterEach(() => {
  resetHaWorkloads();
  vi.useRealTimers();
});

describe('haWorkloads store', () => {
  it('shares one in-flight request', async () => {
    const a = refreshHaWorkloads();
    const b = refreshHaWorkloads();
    expect(a).toBe(b);
    expect(fx.calls).toBe(1);
    await answer();
    await a;
    expect(haWorkloads.loaded).toBe(true);
    expect(haWorkloads.workloads).toHaveLength(1);
  });

  it('keeps the last good list and exposes the error', async () => {
    const p = refreshHaWorkloads();
    await answer();
    await p;
    fx.fail = Object.assign(new Error('boom'), { code: 'pcs_failed' });
    await refreshHaWorkloads();
    expect(haWorkloads.error).toBe('boom');
    expect(haWorkloads.errorCode).toBe('pcs_failed');
    expect(haWorkloads.workloads).toHaveLength(1);
  });

  it('polls every 10s with no overlap and stops on unmount', async () => {
    const stop = watchHaWorkloads();
    expect(fx.calls).toBe(1);
    // slow response: timer must not start until it settles
    await vi.advanceTimersByTimeAsync(POLL_MS * 3);
    expect(fx.calls).toBe(1);
    await answer();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(fx.calls).toBe(2);
    await answer();
    stop();
    await vi.advanceTimersByTimeAsync(POLL_MS * 3);
    expect(fx.calls).toBe(2);
  });

  it('two watchers share one chain', async () => {
    const s1 = watchHaWorkloads();
    const s2 = watchHaWorkloads();
    expect(fx.calls).toBe(1);
    await answer();
    s1();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(fx.calls).toBe(2);
    await answer();
    s2();
    await vi.advanceTimersByTimeAsync(POLL_MS * 2);
    expect(fx.calls).toBe(2);
  });

  it('resets on logout and ignores a late response', async () => {
    const p = refreshHaWorkloads();
    notifySessionCleared();
    await answer();
    await p;
    expect(haWorkloads.loaded).toBe(false);
    expect(haWorkloads.workloads).toEqual([]);
  });
});
