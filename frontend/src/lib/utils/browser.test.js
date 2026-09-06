import { describe, it, expect } from 'vitest';
import { browser } from './browser.js';

// V12-FE-04: utils/ tests run in the node environment (no jsdom), so
// `browser` must resolve to false here. This pins the contract: utils are
// pure and environment-agnostic, and the test setup stays DOM-free.
describe('browser (node env)', () => {
  it('is false under vitest node environment', () => {
    expect(browser).toBe(false);
    expect(typeof window).toBe('undefined');
  });
});
