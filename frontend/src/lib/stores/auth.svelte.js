// V13-SEC-01: the session JWT lives in an HttpOnly cookie (never in JS).
// The SPA tracks only the *session status* ('checking'|'in'|'out') plus
// the non-secret identity, re-validated via /auth/me on every load. The
// CSRF value is the double-submit token echoed back as X-CSRF-Token on
// state-changing requests.
import { SvelteURLSearchParams } from 'svelte/reactivity';
import { isMediaRef } from '../utils/mediaRef.js';
import { t } from '../i18n.svelte.js';
import { localizedApiErrorMessage } from '../apiErrors.js';
import { notifySessionCleared } from '../sessionHooks.js';
const STATUS = { CHECKING: 'checking', IN: 'in', OUT: 'out' };

let statusState = $state(STATUS.CHECKING);
let userState = $state(localStorage.getItem('user') || '');
let roleState = $state(localStorage.getItem('role') || '');
let mustChangeState = $state(localStorage.getItem('must_change') === '1');
let csrfState = $state('');
let avatarState = $state(localStorage.getItem('avatar') || '');
let loggingOut = false;

function readCSRFCookie() {
  if (typeof document === 'undefined') return '';
  const m = /(?:^|; )webkvm_csrf=([^;]+)/.exec(document.cookie);
  return m ? decodeURIComponent(m[1]) : '';
}

// Imported lazily to avoid a circular dep at module-load
// time (auth store ↔ router both want to be importable
// from the other; routing only ever fires on 401, so
// dynamic import keeps the top-level graph a DAG).
async function redirectToLogin(reason) {
  try {
    const { navigate } = await import('../router.svelte.js');
    navigate('/login', { query: reason ? { reason } : null });
  } catch {
    // If the router fails to load, fall back to a hard
    // navigation so the user is not stuck on a broken
    // page with a dead session.
    location.href = '/login' + (reason ? '?reason=' + reason : '');
  }
}

function clearSessionState() {
  notifySessionCleared();
  userState = '';
  roleState = '';
  mustChangeState = false;
  csrfState = '';
  avatarState = '';
  localStorage.removeItem('user');
  localStorage.removeItem('role');
  localStorage.removeItem('must_change');
  localStorage.removeItem('avatar');
}

export const auth = {
  get status() {
    return statusState;
  },
  get user() {
    return userState;
  },
  get role() {
    return roleState;
  },
  get isLoggedIn() {
    return statusState === STATUS.IN;
  },
  get mustChangePassword() {
    return mustChangeState;
  },
  get avatar() {
    return avatarState;
  },

  // Called after the user picks a new profile picture, so the header
  // updates without a reload.
  setAvatar(url) {
    avatarState = isMediaRef(url) ? url : '';
    if (avatarState) localStorage.setItem('avatar', avatarState);
    else localStorage.removeItem('avatar');
  },

  setSession(u, r, mustChange = false, csrf = '') {
    userState = u;
    roleState = r || '';
    mustChangeState = !!mustChange;
    csrfState = csrf || readCSRFCookie();
    statusState = STATUS.IN;
    localStorage.setItem('user', u);
    localStorage.setItem('role', r || '');
    localStorage.setItem('must_change', mustChange ? '1' : '0');
  },

  setMustChange(v) {
    mustChangeState = !!v;
    localStorage.setItem('must_change', v ? '1' : '0');
  },

  // Called on every app load: re-validate the cookie session.
  async bootstrap() {
    statusState = STATUS.CHECKING;
    try {
      const u = await api.me();
      auth.setSession(u.username, u.role, u.must_change_password, readCSRFCookie());
      auth.setAvatar(u.avatar);
      return true;
    } catch {
      // request() already called onUnauthorized() on 401 (status -> out).
      if (statusState !== STATUS.OUT) statusState = STATUS.OUT;
      clearSessionState();
      statusState = STATUS.OUT;
      return false;
    }
  },

  // Expired/invalid session: clear local state and bounce to /login.
  // The "session expired" banner is only meaningful when there WAS a
  // session: a signed-in user whose requests start failing, or a reload
  // with a remembered identity whose cookie has since lapsed. A first
  // visit (bootstrap /auth/me → 401 with nothing remembered), stragglers
  // after a deliberate logout, or anything while already signed out go
  // to /login silently.
  onUnauthorized(reason = 'session_expired') {
    const hadSession =
      !loggingOut &&
      (statusState === STATUS.IN || (statusState === STATUS.CHECKING && !!userState));
    clearSessionState();
    statusState = STATUS.OUT;
    redirectToLogin(hadSession ? reason : null);
  },

  // Server-side logout clears the HttpOnly cookie (fire-and-forget; the
  // server ignores failures), then we reset the local state. While it is
  // in flight, polls/SSE racing the cookie removal can 401; `loggingOut`
  // keeps those from turning a normal logout into "session expired".
  logout() {
    if (statusState === STATUS.IN) {
      loggingOut = true;
      api
        .logoutApi()
        .catch(() => {
          /* cookie may already be gone */
        })
        .finally(() => {
          clearSessionState();
          statusState = STATUS.OUT;
          // Let late responses from the old session settle first.
          setTimeout(() => {
            loggingOut = false;
          }, 1000);
        });
    } else {
      clearSessionState();
      statusState = STATUS.OUT;
    }
  },

  isAdmin() {
    return roleState === 'admin';
  },
  canMutate() {
    return roleState === 'admin' || roleState === 'operator';
  },
};

const BASE = '/api';

export class ApiError extends Error {
  // `data` carries the parsed error body. Some failures are not just a
  // message: a 409 on media delete also reports *what* still uses the
  // image, and the caller can only warn the user properly if that
  // survives the throw. It was previously passed at the 429 call site
  // and silently dropped here.
  constructor(message, status, code, data = null) {
    super(message);
    this.status = status;
    this.code = code;
    this.data = data;
  }
}

const UNSAFE_METHODS = ['POST', 'PUT', 'PATCH', 'DELETE'];

// `credentialCheck: true` (or a predicate over the JSON body) marks
// endpoints whose 401 means "the password or code you just typed is
// wrong" (login, 2FA step, 2FA disable), not
// "your session is gone". Those must surface as an ordinary error the
// form can show — routing them through onUnauthorized() made a typo'd
// password log the user out / show "session expired".
// `skipAuthRedirect: true` is for /auth/logout, whose 401 just means the
// cookie was already gone.
function routeRemoteVMDetail(path) {
  if (typeof window === 'undefined') return path;
  const apiMatch = path.match(/^\/vms\/([^/?]+)/);
  if (!apiMatch) return path;

  const [routePath, routeQuery = ''] = window.location.hash.slice(1).split('?');
  const routeMatch = routePath.match(/^\/vms\/([^/]+)$/);
  if (!routeMatch || routeMatch[1] !== apiMatch[1]) return path;

  const params = new URLSearchParams(routeQuery);
  const node = params.get('node');
  if (!node || node === 'local') return path;
  // hv=kvm|incus disambiguates a name that exists as both a KVM node and an
  // Incus member; the detail route carries it next to node.
  const hv = params.get('hv');
  const own = new URLSearchParams(path.split('?')[1] || '');
  if (own.has('node')) {
    // A call that already names the node but forgot hv: add the route's hv
    // when it targets the same node (an Incus member's instance would
    // otherwise take the libvirt/SSH path and answer 409 no_key).
    if (own.get('node') === node && !own.has('hv') && (hv === 'kvm' || hv === 'incus')) {
      return `${path}&hv=${hv}`;
    }
    return path;
  }
  const sel = `node=${encodeURIComponent(node)}${hv === 'kvm' || hv === 'incus' ? `&hv=${hv}` : ''}`;
  return `${path}${path.includes('?') ? '&' : '?'}${sel}`;
}

