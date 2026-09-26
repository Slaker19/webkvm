// Mirrors backend/internal/api/vms.go validVMNameRE — keep in sync.
// 1-64 characters: letters, digits, dots, hyphens and underscores.
export const VM_NAME_RE = /^[A-Za-z0-9_.-]{1,64}$/;

export function isValidVmName(name) {
  return typeof name === 'string' && VM_NAME_RE.test(name);
}
