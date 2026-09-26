// Canonical media reference: /api/media/<category>:<file>/raw
//
// This is the only form an image is pointed at from anywhere else in the
// UI (avatar, site logo, favicon, VM cover). Keeping one shape means every
// image lives in the media library instead of being copied into
// per-feature directories that drift apart.
//
// It is also a security boundary. These strings are rendered as <img src>,
// and they can reach the UI from localStorage, which anything running in
// the page may edit. Validating on read means a tampered entry degrades to
// "no picture" rather than becoming a "javascript:" payload or an external
// tracking pixel.
const MEDIA_REF_RE = /^\/api\/media\/(?:system|custom):[^/?#\\]+\/raw$/;

export function isMediaRef(v) {
  return typeof v === 'string' && MEDIA_REF_RE.test(v) && !v.includes('..');
}

export function mediaRawURL(id) {
  return `/api/media/${id}/raw`;
}