async function request(path, opts = {}) {
  path = routeRemoteVMDetail(path);
  const { credentialCheck = false, skipAuthRedirect = false, ...fetchOpts } = opts;
  const method = (fetchOpts.method || 'GET').toUpperCase();
  const headers = { 'Content-Type': 'application/json', ...fetchOpts.headers };
  // V13-SEC-01: cookie session + double-submit CSRF on mutations.
  if (UNSAFE_METHODS.includes(method) && csrfState) headers['X-CSRF-Token'] = csrfState;

  const res = await fetch(`${BASE}${path}`, {
    ...fetchOpts,
    method,
    headers,
    credentials: 'include',
  });

  if (res.status === 401 && (credentialCheck || skipAuthRedirect)) {
    const body = await res
      .clone()
      .json()
      .catch(() => ({}));
    const isCredential =
      typeof credentialCheck === 'function' ? credentialCheck(body) : credentialCheck;
    if (isCredential || skipAuthRedirect) {
      throw new ApiError(
        localizedApiErrorMessage(body, 'Unauthorized'),
        401,
        body?.code || (isCredential ? 'invalid_credentials' : 'unauthorized'),
        body
      );
    }
  }

  if (res.status === 401) {
    auth.onUnauthorized();
    // The previous behaviour was to throw a generic
    // "Unauthorized" toast and leave the user on the page
    // — fine for deliberate logout, terrible for an
    // expired session: the operator clicks "Restore" and
    // the page silently does nothing. The v6 release
    // surfaced this when an admin's JWT expired between
    // refresh and the first API call. Redirect to /login
    // with a `reason=session_expired` so the login page
    // can show "Tu sesión expiró" instead of the silent
    // failure.
    throw new ApiError('Session expired', 401, 'unauthorized');
  }

  const text = await res.text().catch(() => '');
  let data;
  try {
    data = JSON.parse(text);
  } catch {
    throw new ApiError(text || `HTTP ${res.status}: empty response`, res.status, 'invalid_json');
  }
  if (!res.ok) {
    if (res.status === 429) {
      // surface Retry-After if present
      const retry = res.headers.get('Retry-After');
      throw new ApiError(localizedApiErrorMessage(data, 'Too many requests'), 429, 'rate_limited', {
        retryAfter: retry,
      });
    }
    if (res.status === 403) {
      // A missing/mismatched CSRF cookie is unrecoverable without a
      // fresh login: /auth/refresh itself requires the very same
      // double-submit pairing to succeed, so retrying through it would
      // 403 again for the same reason (no header/cookie combination we
      // could resend would suddenly start matching). Rather than
      // surfacing this raw backend string wherever the failing call
      // happens to render it (e.g. a terminal panel), treat it like an
      // expired session: clear local state and bounce to /login, same
      // as the 401 path above.
      if (data.error === 'invalid csrf token') {
        auth.onUnauthorized('session_expired');
        throw new ApiError('Session expired', 403, 'unauthorized');
      }
      throw new ApiError(
        localizedApiErrorMessage(data, 'Forbidden'),
        403,
        data.code || 'forbidden',
        data
      );
    }
    throw new ApiError(
      localizedApiErrorMessage(data, data.message || `Request failed (${res.status})`),
      res.status,
      data.code || 'request_failed',
      data
    );
  }
  if (data?.csrf) csrfState = data.csrf;
  return data;
}

// nodeQuery appends the ?node= selector for a remote cluster node. Empty
// or "local" (the VM's own "local" encoding) yields an empty string so the
// URL stays clean for local instances.
// Body for POST /modules/ha/preflight and /modules/ha/install. Accepts the
// legacy bare boolean (LINSTOR flag) or an options object
// { linstor, accept_experimental, fencing: ['sbd'|'pve'|'redfish'|'ipmi'] }.
// Only meaningful, known keys are sent so an unchecked option never reaches
// the backend as an explicit opt-in.
const HA_FENCING_KINDS = ['sbd', 'pve', 'redfish', 'ipmi'];
export function haModuleOptionsBody(opts) {
  const o = opts !== null && typeof opts === 'object' ? opts : { linstor: !!opts };
  const body = { linstor: !!o.linstor };
  if (o.accept_experimental) body.accept_experimental = true;
  if (Array.isArray(o.fencing)) {
    const fencing = o.fencing.filter(
      (k, i, all) => HA_FENCING_KINDS.includes(k) && all.indexOf(k) === i
    );
    if (fencing.length) body.fencing = fencing;
  }
  return JSON.stringify(body);
}

// `&node=…&hv=…` continuation (for URLs that already carry a query).
function nodeParams(node, hv) {
  return nodeQuery(node, hv).replace(/^\?/, '&');
}

// Plain node-id selector for list endpoints (pools, networks, bridges).
function nodeSelector(node) {
  return node && node !== 'local' ? `?node=${encodeURIComponent(node)}` : '';
}

function nodeQuery(node, hv) {
  if (!node || node === 'local') return '';
  return `?node=${encodeURIComponent(node)}${hv === 'kvm' || hv === 'incus' ? `&hv=${hv}` : ''}`;
}

// URL (path + query) of one HA workload: `/ha/workloads/kvm/name` or
// `/ha/workloads/incus/name/<suffix>?project=p`. Extra query params (mode,
// confirm) follow the project.
function haWorkloadPath(kind, name, project, suffix = '', extra = {}) {
  const params = new SvelteURLSearchParams();
  if (kind === 'incus') params.set('project', project || 'default');
  for (const [k, v] of Object.entries(extra)) params.set(k, v);
  const qs = params.toString();
  return `/ha/workloads/${encodeURIComponent(kind)}/${encodeURIComponent(name)}${suffix}${qs ? '?' + qs : ''}`;
}

