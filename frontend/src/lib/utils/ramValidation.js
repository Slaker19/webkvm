// One RAM rule for every place that sets a VM's memory (create wizard, Edit
// hardware, backend POST/PATCH /api/vms). KEEP IN SYNC with
// backend/internal/api/vm_ram.go (MinVMRAMMB, LowRAMFloorMB and the codes).
//
//   ram < 512 MB          refused (ram_below_minimum) unless the caller sets
//                         the explicit advanced override allow_low_ram
//   ram < 128 MB          refused even with the override (ram_below_floor)
//   floor > ram           refused (min_ram_above_ram): the ballooning floor
//                         (min_ram_mb) cannot exceed the RAM itself

export const MIN_VM_RAM_MB = 512;
export const LOW_RAM_FLOOR_MB = 128;

// ramIssue: '' when valid, else one of belowMin | belowFloor | floorAboveRam.
export function ramIssue({ ramMB, minRamMB = 0, allowLow = false } = {}) {
  const ram = Number(ramMB) || 0;
  const floor = Number(minRamMB) || 0;
  if (ram < MIN_VM_RAM_MB && !allowLow) return 'belowMin';
  if (ram < LOW_RAM_FLOOR_MB) return 'belowFloor';
  if (floor > 0 && floor > ram) return 'floorAboveRam';
  return '';
}

// ramIssueText: translated message for an issue ('' for none).
export function ramIssueText(t, issue) {
  if (!issue) return '';
  return t(`vmRam.${issue}`, { min: MIN_VM_RAM_MB, floor: LOW_RAM_FLOOR_MB });
}
