export const SYSTEM_RESERVED_USERS = new Set([
  'root',
  'daemon',
  'bin',
  'sys',
  'sync',
  'games',
  'man',
  'lp',
  'mail',
  'news',
  'uucp',
  'proxy',
  'www-data',
  'backup',
  'list',
  'irc',
  '_apt',
  'nobody',
  'systemd-network',
  'systemd-timesync',
  'dhcpcd',
  'messagebus',
  'syslog',
  'systemd-resolve',
  'uuidd',
  'tss',
  'sshd',
  'pollinate',
  'tcpdump',
  'landscape',
  'fwupd-refresh',
  'polkitd',
  'sudo',
  'adm',
  'admin',
]);

/**
 * Checks whether a given username is a reserved Linux system group/user.
 * @param {string} username
 * @returns {boolean}
 */
export function isSystemReservedUser(username) {
  if (!username) return false;
  return SYSTEM_RESERVED_USERS.has(username.trim().toLowerCase());
}

/**
 * Validates cloud-init credentials and returns an i18n translation key, or '' if valid.
 *
 * @param {Object} options
 * @param {boolean} options.enabled - whether cloud-init is active
 * @param {'vm'|'container'|'cloudinit'|string} options.instanceType
 * @param {string} [options.user]
 * @param {string} [options.password]
 * @returns {string}
 */
export function getCloudInitErrorKey({ enabled, instanceType, user, password }) {
  if (!enabled && instanceType !== 'cloudinit') {
    return '';
  }

  const trimmedUser = (user || '').trim();
  const pwd = password || '';
  const isContainer = instanceType === 'container';
  const isCloudInit = instanceType === 'cloudinit';

  if (isContainer || isCloudInit) {
    if (!pwd) {
      return 'vmCreate.ciPasswordRequired';
    }
    if (pwd.length < 6) {
      return 'vmCreate.ciPasswordMin';
    }
    if (pwd.length > 12) {
      return 'vmCreate.ciPasswordMax';
    }
    if (trimmedUser && isSystemReservedUser(trimmedUser)) {
      return 'vmCreate.ciUserReserved';
    }
    return '';
  }

  // Regular KVM VM with cloud-init
  if (!trimmedUser) {
    return 'vmCreate.ciUserRequired';
  }
  if (isSystemReservedUser(trimmedUser)) {
    return 'vmCreate.ciUserReserved';
  }
  if (!pwd) {
    return 'vmCreate.ciPasswordRequired';
  }
  if (pwd.length < 6) {
    return 'vmCreate.ciPasswordMin';
  }
  if (pwd.length > 12) {
    return 'vmCreate.ciPasswordMax';
  }

  return '';
}