export const api = {
  // --- auth ---
  login: (username, password) =>
    request('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
      credentialCheck: true,
    }),
  login2FA: (mfa_token, code) =>
    request('/auth/login/2fa', {
      method: 'POST',
      body: JSON.stringify({ mfa_token, code }),
      credentialCheck: true,
    }),
  setup2FA: () => request('/auth/2fa/setup', { method: 'POST' }),
  enable2FA: (secret, code, backup_codes) =>
    request('/auth/2fa/enable', {
      method: 'POST',
      body: JSON.stringify({ secret, code, backup_codes }),
    }),
  disable2FA: (password) =>
    request('/auth/2fa/disable', {
      method: 'POST',
      body: JSON.stringify({ password }),
      // This route sits behind the session middleware, so a 401 can also
      // be a genuinely expired session; only the handler's wrong-password
      // answer is a form error.
      credentialCheck: (body) => body?.code === 'invalid_password',
    }),

  // --- WebAuthn / Passkeys ---
  webauthnRegisterBegin: () => request('/auth/webauthn/register/begin', { method: 'POST' }),
  webauthnRegisterFinish: (data) =>
    request('/auth/webauthn/register/finish', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  webauthnListCredentials: () => request('/auth/webauthn/credentials'),
  webauthnUpdateCredential: (id, name) =>
    request(`/auth/webauthn/credentials/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify({ name }),
    }),
  webauthnDeleteCredential: (id) =>
    request(`/auth/webauthn/credentials/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  webauthnLoginBegin: (username) =>
    request('/auth/webauthn/login/begin', {
      method: 'POST',
      body: JSON.stringify({ username: username || '' }),
      credentialCheck: true,
    }),
  webauthnLoginFinish: (data) =>
    request('/auth/webauthn/login/finish', {
      method: 'POST',
      body: JSON.stringify(data),
      credentialCheck: true,
    }),
  logoutApi: () => request('/auth/logout', { method: 'POST', skipAuthRedirect: true }),
  refresh: () => request('/auth/refresh', { method: 'POST' }),
  me: () => request('/auth/me'),
  changeMyPassword: (old_password, new_password) =>
    request('/users/me/password', {
      method: 'PUT',
      body: JSON.stringify({ old_password, new_password }),
    }),

  // --- VMs ---
  // opts.scope === 'local': this server's instances only, without waiting
  // for the remote nodes (the list renders them first, 8.30).
  listVMs: (opts = {}) => request(opts?.scope === 'local' ? '/vms?scope=local' : '/vms'),
  getVM: (id, node, hv) => request(`/vms/${id}${nodeQuery(node, hv)}`),
  listIncusProfiles: () => request('/vms/incus-profiles'),
  listIncusImages: () => request('/vms/incus-images'),
  createVM: (data) => request('/vms', { method: 'POST', body: JSON.stringify(data) }),
  updateVM: (id, data) => request(`/vms/${id}`, { method: 'PATCH', body: JSON.stringify(data) }),
  deleteVM: (id, withDisks = false, node, hv) =>
    request(`/vms/${id}${withDisks ? `?disks=true${nodeParams(node, hv)}` : nodeQuery(node, hv)}`, {
      method: 'DELETE',
    }),
  startVM: (id, node, hv) => request(`/vms/${id}/start${nodeQuery(node, hv)}`, { method: 'POST' }),
  shutdownVM: (id, node, hv) =>
    request(`/vms/${id}/shutdown${nodeQuery(node, hv)}`, { method: 'POST' }),
  forceOffVM: (id, node, hv) =>
    request(`/vms/${id}/forceoff${nodeQuery(node, hv)}`, { method: 'POST' }),
  rebootVM: (id, node, hv) =>
    request(`/vms/${id}/reboot${nodeQuery(node, hv)}`, { method: 'POST' }),
  suspendVM: (id, node, hv) =>
    request(`/vms/${id}/suspend${nodeQuery(node, hv)}`, { method: 'POST' }),
  resetVMPassword: (id) => request(`/vms/${id}/reset-password`, { method: 'POST' }),
  resumeVM: (id, node, hv) =>
    request(`/vms/${id}/resume${nodeQuery(node, hv)}`, { method: 'POST' }),
  // Per-workload "managed" flag (PUT …/managed?node=&hv=).
  setVMManaged: (id, node, hv, managed) =>
    request(`/vms/${encodeURIComponent(id)}/managed${nodeQuery(node, hv)}`, {
      method: 'PUT',
      body: JSON.stringify({ managed: !!managed }),
    }),
  // Incus cluster: move an instance to another member.
  moveVM: (id, targetMember, live = false, node, hv) =>
    request(`/vms/${encodeURIComponent(id)}/move${nodeQuery(node, hv)}`, {
      method: 'POST',
      body: JSON.stringify({ target_member: targetMember, live: !!live }),
    }),
  evacuateIncusMember: (name, mode = '', force = false) =>
    request(`/cluster/members/${encodeURIComponent(name)}/evacuate`, {
      method: 'POST',
      body: JSON.stringify({ ...(mode ? { mode } : {}), ...(force ? { force: true } : {}) }),
    }),
  restoreIncusMember: (name, force = false) =>
    request(`/cluster/members/${encodeURIComponent(name)}/restore`, {
      method: 'POST',
      body: JSON.stringify(force ? { force: true } : {}),
    }),
  migrateVM: (id, sourceNode, destNode, live = false, sharedStorage = false) =>
    request(`/vms/${id}/migrate${nodeQuery(sourceNode)}`, {
      method: 'POST',
      body: JSON.stringify({ node: destNode, live, shared_storage: sharedStorage }),
    }),
  // Autostart toggles libvirtd's per-VM autostart flag (not the
  // host's "auto-start VMs at boot" master switch, which is
  // controlled separately by systemd/libvirtd config). The flag
  // is also returned as a field on the VM object so the UI can
  // initialize the switch without a second round-trip; this
  // setter is the only way to change it.
  setVMAutostart: (id, enabled, node, hv) =>
    request(`/vms/${id}/autostart${nodeQuery(node, hv)}`, {
      method: 'POST',
      body: JSON.stringify({ enabled }),
    }),
  // node/hv: owning node of the VM; hv=incus keeps an Incus member's
  // instance on the Incus handlers (without it the backend takes the
  // libvirt/SSH path for that node name).
  listSnapshots: (vmId, node, hv) => request(`/vms/${vmId}/snapshots${nodeQuery(node, hv)}`),
  createSnapshot: (vmId, data, node, hv) =>
    request(`/vms/${vmId}/snapshots${nodeQuery(node, hv)}`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  deleteSnapshot: (vmId, sid, node, hv) =>
    request(`/vms/${vmId}/snapshots/${sid}${nodeQuery(node, hv)}`, { method: 'DELETE' }),
  revertSnapshot: (vmId, sid, node, hv) =>
    request(`/vms/${vmId}/snapshots/${sid}/revert${nodeQuery(node, hv)}`, { method: 'POST' }),
  getVMFirewall: (vmId) => request(`/vms/${vmId}/firewall`),
  setVMFirewall: (vmId, data) =>
    request(`/vms/${vmId}/firewall`, { method: 'PUT', body: JSON.stringify(data) }),
  makeVMTemplate: (vmId, node, hv) =>
    request(`/vms/${vmId}/make-template${nodeQuery(node, hv)}`, { method: 'POST' }),
  unsetVMTemplate: (vmId, node, hv) =>
    request(`/vms/${vmId}/unset-template${nodeQuery(node, hv)}`, { method: 'POST' }),
  listTemplates: () => request('/templates'),
  instantiateTemplate: (id, data) =>
    request(`/templates/${id}/instantiate`, { method: 'POST', body: JSON.stringify(data) }),
  listAppliances: () => request('/appliances'),
  deployAppliance: (id, data) =>
    request(`/appliances/${id}/deploy`, { method: 'POST', body: JSON.stringify(data) }),
  createAppliance: (data) => request('/appliances', { method: 'POST', body: JSON.stringify(data) }),
  updateAppliance: (id, data) =>
    request(`/appliances/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  deleteAppliance: (id) => request(`/appliances/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  getApplianceProvision: (id) => request(`/appliances/${encodeURIComponent(id)}/provision`),
  listHelperScripts: () => request('/helper-scripts'),
  // Re-imports the upstream catalog: several hundred outbound requests,
  // so the caller must show progress rather than assume it is quick.
  refreshHelperScripts: () => request('/helper-scripts/refresh', { method: 'POST' }),
  getHelperScriptProvision: (slug) =>
    request(`/helper-scripts/${encodeURIComponent(slug)}/provision`),
  getConsoleTicket: (vmId, node, hv) =>
    request(`/vms/${encodeURIComponent(vmId)}/console-ticket${nodeQuery(node, hv)}`, {
      method: 'POST',
    }),
  getVNCTicket: (vmId, node, hv) =>
    request(`/vms/${encodeURIComponent(vmId)}/vnc-ticket${nodeQuery(node, hv)}`, {
      method: 'POST',
    }),
  createVNCTicket: (vmId, node, hv) =>
    request(`/vms/${encodeURIComponent(vmId)}/vnc-ticket${nodeQuery(node, hv)}`, {
      method: 'POST',
    }),
  getHostTerminalTicket: () => request('/host/terminal-ticket', { method: 'POST' }),
  getEventsTicket: () => request('/events/ticket', { method: 'POST' }),
  getVMSchedule: (vmId) => request(`/vms/${vmId}/schedule`),
  setVMSchedule: (vmId, data) =>
    request(`/vms/${vmId}/schedule`, { method: 'PUT', body: JSON.stringify(data) }),

  // --- media pool (avatars, logos, favicons, VM covers) ---
  listMedia: () => request('/media'),
  uploadMedia: (file, onProgress) => {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const formData = new FormData();
      formData.append('file', file);

      if (onProgress) {
        xhr.upload.addEventListener('progress', (e) => {
          if (e.lengthComputable) {
            onProgress(Math.round((e.loaded / e.total) * 100));
          }
        });
      }
      xhr.addEventListener('load', () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve(JSON.parse(xhr.responseText || '{}'));
        } else if (xhr.status === 401) {
          auth.onUnauthorized();
          reject(new ApiError('Unauthorized', 401, 'unauthorized'));
        } else {
          let msg = 'Upload failed';
          try {
            const j = JSON.parse(xhr.responseText);
            if (j.error) msg = j.error;
          } catch {
            /* ignore */
          }
          reject(new ApiError(msg, xhr.status, 'upload_failed'));
        }
      });
      xhr.addEventListener('error', () => reject(new ApiError('Upload failed', 0, 'network')));

      xhr.open('POST', `${BASE}/media/upload`);
      xhr.withCredentials = true;
      if (csrfState) xhr.setRequestHeader('X-CSRF-Token', csrfState);
      xhr.send(formData);
    });
  },
  getOIDCStatus: () => request('/auth/oidc/status'),
  // force=true deletes an image even when something still points at it.
  // Without it the backend answers 409 and lists the usages, so the user
  // can be told what will break before it breaks.
  deleteMedia: (id, force = false) =>
    request(`/media/${encodeURIComponent(id)}${force ? '?force=true' : ''}`, { method: 'DELETE' }),
  applyMediaUsage: (data) =>
    request('/media/apply-usage', { method: 'POST', body: JSON.stringify(data) }),
  getBranding: () => request('/branding'),

  // --- storage ---
  probeDisk: (path, deep = false) =>
    request('/storage/probe-disk', {
      method: 'POST',
      body: JSON.stringify({ path, deep }),
    }),
  listPools: (node) => request(`/storage/pools${nodeSelector(node)}`),
  createPool: (data) => request('/storage/pools', { method: 'POST', body: JSON.stringify(data) }),
  // browseRemote lists the subfolders of an NFS export or SMB share —
  // shared by the storage pool AND backup target forms (see
  // RemoteFolderBrowser.svelte).
  browseRemote: (data) =>
    request('/storage/browse-remote', { method: 'POST', body: JSON.stringify(data) }),
  // Folder picker for a path on the server itself (LocalFolderBrowser).
  // An empty path asks for the root list: the mount points a pool can
  // sensibly live under, not a raw listing of /.
  browseLocal: (path = '') =>
    request('/storage/browse-local', { method: 'POST', body: JSON.stringify({ path }) }),
  // updatePool calls PUT /api/storage/pools/{name} to rotate
  // credentials on a CIFS pool or to drive the cifs-needs-reauth
  // recovery path after a libvirtd reinstall. The backend
  // accepts a partial body (only the fields the operator wants
  // to change); unknown fields are rejected.
  updatePool: (name, data) =>
    request(`/storage/pools/${encodeURIComponent(name)}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  deletePool: (name) => request(`/storage/pools/${name}`, { method: 'DELETE' }),
  // node: optional remote libvirt node (admin-only, ?node=).
  listVolumes: (pool, node) =>
    request(
      `/storage/volumes?pool=${encodeURIComponent(pool)}${nodeSelector(node).replace(/^\?/, '&')}`
    ),
  createVolume: (data) =>
    request('/storage/volumes', { method: 'POST', body: JSON.stringify(data) }),
  resizeVolume: (pool, name, capacity) =>
    request(`/storage/volumes/${pool}/${name}`, {
      method: 'PATCH',
      body: JSON.stringify({ capacity }),
    }),
  deleteVolume: (pool, name) => request(`/storage/volumes/${pool}/${name}`, { method: 'DELETE' }),
  // Relocate a volume or ISO to another pool of the same kind. Answers
  // 202 with a job: a multi-GB copy outlives the request, so the caller
  // polls getDownloadJob() for progress.
  moveVolume: (pool, name, data) =>
    request(`/storage/volumes/${encodeURIComponent(pool)}/${encodeURIComponent(name)}/move`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  // node: optional remote libvirt node — lists the ISO volumes of ITS pools.
  listISOs: (pool, node) => {
    const q = [
      pool ? 'pool=' + encodeURIComponent(pool) : '',
      nodeSelector(node).replace(/^\?/, ''),
    ]
      .filter(Boolean)
      .join('&');
    return request(`/storage/isos${q ? '?' + q : ''}`);
  },
  // pool is required: it goes in the path, and the ISO library's name
  // is the server's to decide (it was renamed ISOS -> webkvm-isos in
  // v2.5). A client-side default here would be a second source of
  // truth that silently goes stale.
  deleteISO: (name, pool) =>
    request(`/storage/isos/${encodeURIComponent(pool)}/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    }),
  renameISO: (name, newName, pool) =>
    request(`/storage/isos/${encodeURIComponent(pool)}/${encodeURIComponent(name)}`, {
      method: 'PATCH',
      body: JSON.stringify({ new_name: newName }),
    }),
  downloadISO: (url, name, pool) =>
    request('/storage/download-iso', { method: 'POST', body: JSON.stringify({ url, name, pool }) }),

  // --- image hub (containers, cloud-base, isos) ---
  listContainerImages: () => request('/images/containers'),
  // Where the Incus daemon caches downloaded container images. Empty
  // pool restores its default directory on the system disk.
  getIncusImagesVolume: () => request('/images/incus-volume'),
  setIncusImagesVolume: (pool, volume = '') =>
    request('/images/incus-volume', { method: 'PUT', body: JSON.stringify({ pool, volume }) }),
  pullContainerImage: (ref) =>
    request('/images/containers/pull', { method: 'POST', body: JSON.stringify({ ref }) }),
  deleteContainerImage: (fp) =>
    request(`/images/containers/${encodeURIComponent(fp)}`, { method: 'DELETE' }),
  listBaseCloudImages: () => request('/images/cloud-base'),
  // pool is optional: the backend falls back to the default disk pool,
  // which is what this call did before the destination was selectable.
  pullBaseCloudImage: (id, pool = '') =>
    request('/images/cloud-base/pull', { method: 'POST', body: JSON.stringify({ id, pool }) }),
  deleteBaseCloudImage: (id) =>
    request(`/images/cloud-base/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  uploadISO: (file, onProgress, pool) => {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const formData = new FormData();
      formData.append('file', file);
      formData.append('pool', pool);

      xhr.upload.addEventListener('progress', (e) => {
        if (e.lengthComputable) {
          onProgress(Math.round((e.loaded / e.total) * 100));
        }
      });
      xhr.addEventListener('load', () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve(JSON.parse(xhr.responseText || '{}'));
        } else if (xhr.status === 401) {
          auth.onUnauthorized();
          reject(new ApiError('Unauthorized', 401, 'unauthorized'));
        } else {
          let msg = 'Upload failed';
          try {
            const j = JSON.parse(xhr.responseText);
            if (j.error) msg = j.error;
          } catch {
            /* ignore: non-JSON response */
          }
          reject(new ApiError(msg, xhr.status, 'upload_failed'));
        }
      });
      xhr.addEventListener('error', () => reject(new ApiError('Upload failed', 0, 'network')));

      xhr.open('POST', `${BASE}/storage/upload-iso`);
      xhr.withCredentials = true;
      if (csrfState) xhr.setRequestHeader('X-CSRF-Token', csrfState);
      xhr.send(formData);
    });
  },

  getDownloadJob: (jobId) => request(`/storage/jobs/${jobId}`),
  listJobs: () => request('/jobs'),
  getJobHistory: (limit = 50) => request(`/jobs/history?limit=${limit}`),
  getJobStats: () => request('/jobs/stats'),
  // getJobLogText fetches the job's plain-text progress log (the request
  // helper always parses JSON, so this goes through fetch directly).
  getJobLogText: async (jobId) => {
    const res = await fetch(`${BASE}/jobs/${encodeURIComponent(jobId)}/log`, {
      credentials: 'include',
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    return res.text();
  },

  // Async job tracking for long operations (VM clone / snapshot / migrations).
  // The endpoints return 202 + {job}; waitJob polls until the job reaches a
  // terminal state and returns its result (or throws the job's error).
  // Automatically synchronizes with the global Task Center so all jobs
  // display a real progress bar in the UI.
  waitJob: async (jobId, opts = {}) => {
    const delay = opts.delay ?? 800;
    const timeout = opts.timeout ?? 10 * 60 * 1000;
    const started = Date.now();
    const taskId = 'async:' + jobId;

    // Lazily import tasks store to avoid circular imports
    let tasksModule = null;
    try {
      tasksModule = await import('./tasks.svelte.js');
    } catch {
      /* ignore if in restricted test environment */
    }

    for (;;) {
      const job = await request(`/jobs/${jobId}`);
      opts.onPoll?.(job);

      const pct = Math.min(100, Math.max(0, Math.round(job.progress || 0)));
      if (tasksModule) {
        let title = opts.title || job.name || t('taskCenter.backgroundDefault');
        if (title.startsWith('clone:')) title = t('taskCenter.cloningVM', { name: title.slice(6) });
        else if (title.startsWith('batch-clone:')) {
          title = t('taskCenter.batchCloning', { name: title.slice(12) });
        } else if (title.startsWith('snapshot:')) {
          title = t('taskCenter.creatingSnapshot', { name: title.slice(9) });
        } else if (title.startsWith('move ')) {
          title = t('taskCenter.movingDisk', { name: title.slice(5) });
        } else if (title.startsWith('evacuate:')) {
          title = t('taskCenter.evacuatingNode', { name: title.slice(9) });
        }

        tasksModule.upsertTask({
          id: taskId,
          kind: opts.kind || 'general',
          title,
          pct,
          message: job.message || `${pct}%`,
          status: 'running',
        });
      }

      if (job.status === 'done' || job.status === 'completed') {
        if (tasksModule) {
          tasksModule.finishTask(
            taskId,
            'success',
            job.message || t('taskCenter.successDefault'),
            100
          );
        }
        return job.result ?? job;
      }
      if (job.status === 'error') {
        const errMsg = localizedApiErrorMessage(
          { code: job.error_code, error: job.error || job.message },
          'Job failed'
        );
        if (tasksModule) {
          tasksModule.finishTask(taskId, 'error', errMsg, pct);
        }
        throw new ApiError(errMsg, 500, job.error_code || 'job_error');
      }
      if (Date.now() - started > timeout) {
        if (tasksModule) {
          tasksModule.finishTask(taskId, 'error', t('taskCenter.timeout'), pct);
        }
        throw new ApiError('Job timed out', 504, 'job_timeout');
      }
      await new Promise((resolve) => setTimeout(resolve, delay));
    }
  },

  uploadDisk: (file, onProgress, pool) => {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const formData = new FormData();
      // "pool" must be appended before "file" — the backend streams the
      // multipart body part-by-part and needs the pool name to run the
      // ACL/quota check before it starts writing the (potentially huge)
      // file part to disk.
      formData.append('pool', pool);
      formData.append('file', file);

      xhr.upload.addEventListener('progress', (e) => {
        if (e.lengthComputable) {
          onProgress(Math.round((e.loaded / e.total) * 100));
        }
      });
      xhr.addEventListener('load', () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve(JSON.parse(xhr.responseText || '{}'));
        } else if (xhr.status === 401) {
          auth.onUnauthorized();
          reject(new ApiError('Unauthorized', 401, 'unauthorized'));
        } else {
          let msg = 'Upload failed';
          try {
            const j = JSON.parse(xhr.responseText);
            if (j.error) msg = j.error;
          } catch {
            /* ignore: non-JSON response */
          }
          reject(new ApiError(msg, xhr.status, 'upload_failed'));
        }
      });
      xhr.addEventListener('error', () => reject(new ApiError('Upload failed', 0, 'network')));

      xhr.open('POST', `${BASE}/storage/upload-disk`);
      xhr.withCredentials = true;
      if (csrfState) xhr.setRequestHeader('X-CSRF-Token', csrfState);
      xhr.send(formData);
    });
  },

  // --- users ---
  listUsers: () => request('/users'),
  createUser: (data) => request('/users', { method: 'POST', body: JSON.stringify(data) }),
  updateUser: (username, data) =>
    request(`/users/${username}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteUser: (username) => request(`/users/${username}`, { method: 'DELETE' }),
  revokeUserSessions: (username) =>
    request(`/users/${username}/revoke-sessions`, { method: 'POST' }),

  // --- networks ---
  listNetworks: (node) => request(`/networks${nodeSelector(node)}`),
  createNetwork: (data) => request('/networks', { method: 'POST', body: JSON.stringify(data) }),
  updateNetwork: (id, data) =>
    request(`/networks/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteNetwork: (id) => request(`/networks/${id}`, { method: 'DELETE' }),
  startNetwork: (id) => request(`/networks/${id}/start`, { method: 'POST' }),
  stopNetwork: (id) => request(`/networks/${id}/stop`, { method: 'POST' }),
  listNetworkLeases: (id) => request(`/networks/${id}/leases`),
  releaseNetworkLease: (id, mac, ip) =>
    request(`/networks/${id}/leases/${encodeURIComponent(mac)}?ip=${encodeURIComponent(ip)}`, {
      method: 'DELETE',
    }),

  // --- host ---
  getHostInfo: () => request('/host'),
  getHostStats: () => request('/host/stats'),
  listHostInterfaces: (node) => request(`/host/interfaces${nodeSelector(node)}`),
  configureHostInterface: (name, data) =>
    request(`/host/interfaces/${name}/configure`, { method: 'POST', body: JSON.stringify(data) }),
  createHostBond: (data) => request('/host/bonds', { method: 'POST', body: JSON.stringify(data) }),
  deleteHostBond: (name) =>
    request(`/host/bonds/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  listHostUSBDevices: () => request('/host/usb-devices'),
  listHostPCIDevices: () => request('/host/pci-devices'),
  getHostPCIPreflight: () => request('/host/pci-preflight'),
  bindHostPCIDeviceVFIO: (address) =>
    request(`/host/pci-devices/${encodeURIComponent(address)}/bind-vfio`, { method: 'POST' }),
  unbindHostPCIDeviceVFIO: (address) =>
    request(`/host/pci-devices/${encodeURIComponent(address)}/unbind-vfio`, { method: 'POST' }),
  getHostUpdates: () => request('/host/updates'),
  refreshHostUpdates: () => request('/host/updates/refresh', { method: 'POST' }),
  upgradeHostUpdates: (packages = []) =>
    request('/host/updates/upgrade', {
      method: 'POST',
      body: packages?.length ? JSON.stringify({ packages }) : undefined,
    }),
  rebootHost: () => request('/host/reboot', { method: 'POST' }),
  getHostUpdateSchedule: () => request('/host/updates/schedule'),
  setHostUpdateSchedule: (cfg) =>
    request('/host/updates/schedule', { method: 'PUT', body: JSON.stringify(cfg) }),
  getGuestInfo: (id) => request(`/vms/${id}/guest-info`),
  guestFSTrim: (id) => request(`/vms/${id}/guest/fstrim`, { method: 'POST' }),
  guestExec: (id, data) =>
    request(`/vms/${id}/guest/exec`, { method: 'POST', body: JSON.stringify(data) }),
  guestFSFreeze: (id, freeze) =>
    request(`/vms/${id}/guest/freeze`, { method: 'POST', body: JSON.stringify({ freeze }) }),
  guestFSFreezeStatus: (id) => request(`/vms/${id}/guest/freeze-status`),
  guestSyncTime: (id) => request(`/vms/${id}/guest/sync-time`, { method: 'POST' }),
  listHostDisks: () => request('/host/disks'),
  listHostZVols: () => request('/host/zvols'),
  listHostZpools: () => request('/host/zpools'),
  createHostZpool: (data) =>
    request('/host/zpools', { method: 'POST', body: JSON.stringify(data) }),
  destroyHostZpool: (name) =>
    request(`/host/zpools/${encodeURIComponent(name)}?wipe=true`, { method: 'DELETE' }),
  createHostZvol: (data) => request('/host/zvols', { method: 'POST', body: JSON.stringify(data) }),
  listHostRaid: () => request('/host/raid'),
  createHostRaid: (data) => request('/host/raid', { method: 'POST', body: JSON.stringify(data) }),
  syncHostRaid: (device, action = 'check') => {
    const dev = String(device || '').replace(/^\/dev\//, '');
    return request(`/host/raid/${encodeURIComponent(dev)}/sync`, {
      method: 'POST',
      body: JSON.stringify({ action }),
    });
  },
  stopHostRaid: (device) => {
    const dev = String(device || '').replace(/^\/dev\//, '');
    return request(`/host/raid/${encodeURIComponent(dev)}`, { method: 'DELETE' });
  },
  destroyHostRaid: (device) => {
    const dev = String(device || '').replace(/^\/dev\//, '');
    return request(`/host/raid/${encodeURIComponent(dev)}?destroy=true`, { method: 'DELETE' });
  },
  unmountHostDisk: (data) =>
    request('/host/disks/unmount', { method: 'POST', body: JSON.stringify(data) }),
  getStorageBreakdown: () => request('/storage/pools/breakdown'),
  listFilesystems: () => request('/host/disks/filesystems'),
  listOrphanMounts: () => request('/host/disks/orphan-mounts'),
  getCapabilities: () => request('/host/capabilities'),
  refreshCapabilities: () => request('/host/capabilities/refresh', { method: 'POST' }),
  wipeHostDisk: (diskPath) =>
    request('/host/disks/wipe', { method: 'POST', body: JSON.stringify({ disk_path: diskPath }) }),
  initHostDiskDirectory: (data) =>
    request('/host/disks/initialize-directory', { method: 'POST', body: JSON.stringify(data) }),
  getHostDiskSMART: (diskPath, refresh = false) =>
    request(
      `/host/disks/smart?path=${encodeURIComponent(diskPath)}${refresh ? '&refresh=true' : ''}`
    ),
  runHostDiskSelfTest: (diskPath, type) =>
    request('/host/disks/selftest', {
      method: 'POST',
      body: JSON.stringify({ disk_path: diskPath, type }),
    }),
  runSMARTTest: (diskPath, type) =>
    request('/host/disks/selftest', {
      method: 'POST',
      body: JSON.stringify({ disk_path: diskPath, type }),
    }),
  scrubHostZpool: (name, action = 'start') =>
    request(`/host/zpools/${encodeURIComponent(name)}/scrub`, {
      method: 'POST',
      body: JSON.stringify({ action }),
    }),
  scrubZpool: (name, action = 'start') =>
    request(`/host/zpools/${encodeURIComponent(name)}/scrub`, {
      method: 'POST',
      body: JSON.stringify({ action }),
    }),

  // --- graphics ---
  getRDPUrl: (id, node, hv) => `${BASE}/vms/${id}/rdp${nodeQuery(node, hv)}`,
  getSPICEUrl: (id, node, hv) => `${BASE}/vms/${id}/spice${nodeQuery(node, hv)}`,
  // Console WebSockets for a VM that lives on another node: node/hv ride in
  // the query next to the ticket (the legacy /console/{id} page does not
  // forward them, so the SPA never uses it for remote VMs).
  getVNCWsUrl: (id, ticket, node, hv) => {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${location.host}/api/vms/${encodeURIComponent(id)}/vnc?vt=${encodeURIComponent(ticket || '')}${nodeParams(node, hv)}`;
  },
  getSerialWsUrl: (id, ticket, node, hv) => {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${location.host}/api/vms/${encodeURIComponent(id)}/serial?ticket=${encodeURIComponent(ticket || '')}${nodeParams(node, hv)}`;
  },
  getSPICEWsUrl: (id, ticket) => {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${location.host}/api/vms/${id}/spice-ws?vt=${encodeURIComponent(ticket || '')}`;
  },

  // --- disks ---
  listDisks: (vmId, node) => request(`/vms/${vmId}/disks${nodeQuery(node)}`),
  // node: remote libvirt node of the VM (per-node route, ?node=&hv=kvm).
  createDisk: (vmId, data, node) =>
    request(`/vms/${vmId}/disks${nodeQuery(node, 'kvm')}`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  deleteDisk: (vmId, dev, node, hv) =>
    request(`/vms/${vmId}/disks/${dev}${nodeQuery(node, hv)}`, { method: 'DELETE' }),
  updateDiskSource: (vmId, dev, source) =>
    request(`/vms/${vmId}/disks/${dev}`, { method: 'PUT', body: JSON.stringify({ source }) }),
  resizeVmDisk: (vmId, dev, sizeGb, node, hv) =>
    request(`/vms/${vmId}/disks/${dev}/resize${nodeQuery(node, hv)}`, {
      method: 'POST',
      body: JSON.stringify({ size_gb: Number(sizeGb) }),
    }),
  changeDiskBus: (vmId, dev, bus, node, hv) =>
    request(`/vms/${vmId}/disks/${dev}/bus${nodeQuery(node, hv)}`, {
      method: 'POST',
      body: JSON.stringify({ bus }),
    }),
  attachUSBDevice: (vmId, vendorId, productId) =>
    request(`/vms/${vmId}/usb`, {
      method: 'POST',
      body: JSON.stringify({ vendor_id: vendorId, product_id: productId }),
    }),
  detachUSBDevice: (vmId, vendorId, productId) =>
    request(`/vms/${vmId}/usb/${encodeURIComponent(vendorId)}/${encodeURIComponent(productId)}`, {
      method: 'DELETE',
    }),
  attachPCIDevices: (vmId, addresses) =>
    request(`/vms/${vmId}/pci`, { method: 'POST', body: JSON.stringify({ addresses }) }),
  detachPCIDevice: (vmId, address) =>
    request(`/vms/${vmId}/pci/${encodeURIComponent(address)}`, { method: 'DELETE' }),
  attachSharedFolder: (vmId, hostPath, tag, readOnly) =>
    request(`/vms/${vmId}/shared-folders`, {
      method: 'POST',
      body: JSON.stringify({ host_path: hostPath, tag, read_only: readOnly }),
    }),
  detachSharedFolder: (vmId, tag) =>
    request(`/vms/${vmId}/shared-folders/${encodeURIComponent(tag)}`, { method: 'DELETE' }),

  // --- net ifaces ---
  listNetIfaces: (vmId, node) => request(`/vms/${vmId}/networks${nodeQuery(node)}`),
  createNetIface: (vmId, data, node) =>
    request(`/vms/${vmId}/networks${nodeQuery(node, 'kvm')}`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  updateNetIface: (vmId, mac, data, node, hv) =>
    request(`/vms/${vmId}/networks/${encodeURIComponent(mac)}${nodeQuery(node, hv)}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    }),
  checkVLANSupport: (network) =>
    request(`/vms/_/vlan-support?network=${encodeURIComponent(network)}`),
  deleteNetIface: (vmId, mac, node, hv) =>
    request(`/vms/${vmId}/networks/${encodeURIComponent(mac)}${nodeQuery(node, hv)}`, {
      method: 'DELETE',
    }),

  // --- meta + metrics + cover ---
  getVMMeta: (vmId, node, hv) => request(`/vms/${vmId}/meta${nodeQuery(node, hv)}`),
  updateVMMeta: (vmId, data, node, hv) =>
    request(`/vms/${vmId}/meta${nodeQuery(node, hv)}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  getVMMetrics: (vmId) => request(`/vms/${vmId}/metrics`),

  uploadCover: (vmId, file) => {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const formData = new FormData();
      formData.append('file', file);
      xhr.open('POST', `${BASE}/vms/${vmId}/cover`);
      xhr.withCredentials = true;
      if (csrfState) xhr.setRequestHeader('X-CSRF-Token', csrfState);
      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          try {
            resolve(JSON.parse(xhr.responseText));
          } catch {
            resolve({});
          }
        } else if (xhr.status === 401) {
          auth.onUnauthorized();
          reject(new ApiError('Unauthorized', 401, 'unauthorized'));
        } else {
          let msg = `HTTP ${xhr.status}`;
          try {
            const j = JSON.parse(xhr.responseText);
            if (j.error) msg = j.error;
          } catch {
            /* ignore: non-JSON response */
          }
          reject(new ApiError(msg, xhr.status, 'upload_failed'));
        }
      };
      xhr.onerror = () => reject(new ApiError('Upload failed', 0, 'network'));
      xhr.send(formData);
    });
  },
  deleteCover: (vmId) => request(`/vms/${vmId}/cover`, { method: 'DELETE' }),

  // --- groups ---
  listGroups: () => request('/groups'),
  listTags: () => request('/tags'),
  userUsage: (username) => request(`/users/${encodeURIComponent(username)}/usage`),
  myUsage: () => request('/users/me/usage'),
  createGroup: (data) => request('/groups', { method: 'POST', body: JSON.stringify(data) }),
  updateGroup: (name, data) =>
    request(`/groups/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteGroup: (name) => request(`/groups/${encodeURIComponent(name)}`, { method: 'DELETE' }),

  // --- clone / export / import ---
  cloneVM: (vmId, data) =>
    request(`/vms/${vmId}/clone`, { method: 'POST', body: JSON.stringify(data) }),
  batchCloneVM: (vmId, data) =>
    request(`/vms/${vmId}/batch-clone`, { method: 'POST', body: JSON.stringify(data) }),
  // Move an instance's storage to another pool of the same hypervisor.
  // Cold only, and answered with a job like moveVolume above.
  moveVMStorage: (vmId, pool) =>
    request(`/vms/${vmId}/move-storage`, { method: 'POST', body: JSON.stringify({ pool }) }),

  // --- cloud-init (post-creation reapply) ---
  getCloudInitStatus: (vmId) => request(`/vms/${vmId}/cloudinit`),
  reapplyCloudInit: (vmId, data) =>
    request(`/vms/${vmId}/cloudinit`, { method: 'POST', body: JSON.stringify(data) }),
  getVMLogs: (vmId, lines = 200, node) =>
    request(
      `/vms/${vmId}/logs?lines=${lines}${node && node !== 'local' ? '&node=' + encodeURIComponent(node) : ''}`
    ),
  exportVM: async (id, opts = {}) => {
    const params = new SvelteURLSearchParams();
    if (opts.format) params.set('format', opts.format);
    if (opts.target) params.set('target', opts.target);
    if (opts.compress) params.set('compress', '1');
    const qs = params.toString();
    const url = `${BASE}/vms/${id}/export${qs ? '?' + qs : ''}`;
    const res = await fetch(url, {
      credentials: 'include',
      signal: opts.signal,
    });
    if (res.status === 401) {
      auth.onUnauthorized();
      throw new ApiError('Unauthorized', 401, 'unauthorized');
    }
    if (!res.ok) {
      let msg = `HTTP ${res.status}`;
      try {
        const j = await res.json();
        if (j.error) msg = j.error;
      } catch {
        /* ignore: non-JSON response */
      }
      throw new ApiError(msg, res.status, 'export_failed');
    }
    const disp = res.headers.get('Content-Disposition') || '';
    const m = /filename="?([^"]+)"?/.exec(disp);
    const filename = m ? m[1] : `${id}.ova`;
    const total = parseInt(res.headers.get('Content-Length') || '0', 10);
    const reader = res.body && res.body.getReader ? res.body.getReader() : null;
    const chunks = [];
    let received = 0;
    if (reader) {
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        chunks.push(value);
        received += value.length;
        if (opts.onProgress && total > 0) {
          opts.onProgress({ received, total, percent: (received / total) * 100 });
        } else if (opts.onProgress) {
          opts.onProgress({ received, total: 0, percent: 0 });
        }
      }
    } else {
      const blob = await res.blob();
      chunks.push(blob);
      received = blob.size;
      if (opts.onProgress) opts.onProgress({ received, total: blob.size, percent: 100 });
    }
    const blob = new Blob(chunks);
    const dlUrl = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = dlUrl;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(dlUrl);
    return { filename, size: received };
  },
  importVM: (file, name, pool, onProgress, network) => {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const formData = new FormData();
      formData.append('file', file);
      if (name) formData.append('name', name);
      if (pool) formData.append('pool', pool);
      if (network) formData.append('network', network);
      if (onProgress) {
        xhr.upload.addEventListener('progress', (e) => {
          if (e.lengthComputable) {
            onProgress(Math.round((e.loaded / e.total) * 100));
          }
        });
      }
      xhr.open('POST', `${BASE}/vms/import`);
      xhr.withCredentials = true;
      if (csrfState) xhr.setRequestHeader('X-CSRF-Token', csrfState);
      // Imports can take many minutes for multi-GB archives (upload
      // + extract + libvirt define). 30 minutes covers a 5 GB file
      // on a slow link with margin. Without this, the browser's
      // default XHR timeout (~0 = none) means a hung server would
      // only surface via the generic onerror handler.
      xhr.timeout = 30 * 60 * 1000;
      xhr.onload = () => {
        if (xhr.status === 401) {
          auth.onUnauthorized();
          reject(new ApiError('Unauthorized', 401, 'unauthorized'));
          return;
        }
        if (xhr.status >= 200 && xhr.status < 300) {
          try {
            resolve(JSON.parse(xhr.responseText));
          } catch {
            resolve({ status: 'ok' });
          }
        } else {
          let msg = `HTTP ${xhr.status}`;
          try {
            const j = JSON.parse(xhr.responseText);
            if (j.error) msg = j.error;
          } catch {
            /* ignore: non-JSON response */
          }
          reject(new ApiError(msg, xhr.status, 'import_failed'));
        }
      };
      xhr.ontimeout = () => reject(new ApiError('Import timed out (30 min)', 0, 'timeout'));
      xhr.onerror = () => reject(new ApiError('Network error', 0, 'network'));
      xhr.onabort = () => reject(new ApiError('Import aborted', 0, 'aborted'));
      xhr.send(formData);
    });
  },

  // --- boot ---
  getBootDevice: (vmId) => request(`/vms/${vmId}/boot`),
  setBootDevice: (vmId, device) =>
    request(`/vms/${vmId}/boot`, { method: 'POST', body: JSON.stringify({ device }) }),

  // --- system ---
  systemStatus: () => request('/system/status'),
  systemLogs: async (lines = 200) => {
    const res = await fetch(`${BASE}/system/logs?lines=${lines}`, {
      credentials: 'include',
    });
    if (res.status === 401) {
      auth.onUnauthorized();
      throw new ApiError('Unauthorized', 401, 'unauthorized');
    }
    if (!res.ok) throw new ApiError(`HTTP ${res.status}`, res.status, 'logs_failed');
    return res.text();
  },
  systemRestart: () => request('/system/restart', { method: 'POST' }),
  systemUpdate: () => request('/system/update', { method: 'POST' }),
  systemBackup: () => request('/system/backup', { method: 'POST' }),
  systemBackups: () => request('/system/backups'),

  // --- Settings (config store) ---
  getSettingsSchema: () => request('/settings/schema'),
  getSettings: () => request('/settings'),
  listJailedIPs: () => request('/settings/jail'),
  unbanJailedIP: (ip) =>
    request('/settings/jail/unban', { method: 'POST', body: JSON.stringify({ ip }) }),
  manualBanIP: (body) =>
    request('/settings/jail/ban', { method: 'POST', body: JSON.stringify(body) }),
  getJailWhitelist: () => request('/settings/jail/whitelist'),
  addJailWhitelist: (body) =>
    request('/settings/jail/whitelist', { method: 'POST', body: JSON.stringify(body) }),
  removeJailWhitelist: (cidr) =>
    request('/settings/jail/whitelist', { method: 'DELETE', body: JSON.stringify({ cidr }) }),
  getJailConfig: () => request('/settings/jail/config'),
  updateJailConfig: (cfg) =>
    request('/settings/jail/config', { method: 'PUT', body: JSON.stringify(cfg) }),
  addCustomJail: (body) =>
    request('/settings/jail/custom', { method: 'POST', body: JSON.stringify(body) }),
  deleteCustomJail: (id) =>
    request(`/settings/jail/custom/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  setSettings: (values) =>
    request('/settings', { method: 'PUT', body: JSON.stringify({ values }) }),
  resetSettings: () => request('/settings/reset', { method: 'POST' }),
  getNotifyConfig: () => request('/notify/config'),
  updateNotifyConfig: (body) =>
    request('/notify/config', { method: 'PUT', body: JSON.stringify(body) }),
  testNotify: () => request('/notify/test', { method: 'POST' }),
  listNotifyEvents: () => request('/notify/events'),

  // --- Cloud-Init Studio & Snippets ---
  listCloudInitSnippets: () => request('/cloudinit/snippets'),
  getCloudInitSnippet: (id) => request(`/cloudinit/snippets/${id}`),
  createCloudInitSnippet: (snippet) =>
    request('/cloudinit/snippets', {
      method: 'POST',
      body: JSON.stringify(snippet),
    }),
  updateCloudInitSnippet: (id, snippet) =>
    request(`/cloudinit/snippets/${id}`, {
      method: 'PUT',
      body: JSON.stringify(snippet),
    }),
  deleteCloudInitSnippet: (id) => request(`/cloudinit/snippets/${id}`, { method: 'DELETE' }),
  previewCloudInit: (body) =>
    request('/cloudinit/preview', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  applyLiveSettings: (keys) =>
    request('/settings/apply-live', {
      method: 'POST',
      body: JSON.stringify({ keys }),
    }),
  applyRestart: (keys) =>
    request('/system/apply-restart', {
      method: 'POST',
      body: JSON.stringify({ keys }),
    }),

  // --- API tokens (long-lived) ---
  listTokens: (all = false) => request(`/tokens${all ? '?all=1' : ''}`),
  createToken: (name, ttlHours, scopes = []) =>
    request('/tokens', {
      method: 'POST',
      body: JSON.stringify({ name, ttl_hours: ttlHours, scopes }),
    }),
  revokeToken: (id) => request(`/tokens/${id}/revoke`, { method: 'POST' }),
  deleteToken: (id) => request(`/tokens/${id}`, { method: 'DELETE' }),

  // --- Cluster nodes ---
  listNodes: () => request('/nodes'),
  // Optional Cluster/HA module (never gated by the module flag: this is
  // how the operator installs and enables it).
  getHAModule: () => request('/modules/ha'),
  // opts: { linstor, accept_experimental, fencing: ['sbd'|'pve'|'redfish'|'ipmi'] }.
  // A bare boolean is still accepted as the LINSTOR flag.
  preflightHAModule: (opts) =>
    request('/modules/ha/preflight', {
      method: 'POST',
      body: haModuleOptionsBody(opts),
    }),
  installHAModule: (opts) =>
    request('/modules/ha/install', {
      method: 'POST',
      body: haModuleOptionsBody(opts),
    }),
  getHAModuleCompat: () => request('/modules/ha/compat'),
  verifyHAModule: () => request('/modules/ha/verify', { method: 'POST' }),
  enableHAModule: () => request('/modules/ha/enable', { method: 'POST' }),
  disableHAModule: () => request('/modules/ha/disable', { method: 'POST' }),
  uninstallHAModule: (purge) =>
    request('/modules/ha/uninstall', {
      method: 'POST',
      body: JSON.stringify({ confirm: true, purge: !!purge }),
    }),
  getHAReadiness: () => request('/ha/readiness'),
  verifyFencePVE: () => request('/ha/fence-pve/verify', { method: 'POST' }),
  // HA Phase 2: cluster creation wizard, fencing test, node maintenance.
  haClusterPlan: (body) =>
    request('/modules/ha/cluster/plan', { method: 'POST', body: JSON.stringify(body) }),
  haClusterSetup: (body) =>
    request('/modules/ha/cluster/setup', { method: 'POST', body: JSON.stringify(body) }),
  haClusterStatus: () => request('/modules/ha/cluster/status'),
  haFencingTest: (body) =>
    request('/ha/fencing/test', { method: 'POST', body: JSON.stringify(body) }),
  haFencingEvidence: () => request('/ha/fencing/evidence'),
  haNodeStandby: (name) =>
    request(`/ha/nodes/${encodeURIComponent(name)}/standby`, { method: 'POST' }),
  haNodeUnstandby: (name) =>
    request(`/ha/nodes/${encodeURIComponent(name)}/unstandby`, { method: 'POST' }),
  getIncusClusterMembers: () => request('/cluster/members'),
  getIncusHealing: () => request('/incus/healing'),
  setIncusHealing: (thresholdSeconds) =>
    request('/incus/healing', {
      method: 'PUT',
      body: JSON.stringify({ threshold_seconds: thresholdSeconds }),
    }),
  testNode: (uri, backend = 'libvirt', project = '', trustToken = '') =>
    request('/nodes/test', {
      method: 'POST',
      body: JSON.stringify({ uri, backend, project, trust_token: trustToken }),
    }),
  getNodeIncusInventory: (id) => request(`/nodes/${encodeURIComponent(id)}/incus-inventory`),
  getNodeDependencyInstaller: () => request('/nodes/install-script'),
  installNodeDependencies: (uri, allowSystemUpgrade = false) =>
    request('/nodes/install', {
      method: 'POST',
      body: JSON.stringify({ uri, allow_system_upgrade: allowSystemUpgrade }),
    }),
  getNode: (id) => request(`/nodes/${id}`),
  pingNode: (id) => request(`/nodes/${id}/ping`, { method: 'POST' }),
  getWebPushKey: () => request('/push/vapid-key'),
  subscribeWebPush: (sub) =>
    request('/push/subscribe', { method: 'POST', body: JSON.stringify(sub) }),
  unsubscribeWebPush: (endpoint) =>
    request('/push/subscribe', { method: 'DELETE', body: JSON.stringify({ endpoint }) }),
  testWebPush: (locale) =>
    request('/push/test', { method: 'POST', body: JSON.stringify({ locale }) }),
  setNodeDraining: (id, draining) =>
    request(`/nodes/${encodeURIComponent(id)}/${draining ? 'drain' : 'undrain'}`, {
      method: 'POST',
    }),
  getNodeDrainPlan: (id) => request(`/nodes/${encodeURIComponent(id)}/drain-plan`),
  evacuateNode: (id, body) =>
    request(`/nodes/${encodeURIComponent(id)}/evacuate`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  getNodeEvacuation: (id) => request(`/nodes/${encodeURIComponent(id)}/evacuate`),
  cancelNodeEvacuation: (id) =>
    request(`/nodes/${encodeURIComponent(id)}/evacuate/cancel`, { method: 'POST' }),
  pingAllNodes: () => request('/nodes/ping-all', { method: 'POST' }),
  getClusterSummary: () => request('/nodes/summary'),
  createNode: (name, uri, backend = 'libvirt', project = '', trustToken = '', uiUrl = '') =>
    request('/nodes', {
      method: 'POST',
      body: JSON.stringify({
        name,
        uri,
        backend,
        project,
        trust_token: trustToken,
        ...(uiUrl ? { ui_url: uiUrl } : {}),
      }),
    }),
  updateNode: (id, data) => request(`/nodes/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  // force=true skips the remote Incus certificate revoke that failed
  // (backend answers 502 incus_revoke_failed without it). The client
  // certificate may then remain on the remote server.
  deleteNode: (id, { force = false } = {}) =>
    request(`/nodes/${id}${force ? '?force=true' : ''}`, { method: 'DELETE' }),

  // --- Managed cluster SSH (admin only). WebKVM never pushes the key to a
  // node: the operator runs the authorize command there.
  getClusterSSH: () => request('/cluster/ssh'),
  generateClusterSSHKey: () =>
    request('/cluster/ssh/key', { method: 'POST', body: JSON.stringify({ algorithm: 'ed25519' }) }),
  startClusterSSHRotation: () => request('/cluster/ssh/rotate', { method: 'POST' }),
  // force=true switches even though some node did not pass the test of the
  // new key (ADVANCED only; the backend audits it).
  commitClusterSSHRotation: ({ force = false } = {}) =>
    request('/cluster/ssh/rotate/commit', {
      method: 'POST',
      ...(force ? { body: JSON.stringify({ force: true }) } : {}),
    }),
  // Tests the PENDING key against every enabled libvirt node without
  // touching the active connections.
  testClusterSSHRotation: () => request('/cluster/ssh/rotate/test', { method: 'POST' }),
  rollbackClusterSSHRotation: () => request('/cluster/ssh/rotate/rollback', { method: 'POST' }),
  abortClusterSSHRotation: () => request('/cluster/ssh/rotate/abort', { method: 'POST' }),
  getNodeSSHAuthorize: (id) => request(`/nodes/${encodeURIComponent(id)}/ssh-authorize`),
  captureNodeHostKey: (id) =>
    request(`/nodes/${encodeURIComponent(id)}/ssh/hostkey`, { method: 'POST' }),
  pinNodeHostKey: (id, fingerprint, replace = false) =>
    request(`/nodes/${encodeURIComponent(id)}/ssh/pin`, {
      method: 'POST',
      body: JSON.stringify({ fingerprint, replace: !!replace }),
    }),
  testNodeSSH: (id) => request(`/nodes/${encodeURIComponent(id)}/ssh/test`, { method: 'POST' }),

  // --- HA workloads (Cluster/HA module only; admin only). `kind` is
  // kvm|incus; `project` is only sent for Incus.
  listHAWorkloads: () => request('/ha/workloads'),
  getHAWorkload: (kind, name, project) => request(haWorkloadPath(kind, name, project)),
  enableHAWorkload: (kind, name, project, body = {}) =>
    request(haWorkloadPath(kind, name, project, '/enable'), {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  // opts: { mode: stop|pause|forget, confirm, acceptUnrestorable }.
  disableHAWorkload: (
    kind,
    name,
    project,
    { mode = 'stop', confirm = '', acceptUnrestorable = false } = {}
  ) =>
    request(
      haWorkloadPath(kind, name, project, '', {
        mode,
        ...(confirm ? { confirm } : {}),
        ...(acceptUnrestorable ? { accept_unrestorable: 'true' } : {}),
      }),
      { method: 'DELETE' }
    ),
  // Released KVM VMs (HA turned off with stop/forget): inventory + restore.
  listReleasedVMs: () => request('/ha/released'),
  // opts: { confirm (the VM name), start, node }.
  restoreReleasedVM: (name, { confirm = '', start = false, node = '' } = {}) =>
    request(`/ha/released/${encodeURIComponent(name)}/restore`, {
      method: 'POST',
      body: JSON.stringify({ confirm, start: !!start, ...(node ? { node } : {}) }),
    }),
  discardReleasedVM: (name, confirm) =>
    request(
      `/ha/released/${encodeURIComponent(name)}?confirm=${encodeURIComponent(confirm || '')}`,
      { method: 'DELETE' }
    ),
  placeHAWorkload: (kind, name, project, body) =>
    request(haWorkloadPath(kind, name, project, '/placement'), {
      method: 'PUT',
      body: JSON.stringify(body),
    }),
  moveHAWorkload: (kind, name, project, node) =>
    request(haWorkloadPath(kind, name, project, '/move'), {
      method: 'POST',
      body: JSON.stringify({ node }),
    }),
  clearHAWorkloadFailcount: (kind, name, project) =>
    request(haWorkloadPath(kind, name, project, '/clear-failcount'), {
      method: 'POST',
    }),
  maintenanceHAWorkload: (kind, name, project, action, ttlSeconds) =>
    request(haWorkloadPath(kind, name, project, '/maintenance'), {
      method: 'POST',
      body: JSON.stringify(ttlSeconds ? { action, ttl_s: ttlSeconds } : { action }),
    }),
  // body: { action: enable|disable, items: [{kind,name,project}], dry_run, mode } (max 50).
  bulkHAWorkloads: (body) =>
    request('/ha/workloads/bulk', { method: 'POST', body: JSON.stringify(body) }),

  // --- audit log (admin only) ---
  listAudit: ({ limit, offset, q, user, action } = {}) => {
    const params = new SvelteURLSearchParams();
    if (limit) params.set('limit', limit);
    if (offset) params.set('offset', offset);
    if (q) params.set('q', q);
    if (user) params.set('user', user);
    if (action) params.set('action', action);
    const qs = params.toString();
    return request(`/audit${qs ? '?' + qs : ''}`);
  },

  // --- cross-fleet snapshots ---
  listAllSnapshots: () => request('/vms/snapshots'),

  // --- Backup v2 ---
  listBackupTargets: () => request('/backup/targets'),
  createBackupTarget: (data) =>
    request('/backup/targets', { method: 'POST', body: JSON.stringify(data) }),
  updateBackupTarget: (id, data) =>
    request(`/backup/targets/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteBackupTarget: (id) => request(`/backup/targets/${id}`, { method: 'DELETE' }),
  testBackupTarget: (data) =>
    request('/backup/targets/test', { method: 'POST', body: JSON.stringify(data) }),
  backupNow: (id) => request(`/backup/targets/${id}/run`, { method: 'POST' }),
  listBackupsOnTarget: (id) => request(`/backup/targets/${id}/files`),
  deleteBackupFile: (id, filename) =>
    request(`/backup/targets/${id}/files/${encodeURIComponent(filename)}`, { method: 'DELETE' }),
  deleteBackupRun: (id, suffix) =>
    request(`/backup/targets/${id}/runs/${encodeURIComponent(suffix)}`, { method: 'DELETE' }),
  deleteBackupConfig: (id) => request(`/backup/targets/${id}/config`, { method: 'DELETE' }),
  restoreBackupConfig: (id) =>
    request(`/backup/targets/${id}/restore`, {
      method: 'POST',
      body: JSON.stringify({ config: true }),
    }),
  verifyBackup: (id, filename) =>
    request(`/backup/targets/${id}/verify?filename=${encodeURIComponent(filename)}`),
  restoreBackup: (id, filename) =>
    request(`/backup/targets/${id}/restore`, {
      method: 'POST',
      body: JSON.stringify({ filename }),
    }),
  restoreBackupRun: (id, run) =>
    request(`/backup/targets/${id}/restore`, {
      method: 'POST',
      body: JSON.stringify({ run }),
    }),
  // --- Host firewall (V13-C-01 / V13-C-02) ---
  getHostFirewall: () => request('/firewall/host'),
  previewHostFirewall: (firewall) =>
    request('/firewall/host/preview', { method: 'POST', body: JSON.stringify({ firewall }) }),
  applyHostFirewall: (firewall) =>
    request('/firewall/host/apply', { method: 'POST', body: JSON.stringify({ firewall }) }),
  confirmHostFirewall: () => request('/firewall/host/confirm', { method: 'POST' }),
  rollbackHostFirewall: () => request('/firewall/host/rollback', { method: 'POST' }),
  // --- Metric history + alerts (V13-C-03 / V13-C-04) ---
  getVMMetricsHistory: (id, window) => request(`/vms/${id}/metrics/history?window=${window}`),
  getVMAlerterRules: (id) => request(`/vms/${id}/alerts`),
  setVMAlerterRules: (id, rules) =>
    request(`/vms/${id}/alerts`, { method: 'PUT', body: JSON.stringify({ rules }) }),
  listActiveAlerts: () => request('/alerts/active'),
  listIncidents: () => request('/alerts/incidents'),
  clearResolvedIncidents: () => request('/alerts/incidents/resolved', { method: 'DELETE' }),
  // --- Dashboard (V13-D-04) ---
  hostMetrics: () => request('/host/metrics'),
  importHostFirewall: (firewall) =>
    request('/firewall/host/import', { method: 'POST', body: JSON.stringify(firewall) }),
  getClusterFirewall: () => request('/firewall/cluster'),
  saveClusterFirewall: (profile) =>
    request('/firewall/cluster', { method: 'PUT', body: JSON.stringify({ profile }) }),
  previewClusterFirewall: (profile) =>
    request('/firewall/cluster/preview', { method: 'POST', body: JSON.stringify({ profile }) }),
  applyClusterFirewall: (profile) =>
    request('/firewall/cluster/apply', { method: 'POST', body: JSON.stringify({ profile }) }),
  confirmClusterFirewall: () => request('/firewall/cluster/confirm', { method: 'POST' }),
  rollbackClusterFirewall: () => request('/firewall/cluster/rollback', { method: 'POST' }),
  exportHostFirewall: () => request('/firewall/host/export'),
  // restoreAsVM is the operator-friendly restore: it takes the
  // backup archive already on disk in the target's path and
  // creates a new VM in libvirt from it (no re-upload round-
  // trip). Equivalent to /api/vms/import but reading from a
  // local path instead of multipart.
  restoreAsVM: (id, payload) =>
    request(`/backup/targets/${id}/restore-as-vm`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  listBackupSchedules: () => request('/backup/schedules'),
  createBackupSchedule: (data) =>
    request('/backup/schedules', { method: 'POST', body: JSON.stringify(data) }),
  updateBackupSchedule: (id, data) =>
    request(`/backup/schedules/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteBackupSchedule: (id) => request(`/backup/schedules/${id}`, { method: 'DELETE' }),
  listBackupJobs: () => request('/backup/jobs'),
  listBackupChains: (id) => request(`/backup/targets/${id}/chains`),
  deleteBackupChain: (id, vmID) =>
    request(`/backup/targets/${id}/chains/${encodeURIComponent(vmID)}`, { method: 'DELETE' }),

  // --- generic helpers ---
  // post sends a JSON POST to an absolute path (caller must include the
  // '/api' prefix). Used by callers that need query strings or dynamic
  // paths not worth a dedicated named method.
  post: (path, body) => request(path, { method: 'POST', body: JSON.stringify(body ?? {}) }),
};

// passwordStrength returns { score: 0-4, labelKey, label, color } based on a
// simple heuristic. Used by the Login + Account + Users forms.
export function passwordStrength(pw) {
  if (!pw) return { score: 0, labelKey: '', label: '—', color: 'bg-muted' };
  let score = 0;
  if (pw.length >= 8) score++;
  if (pw.length >= 12) score++;
  if (/[A-Z]/.test(pw) && /[a-z]/.test(pw)) score++;
  if (/\d/.test(pw)) score++;
  if (/[^A-Za-z0-9]/.test(pw)) score++;
  if (score > 4) score = 4;
  const labelKeys = [
    'account.strengthVeryWeak',
    'account.strengthWeak',
    'account.strengthFair',
    'account.strengthGood',
    'account.strengthStrong',
  ];
  const labels = ['Very weak', 'Weak', 'Fair', 'Good', 'Strong'];
  const colors = ['bg-destructive', 'bg-destructive', 'bg-warning', 'bg-info', 'bg-success'];
  return { score, labelKey: labelKeys[score], label: labels[score], color: colors[score] };
}
