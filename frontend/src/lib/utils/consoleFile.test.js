import { describe, it, expect } from 'vitest';
import { fetchConsoleFile, consoleFileErrorText, ConsoleFileError } from './consoleFile.js';

const t = (k, p) => {
  const dict = {
    'remote.errors.host_key_unpinned': 'PINNED-TEXT',
    'remote.errors.console_busy': 'BUSY-TEXT',
    'remote.errors.remote_unsupported': 'UNSUPPORTED-TEXT',
    'remote.console.downloadFailed': `FAILED ${p?.status}`,
  };
  return dict[k] ?? k;
};
const resp = (status, body) => ({
  ok: status < 400,
  status,
  json: () => Promise.resolve(body),
  blob: () => Promise.resolve(new Blob(['x'])),
});

describe('fetchConsoleFile', () => {
  it('returns the blob on success', async () => {
    const b = await fetchConsoleFile('/x', () => Promise.resolve(resp(200, {})));
    expect(b.size).toBe(1);
  });
  it.each([
    [502, 'host_key_unpinned', 'PINNED-TEXT'],
    [503, 'console_busy', 'BUSY-TEXT'],
    [501, 'remote_unsupported', 'UNSUPPORTED-TEXT'],
  ])('%s %s becomes a translated message', async (status, code, text) => {
    const err = await fetchConsoleFile('/x', () =>
      Promise.resolve(resp(status, { error: 'raw backend text', code }))
    ).catch((e) => e);
    expect(err).toBeInstanceOf(ConsoleFileError);
    expect(consoleFileErrorText(t, err)).toBe(text);
  });
  it('never shows raw text or "HTTP 502" for an untyped failure', async () => {
    const err = await fetchConsoleFile('/x', () =>
      Promise.resolve(resp(502, { error: 'dial tcp 10.0.0.9:22: i/o timeout' }))
    ).catch((e) => e);
    expect(consoleFileErrorText(t, err)).toBe('FAILED 502');
  });
});
