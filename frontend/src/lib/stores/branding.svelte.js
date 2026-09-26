// Site branding: the logo shown in the sidebar and the favicon in the
// browser tab. Both are media references, never free-form URLs; the
// backend enforces that on write and this module re-checks on read, so a
// value that somehow got through cannot end up in an <img src> or a
// <link href>.
import { api } from './auth.svelte.js';
import { isMediaRef } from '../utils/mediaRef.js';

let logoState = $state('');

export const branding = {
  get logo() {
    return logoState;
  },
};

// The favicon lives outside the Svelte tree, so it is applied by editing
// the document head directly. An empty value restores whatever the page
// shipped with rather than leaving a dangling link.
let defaultFavicon = null;

function applyFavicon(href) {
  if (typeof document === 'undefined') return;
  const link = document.querySelector("link[rel~='icon']");
  if (!link) return;
  if (defaultFavicon === null) defaultFavicon = link.getAttribute('href') || '';
  link.setAttribute('href', href || defaultFavicon);
}

// Fetched without auth so the login screen is branded too. A failure is
// silent on purpose: branding is decoration, and a broken settings store
// must never stop the app from rendering.
export async function refreshBranding() {
  try {
    const res = await api.getBranding();
    logoState = isMediaRef(res?.logo) ? res.logo : '';
    applyFavicon(isMediaRef(res?.favicon) ? res.favicon : '');
  } catch {
    /* keep whatever is on screen */
  }
}
