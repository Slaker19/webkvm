/**
 * User capability model, shared by the create and edit user forms.
 *
 * Kept out of the Svelte component so the rules that decide what a user
 * can actually do are unit-testable without a DOM, and so both forms
 * cannot drift apart: they were previously two hand-maintained copies of
 * the same seven checkboxes with subtly different defaults.
 */

/** Roles, ordered from least to most privileged. */
export const ROLES = ['viewer', 'operator', 'admin'];

/**
 * The capabilities, in the order they are presented.
 *
 * `key` matches the backend's UserPermissions JSON field, minus the
 * `can_` prefix. `roles` lists the roles for which the switch does
 * anything at all: the backend treats the role as a ceiling, so offering
 * a viewer a "create VM" toggle promises something it will never honour.
 */
export const CAPABILITIES = [
  { key: 'create_vm', icon: 'plus', roles: ['operator'] },
  { key: 'delete_vm', icon: 'trash', roles: ['operator'] },
  { key: 'control_power', icon: 'power', roles: ['operator'] },
  { key: 'console', icon: 'monitor', roles: ['operator', 'viewer'] },
  { key: 'snapshots', icon: 'camera', roles: ['operator'] },
  { key: 'backups', icon: 'archive', roles: ['operator'] },
  { key: 'media', icon: 'image', roles: ['operator'] },
  { key: 'export', icon: 'download', roles: ['operator'] },
];

/** Capability keys that apply to a given role. */
export function capabilitiesForRole(role) {
  return CAPABILITIES.filter((c) => c.roles.includes(role));
}

/**
 * Whether a role would hold a capability with no explicit override.
 *
 * Mirrors User.HasPermission in the backend: admins hold everything,
 * viewers hold nothing (console is grantable but off by default), and
 * operators default to allowed.
 */
export function defaultFor(role, key) {
  if (role === 'admin') return true;
  if (role === 'viewer') return false;
  return CAPABILITIES.some((c) => c.key === key && c.roles.includes(role));
}

/**
 * Read a user's effective capabilities into a flat map.
 *
 * A capability absent from `permissions` is inherited from the role
 * rather than assumed true: the previous code defaulted `console` to
 * true for every role, so the edit form showed viewers holding a
 * permission the backend denies them.
 */
export function effectiveCapabilities(user) {
  const role = user?.role || 'operator';
  const perms = user?.permissions || {};
  const out = {};
  for (const cap of CAPABILITIES) {
    const explicit = perms[`can_${cap.key}`];
    out[cap.key] = typeof explicit === 'boolean' ? explicit : defaultFor(role, cap.key);
  }
  return out;
}

/**
 * Build the permissions payload for the API.
 *
 * Returns undefined for admins: they hold everything unconditionally, so
 * sending a permission set would persist flags that are never consulted
 * and would resurface if the account were later demoted.
 *
 * Capabilities that do not apply to the role are omitted rather than
 * sent as false, so that promoting a viewer to operator restores the
 * operator defaults instead of leaving them silently stripped.
 */
export function buildPermissionsPayload(role, caps) {
  if (role === 'admin') return undefined;
  const out = {};
  for (const cap of capabilitiesForRole(role)) {
    out[`can_${cap.key}`] = !!caps[cap.key];
  }
  return out;
}

/**
 * Presets, expressed as the capabilities they grant. Anything not listed
 * is denied. `full` is special-cased to mean "everything for this role".
 */
export const PRESETS = {
  full: null,
  support: ['control_power', 'console', 'snapshots'],
  readonly: [],
};

/** Apply a preset, returning a fresh capability map for the role. */
export function applyPreset(role, preset) {
  const allowed = capabilitiesForRole(role);
  const granted = PRESETS[preset];
  const out = {};
  for (const cap of allowed) {
    out[cap.key] = granted === null ? true : granted.includes(cap.key);
  }
  return out;
}

/** Which preset a capability map corresponds to, or null for a custom set. */
export function matchPreset(role, caps) {
  for (const name of Object.keys(PRESETS)) {
    const candidate = applyPreset(role, name);
    const keys = Object.keys(candidate);
    if (keys.every((k) => !!caps[k] === !!candidate[k])) return name;
  }
  return null;
}

/**
 * Username rules, mirroring validateUsername on the backend.
 *
 * Duplicated deliberately: the backend is the authority, but a form that
 * only discovers a bad name after a round-trip makes the admin fill in
 * four tabs before being told the first field was wrong.
 */
const USERNAME_RE = /^[a-zA-Z0-9](?:[a-zA-Z0-9._-]*[a-zA-Z0-9])?$/;

export function validateUsername(name) {
  if (!name) return 'required';
  if (name !== name.trim()) return 'whitespace';
  if (name.length > 64) return 'tooLong';
  if (!USERNAME_RE.test(name)) return 'charset';
  return null;
}

/**
 * Password rules, mirroring validatePasswordStrength on the backend:
 * 8 characters minimum, and under 16 characters at least three of
 * lowercase / uppercase / digit / symbol.
 */
export function validatePassword(pw) {
  if (!pw) return 'required';
  if (pw.length < 8) return 'tooShort';
  if (pw.length > 128) return 'tooLong';
  if (pw.length < 16) {
    let classes = 0;
    if (/[a-z]/.test(pw)) classes++;
    if (/[A-Z]/.test(pw)) classes++;
    if (/[0-9]/.test(pw)) classes++;
    if (/[^a-zA-Z0-9]/.test(pw)) classes++;
    if (classes < 3) return 'complexity';
  }
  return null;
}

/** Percentage of a quota dimension in use, capped at 100. 0 = unlimited. */
export function quotaPercent(used, limit) {
  if (!limit || limit <= 0) return 0;
  return Math.min(100, Math.round((used / limit) * 100));
}

/**
 * Whether a proposed limit is below what the user already consumes.
 * Setting such a limit does not free resources; it only blocks the next
 * request, so the form warns instead of silently accepting it.
 */
export function limitBelowUsage(used, limit) {
  return !!limit && limit > 0 && used > limit;
}
