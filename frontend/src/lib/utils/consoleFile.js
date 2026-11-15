// Download of a console launcher file (.vv for SPICE, .rdp) with the session
// cookie. Errors come back typed (code/status/data) so the caller can show the
// translated message instead of raw JSON or "HTTP 502".

import { remoteErrorText } from './remoteRoutes.js';

export class ConsoleFileError extends Error {
  constructor(message, status, code, data) {
    super(message);
    this.status = status;
    this.code = code || '';
    this.data = data || null;
  }
}

// fetchConsoleFile returns the Blob or throws ConsoleFileError.
export async function fetchConsoleFile(url, fetchImpl = fetch) {
  const res = await fetchImpl(url, { credentials: 'include' });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ConsoleFileError(body.error || '', res.status, body.code, body);
  }
  return res.blob();
}

// consoleFileErrorText: translated text for a failed download. Typed codes use
// remote.errors.<code>; anything else a generic translated sentence (never the
// raw backend text or "HTTP 502").
export function consoleFileErrorText(t, e) {
  const text = remoteErrorText(t, { code: e?.code, status: e?.status, data: e?.data });
  if (e?.code && text && text !== e.message && text !== String(e)) return text;
  if (e?.status === 501) return t('remote.errors.remote_unsupported');
  return t('remote.console.downloadFailed', { status: e?.status || '' });
}

// saveBlob triggers the browser download of blob as filename.
export function saveBlob(blob, filename) {
  const objectUrl = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = objectUrl;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(objectUrl);
}
