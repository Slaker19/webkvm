// Returns `url` only when it is an absolute http(s) URL, otherwise undefined
// (an <a> without href is inert). Use for every href built from data that an
// admin, a catalog or a third party supplied: a stored `javascript:` or
// `data:` value would otherwise run script when clicked.
export function safeHref(url) {
  if (typeof url !== 'string') return undefined;
  const v = url.trim();
  if (!v) return undefined;
  try {
    const u = new URL(v);
    if ((u.protocol === 'http:' || u.protocol === 'https:') && u.hostname) return v;
  } catch (_e) {
    // not an absolute URL
  }
  return undefined;
}
