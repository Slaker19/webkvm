import { t } from './i18n.svelte.js';

const API_ERROR_KEYS = {
  invalid_password: 'apiErrors.invalidPassword',
  invalid_2fa_code: 'apiErrors.invalid2FACode',
  invalid_totp_code: 'apiErrors.invalidTOTPCode',
  quota_exceeded_after_import: 'apiErrors.quotaExceededAfterImport',
  quota_exceeded_after_materialization: 'apiErrors.quotaExceededAfterMaterialization',
  system_media_protected: 'apiErrors.systemMediaProtected',
  pacemaker_managed: 'apiErrors.pacemakerManaged',
  name_conflict: 'apiErrors.nodeExists',
  invalid_name: 'apiErrors.invalidName',
  local_node: 'apiErrors.localNode',
  install_in_progress: 'apiErrors.installInProgress',
  reserved_name: 'apiErrors.reservedName',
  invalid_uri: 'apiErrors.invalidUri',
  invalid_ui_url: 'apiErrors.invalidUiUrl',
  incus_revoke_failed: 'apiErrors.incusRevokeFailed',
  incus_healing_blocked: 'apiErrors.incusHealingBlocked',
  // RAM rule (backend/internal/api/vm_ram.go, utils/ramValidation.js).
  ram_below_minimum: 'apiErrors.ramBelowMinimum',
  ram_below_floor: 'apiErrors.ramBelowFloor',
  min_ram_above_ram: 'apiErrors.minRamAboveRam',
};

// Typed errors of the remote-node / Incus-cluster surface: translated
// everywhere so a raw backend sentence never reaches the user.
const REMOTE_CODES = new Set([
  'node_unreachable',
  'node_timeout',
  'ssh_auth_denied',
  'host_key_unpinned',
  'host_key_changed',
  'node_disabled',
  'transport_unsupported',
  'no_key',
  'libvirt_unavailable',
  'remote_unsupported',
  'invalid_hv',
  'vm_not_running',
  'ticket_scope',
  'console_busy',
  'console_forward_denied',
  'incus_only',
  'member_not_found',
  'member_offline',
  'needs_stop',
  'same_member',
  'not_clustered',
  'invalid_bridge',
  // Create-on-node media (ISO / existing disk from the node's own pools).
  'cloud_init_remote_unsupported',
  'invalid_media_name',
  'media_not_found',
  'media_ambiguous',
  'existing_disk_not_found',
  'existing_disk_pool_invalid',
  'node_storage_unavailable',
  // Volume listing of one pool (inactive / missing / unreadable).
  'pool_inactive',
  'pool_not_found',
  'volumes_unavailable',
]);

export function localizedApiErrorMessage(data, fallback = 'Request failed') {
  if (REMOTE_CODES.has(data?.code)) {
    const key = `remote.errors.${data.code}`;
    const out = t(key);
    if (out !== key) return out;
  }
  const key = Object.hasOwn(API_ERROR_KEYS, data?.code) ? API_ERROR_KEYS[data.code] : null;
  return key ? t(key) : data?.error || fallback;
}
