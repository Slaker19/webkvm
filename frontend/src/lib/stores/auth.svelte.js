let tokenState = $state(localStorage.getItem('token') || '');
let userState = $state(localStorage.getItem('user') || '');
let roleState = $state(localStorage.getItem('role') || '');
let mustChangeState = $state(localStorage.getItem('must_change') === '1');

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
    // page with a dead token.
    location.href = '/login' + (reason ? '?reason=' + reason : '');
  }
}

export const auth = {
  get token() {
    return tokenState;
  },
  get user() {
    return userState;
  },
  get role() {
    return roleState;
  },
  get isLoggedIn() {
    return !!tokenState;
  },
  get mustChangePassword() {
    return mustChangeState;
  },

  setToken(t, u, r, mustChange = false) {
    tokenState = t;
    userState = u;
    roleState = r || '';
    mustChangeState = !!mustChange;
    localStorage.setItem('token', t);
    localStorage.setItem('user', u);
    localStorage.setItem('role', r || '');
    localStorage.setItem('must_change', mustChange ? '1' : '0');
  },

  setMustChange(v) {
    mustChangeState = !!v;
    localStorage.setItem('must_change', v ? '1' : '0');
  },

  logout() {
    tokenState = '';
    userState = '';
    roleState = '';
    mustChangeState = false;
    localStorage.removeItem('token');
    localStorage.removeItem('user');
    localStorage.removeItem('role');
    localStorage.removeItem('must_change');
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
  constructor(message, status, code) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function request(path, opts = {}) {
  const headers = { 'Content-Type': 'application/json', ...opts.headers };
  if (tokenState) headers['Authorization'] = `Bearer ${tokenState}`;

  const res = await fetch(`${BASE}${path}`, { ...opts, headers });

  if (res.status === 401) {
    auth.logout();
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
    redirectToLogin('session_expired');
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
      throw new ApiError(data.error || 'Too many requests', 429, 'rate_limited', {
        retryAfter: retry,
      });
    }
    if (res.status === 403) {
      throw new ApiError(data.error || 'Forbidden', 403, 'forbidden');
    }
    throw new ApiError(
      data.error || `Request failed (${res.status})`,
      res.status,
      'request_failed'
    );
  }
  return data;
}

export const api = {
  // --- auth ---
  login: (username, password) =>
    request('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  logoutApi: () => request('/auth/logout', { method: 'POST' }),
  refresh: () => request('/auth/refresh', { method: 'POST' }),
  me: () => request('/auth/me'),
  changeMyPassword: (old_password, new_password) =>
    request('/users/me/password', {
      method: 'PUT',
      body: JSON.stringify({ old_password, new_password }),
    }),

  // --- VMs ---
  listVMs: () => request('/vms'),
  getVM: (id) => request(`/vms/${id}`),
  createVM: (data) => request('/vms', { method: 'POST', body: JSON.stringify(data) }),
  updateVM: (id, data) => request(`/vms/${id}`, { method: 'PATCH', body: JSON.stringify(data) }),
  deleteVM: (id, withDisks = false) =>
    request(`/vms/${id}${withDisks ? '?disks=true' : ''}`, { method: 'DELETE' }),
  startVM: (id) => request(`/vms/${id}/start`, { method: 'POST' }),
  shutdownVM: (id) => request(`/vms/${id}/shutdown`, { method: 'POST' }),
  forceOffVM: (id) => request(`/vms/${id}/forceoff`, { method: 'POST' }),
  rebootVM: (id) => request(`/vms/${id}/reboot`, { method: 'POST' }),
  suspendVM: (id) => request(`/vms/${id}/suspend`, { method: 'POST' }),
  resetVMPassword: (id) => request(`/vms/${id}/reset-password`, { method: 'POST' }),
  resumeVM: (id) => request(`/vms/${id}/resume`, { method: 'POST' }),
  // Autostart toggles libvirtd's per-VM autostart flag (not the
  // host's "auto-start VMs at boot" master switch, which is
  // controlled separately by systemd/libvirtd config). The flag
  // is also returned as a field on the VM object so the UI can
  // initialize the switch without a second round-trip; this
  // setter is the only way to change it.
  setVMAutostart: (id, enabled) =>
    request(`/vms/${id}/autostart`, { method: 'POST', body: JSON.stringify({ enabled }) }),
  listSnapshots: (vmId) => request(`/vms/${vmId}/snapshots`),
  createSnapshot: (vmId, data) =>
    request(`/vms/${vmId}/snapshots`, { method: 'POST', body: JSON.stringify(data) }),
  deleteSnapshot: (vmId, sid) => request(`/vms/${vmId}/snapshots/${sid}`, { method: 'DELETE' }),
  revertSnapshot: (vmId, sid) =>
    request(`/vms/${vmId}/snapshots/${sid}/revert`, { method: 'POST' }),
  getVMFirewall: (vmId) => request(`/vms/${vmId}/firewall`),
  setVMFirewall: (vmId, data) =>
    request(`/vms/${vmId}/firewall`, { method: 'PUT', body: JSON.stringify(data) }),
  makeVMTemplate: (vmId) => request(`/vms/${vmId}/make-template`, { method: 'POST' }),
  unsetVMTemplate: (vmId) => request(`/vms/${vmId}/unset-template`, { method: 'POST' }),
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
  getConsoleTicket: (vmId) =>
    request(`/vms/${encodeURIComponent(vmId)}/console-ticket`, { method: 'POST' }),
  getHostTerminalTicket: () => request('/host/terminal-ticket', { method: 'POST' }),
  getVMSchedule: (vmId) => request(`/vms/${vmId}/schedule`),
  setVMSchedule: (vmId, data) =>
    request(`/vms/${vmId}/schedule`, { method: 'PUT', body: JSON.stringify(data) }),
  powerVMNow: (vmId, action) => request(`/vms/${vmId}/power/${action}`, { method: 'POST' }),

  // --- storage ---
  listPools: () => request('/storage/pools'),
  createPool: (data) => request('/storage/pools', { method: 'POST', body: JSON.stringify(data) }),
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
  listVolumes: (pool) => request(`/storage/volumes?pool=${pool}`),
  createVolume: (data) =>
    request('/storage/volumes', { method: 'POST', body: JSON.stringify(data) }),
  resizeVolume: (pool, name, capacity) =>
    request(`/storage/volumes/${pool}/${name}`, {
      method: 'PATCH',
      body: JSON.stringify({ capacity }),
    }),
  deleteVolume: (pool, name) => request(`/storage/volumes/${pool}/${name}`, { method: 'DELETE' }),
  listISOs: (pool) => request(`/storage/isos${pool ? '?pool=' + encodeURIComponent(pool) : ''}`),
  deleteISO: (name, pool = 'ISOS') =>
    request(`/storage/isos/${encodeURIComponent(pool)}/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    }),
  renameISO: (name, newName, pool = 'ISOS') =>
    request(`/storage/isos/${encodeURIComponent(pool)}/${encodeURIComponent(name)}`, {
      method: 'PATCH',
      body: JSON.stringify({ new_name: newName }),
    }),
  downloadISO: (url, name, pool = 'ISOS') =>
    request('/storage/download-iso', { method: 'POST', body: JSON.stringify({ url, name, pool }) }),

  uploadISO: (file, onProgress, pool = 'ISOS') => {
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
          auth.logout();
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
      if (tokenState) xhr.setRequestHeader('Authorization', `Bearer ${tokenState}`);
      xhr.send(formData);
    });
  },

  getDownloadJob: (jobId) => request(`/storage/jobs/${jobId}`),

  // --- users ---
  listUsers: () => request('/users'),
  createUser: (data) => request('/users', { method: 'POST', body: JSON.stringify(data) }),
  updateUser: (username, data) =>
    request(`/users/${username}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteUser: (username) => request(`/users/${username}`, { method: 'DELETE' }),

  // --- networks ---
  listNetworks: () => request('/networks'),
  createNetwork: (data) => request('/networks', { method: 'POST', body: JSON.stringify(data) }),
  updateNetwork: (id, data) =>
    request(`/networks/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteNetwork: (id) => request(`/networks/${id}`, { method: 'DELETE' }),
  startNetwork: (id) => request(`/networks/${id}/start`, { method: 'POST' }),
  stopNetwork: (id) => request(`/networks/${id}/stop`, { method: 'POST' }),

  // --- host ---
  getHostInfo: () => request('/host'),
  getHostStats: () => request('/host/stats'),
  listHostInterfaces: () => request('/host/interfaces'),
  listHostBridges: () => request('/host/bridges'),
  createHostBridge: (data) =>
    request('/host/bridges', { method: 'POST', body: JSON.stringify(data) }),
  deleteHostBridge: (name) =>
    request(`/host/bridges/${encodeURIComponent(name)}`, { method: 'DELETE' }),

  // --- graphics ---
  getGraphics: (id) => request(`/vms/${id}/graphics`),
  getVNCEndpoint: (id) => `${BASE}/vms/${id}/vnc`,
  getRDPUrl: (id) => `${BASE}/vms/${id}/rdp`,
  getSPICEUrl: (id) => `${BASE}/vms/${id}/spice`,

  // --- disks ---
  listDisks: (vmId) => request(`/vms/${vmId}/disks`),
  createDisk: (vmId, data) =>
    request(`/vms/${vmId}/disks`, { method: 'POST', body: JSON.stringify(data) }),
  deleteDisk: (vmId, dev) => request(`/vms/${vmId}/disks/${dev}`, { method: 'DELETE' }),
  updateDiskSource: (vmId, dev, source) =>
    request(`/vms/${vmId}/disks/${dev}`, { method: 'PUT', body: JSON.stringify({ source }) }),

  // --- net ifaces ---
  listNetIfaces: (vmId) => request(`/vms/${vmId}/networks`),
  createNetIface: (vmId, data) =>
    request(`/vms/${vmId}/networks`, { method: 'POST', body: JSON.stringify(data) }),
  updateNetIface: (vmId, mac, data) =>
    request(`/vms/${vmId}/networks/${encodeURIComponent(mac)}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    }),
  checkVLANSupport: (network) =>
    request(`/vms/_/vlan-support?network=${encodeURIComponent(network)}`),
  deleteNetIface: (vmId, mac) =>
    request(`/vms/${vmId}/networks/${encodeURIComponent(mac)}`, { method: 'DELETE' }),

  // --- meta + metrics + cover ---
  getVMMeta: (vmId) => request(`/vms/${vmId}/meta`),
  updateVMMeta: (vmId, data) =>
    request(`/vms/${vmId}/meta`, { method: 'PUT', body: JSON.stringify(data) }),
  getVMMetrics: (vmId) => request(`/vms/${vmId}/metrics`),

  uploadCover: (vmId, file) => {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const formData = new FormData();
      formData.append('file', file);
      xhr.open('POST', `${BASE}/vms/${vmId}/cover`);
      xhr.setRequestHeader('Authorization', `Bearer ${tokenState}`);
      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          try {
            resolve(JSON.parse(xhr.responseText));
          } catch {
            resolve({});
          }
        } else if (xhr.status === 401) {
          auth.logout();
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
  createGroup: (data) => request('/groups', { method: 'POST', body: JSON.stringify(data) }),
  updateGroup: (name, data) =>
    request(`/groups/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteGroup: (name) => request(`/groups/${encodeURIComponent(name)}`, { method: 'DELETE' }),

  // --- clone / export / import ---
  cloneVM: (vmId, data) =>
    request(`/vms/${vmId}/clone`, { method: 'POST', body: JSON.stringify(data) }),
  exportVM: async (id, opts = {}) => {
    const params = new URLSearchParams();
    if (opts.format) params.set('format', opts.format);
    if (opts.target) params.set('target', opts.target);
    if (opts.compress) params.set('compress', '1');
    const qs = params.toString();
    const url = `${BASE}/vms/${id}/export${qs ? '?' + qs : ''}`;
    const res = await fetch(url, {
      headers: { Authorization: `Bearer ${auth.token}` },
      signal: opts.signal,
    });
    if (res.status === 401) {
      auth.logout();
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
  importVM: (file, name, pool, onProgress) => {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const formData = new FormData();
      formData.append('file', file);
      if (name) formData.append('name', name);
      if (pool) formData.append('pool', pool);
      if (onProgress) {
        xhr.upload.addEventListener('progress', (e) => {
          if (e.lengthComputable) {
            onProgress(Math.round((e.loaded / e.total) * 100));
          }
        });
      }
      xhr.open('POST', `${BASE}/vms/import`);
      xhr.setRequestHeader('Authorization', `Bearer ${auth.token}`);
      // Imports can take many minutes for multi-GB archives (upload
      // + extract + libvirt define). 30 minutes covers a 5 GB file
      // on a slow link with margin. Without this, the browser's
      // default XHR timeout (~0 = none) means a hung server would
      // only surface via the generic onerror handler.
      xhr.timeout = 30 * 60 * 1000;
      xhr.onload = () => {
        if (xhr.status === 401) {
          auth.logout();
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
      headers: { Authorization: `Bearer ${tokenState}` },
    });
    if (res.status === 401) {
      auth.logout();
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
  setSettings: (values) =>
    request('/settings', { method: 'PUT', body: JSON.stringify({ values }) }),
  resetSettings: () => request('/settings/reset', { method: 'POST' }),
  getNotifyConfig: () => request('/notify/config'),
  updateNotifyConfig: (body) =>
    request('/notify/config', { method: 'PUT', body: JSON.stringify(body) }),
  testNotify: () => request('/notify/test', { method: 'POST' }),
  listNotifyEvents: () => request('/notify/events'),
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

  // --- Nodes (libvirt hosts) ---
  listNodes: () => request('/nodes'),
  getNode: (id) => request(`/nodes/${id}`),
  createNode: (name, uri) =>
    request('/nodes', { method: 'POST', body: JSON.stringify({ name, uri }) }),
  updateNode: (id, data) => request(`/nodes/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteNode: (id) => request(`/nodes/${id}`, { method: 'DELETE' }),

  // --- Host bridges (extended) ---
  setHostBridgeVLanAware: (name, enabled) =>
    request(`/host/bridges/${name}/vlan_aware`, {
      method: 'POST',
      body: JSON.stringify({ enabled }),
    }),

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
};

// passwordStrength returns { score: 0-4, label, color } based on a
// simple heuristic. Used by the Login + Account + Users forms.
export function passwordStrength(pw) {
  if (!pw) return { score: 0, label: '—', color: 'bg-muted' };
  let score = 0;
  if (pw.length >= 8) score++;
  if (pw.length >= 12) score++;
  if (/[A-Z]/.test(pw) && /[a-z]/.test(pw)) score++;
  if (/\d/.test(pw)) score++;
  if (/[^A-Za-z0-9]/.test(pw)) score++;
  if (score > 4) score = 4;
  const labels = ['Very weak', 'Weak', 'Fair', 'Good', 'Strong'];
  const colors = ['bg-destructive', 'bg-destructive', 'bg-warning', 'bg-info', 'bg-success'];
  return { score, label: labels[score], color: colors[score] };
}
