import { describe, it, expect, beforeEach } from 'vitest';
import { gaugeStyle, setGaugeStyle, toggleGaugeStyle, loadColor } from './gaugeStyle.svelte.js';

beforeEach(() => {
  setGaugeStyle('radial');
});

describe('gaugeStyle store', () => {
  it('defaults to radial', () => {
    expect(gaugeStyle.mode).toBe('radial');
  });

  it('setGaugeStyle switches between the two supported modes', () => {
    setGaugeStyle('linear');
    expect(gaugeStyle.mode).toBe('linear');
    setGaugeStyle('radial');
    expect(gaugeStyle.mode).toBe('radial');
  });

  it('setGaugeStyle ignores unknown modes', () => {
    setGaugeStyle('spiral');
    expect(gaugeStyle.mode).toBe('radial');
  });

  it('toggleGaugeStyle flips the mode', () => {
    toggleGaugeStyle();
    expect(gaugeStyle.mode).toBe('linear');
    toggleGaugeStyle();
    expect(gaugeStyle.mode).toBe('radial');
  });
});

describe('loadColor', () => {
  it('is green below the warning threshold', () => {
    expect(loadColor(0)).toBe('var(--success)');
    expect(loadColor(69.9)).toBe('var(--success)');
  });

  it('is amber from 70% up to the critical threshold', () => {
    expect(loadColor(70)).toBe('var(--warning)');
    expect(loadColor(89.9)).toBe('var(--warning)');
  });

  it('is red at or above 90%', () => {
    expect(loadColor(90)).toBe('var(--destructive)');
    expect(loadColor(100)).toBe('var(--destructive)');
  });

  it('treats non-numeric input as zero rather than NaN-coloured', () => {
    expect(loadColor(undefined)).toBe('var(--success)');
    expect(loadColor(null)).toBe('var(--success)');
    expect(loadColor('not-a-number')).toBe('var(--success)');
  });
});
