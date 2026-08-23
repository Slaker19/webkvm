/**
 * Tiny hash-based router (~80 lines, no deps).
 *
 * Routes are defined as a list of patterns. Each pattern is `:segment` for a
 * required param, or `*` for a wildcard. The first matching pattern wins.
 *
 * Hash format: `#/vms/abc-123` → location.hash = "#/vms/abc-123"
 * The hash is everything after the leading "#", then we strip the leading "/".
 *
 * Exposes:
 *   - route: $state-derived object { name, path, params, query }
 *   - navigate(path, { query }?): push a new route, update location.hash
 *   - back(): history.back()
 *
 * Routes map:
 *   "" or "/"                  → "vms"   (default)
 *   "/vms"                     → "vms"
 *   "/vms/new"                 → "vms-new"
 *   "/vms/:id"                 → "vm-detail"
 *   "/storage"                 → "storage"
 *   "/networks"                → "networks"
 *   "/backup"                  → "backup"     (admin only)
 *   "/users"                   → "users"   (admin only)
 *   "/nodes"                   → "nodes"     (admin only)
 *   "/status"                  → "status"
 *   "/settings"                → "settings"   (admin only)
 *   "/account"                 → "account"
 *
 * Unmatched URLs → "not-found".
 *
 * Routes can declare `roles: ['admin', ...]` — non-matching roles get
 * "access-denied". The App.svelte reads route.name to decide which
 * component to render.
 */

import { browser } from './utils/browser.js';

const ROUTES = [
  { pattern: '', name: 'vms' },
  { pattern: 'vms', name: 'vms' },
  { pattern: 'vms/new', name: 'vms-new' },
  { pattern: 'vms/:id', name: 'vm-detail' },
  { pattern: 'storage', name: 'storage' },
  { pattern: 'networks', name: 'networks' },
  { pattern: 'backup', name: 'backup', roles: ['admin'] },
  { pattern: 'users', name: 'users', roles: ['admin'] },
  { pattern: 'nodes', name: 'nodes', roles: ['admin'] },
  { pattern: 'status', name: 'status' },
  { pattern: 'host-console', name: 'host-console', roles: ['admin'] },
  { pattern: 'settings', name: 'settings', roles: ['admin'] },
  { pattern: 'account', name: 'account' },
];

function parseHash() {
  const hash = (browser ? location.hash : '#/') || '#/';
  let path = hash.startsWith('#') ? hash.slice(1) : hash;
  if (!path.startsWith('/')) path = '/' + path;

  // Split path and query
  const [pathname, queryStr] = path.split('?');
  const segments = pathname.split('/').filter(Boolean);

  // Parse query
  const query = {};
  if (queryStr) {
    for (const part of queryStr.split('&')) {
      if (!part) continue;
      const [k, v] = part.split('=');
      query[decodeURIComponent(k)] = decodeURIComponent(v || '');
    }
  }

  // Match against patterns (in order — most specific first)
  for (const r of ROUTES) {
    const patternSegs = r.pattern.split('/').filter(Boolean);
    if (patternSegs.length !== segments.length) continue;
    const params = {};
    let match = true;
    for (let i = 0; i < patternSegs.length; i++) {
      const p = patternSegs[i];
      const s = segments[i];
      if (p.startsWith(':')) {
        params[p.slice(1)] = decodeURIComponent(s);
      } else if (p !== s) {
        match = false;
        break;
      }
    }
    if (match) {
      return { name: r.name, path: pathname, params, query, roles: r.roles || null };
    }
  }

  // No match → 404
  return { name: 'not-found', path: pathname, params: {}, query: {}, roles: null };
}

let _route = $state(parseHash());

if (browser) {
  window.addEventListener('hashchange', () => {
    _route = parseHash();
  });
}

export function getRoute() {
  return _route;
}

export function navigate(path, opts = {}) {
  if (!browser) return;
  let target = path.startsWith('#') ? path : '#' + (path.startsWith('/') ? path : '/' + path);
  if (opts.query && Object.keys(opts.query).length > 0) {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(opts.query)) {
      if (v == null || v === '') continue;
      qs.set(k, v);
    }
    const s = qs.toString();
    if (s) target += (target.includes('?') ? '&' : '?') + s;
  }
  if (location.hash === target) {
    // Force re-parse to trigger reactivity even when hash is unchanged
    _route = parseHash();
  } else {
    location.hash = target;
  }
}

export function back() {
  if (browser) history.back();
}
