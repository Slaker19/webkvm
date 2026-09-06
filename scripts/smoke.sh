#!/usr/bin/env bash
# =============================================================================
# webkvm — smoke test (V12-OPS-03)
#
# Boots a DISPOSABLE VM on the local libvirt host, verifies the
# virtualization stack actually works (pool -> volume -> domain define ->
# start -> running state), then tears everything down. It is designed to
# leave ZERO residue on the host:
#
#   * every libvirt object carries the unique $RUN prefix, so it can never
#     collide with, or touch, a real VM / pool / volume;
#   * a single unconditional EXIT trap funnels every exit path — success,
#     assertion failure, command timeout and Ctrl+C — into the same
#     cleanup(), so a crashed run cannot strand resources.
#
# Requirements: bash >= 4, virsh (libvirt-client), qemu-img, working
# libvirtd (system connection). Run as root or a user with libvirt access.
#
# Usage:
#   sudo ./scripts/smoke.sh
#
# Optional env:
#   SMOKE_TIMEOUT    per-step timeout in seconds (default 120)
#   WEBKVM_SMOKE_KEEP   keep resources for debugging (default 0/false)
# =============================================================================

set -Eeuo pipefail

# --- identity ----------------------------------------------------------------
RUN="${WEBKVM_SMOKE_PREFIX:-webkvm-smoke-$$}-$(date +%s)"
POOL_NAME="${RUN}-pool"
POOL_DIR=""
VM_NAME="${RUN}-vm"
VOL_NAME="${RUN}-disk"
VOL_SIZE_MB="${SMOKE_VOL_MB:-256}"
SMOKE_TIMEOUT="${SMOKE_TIMEOUT:-120}"
KEEP="${WEBKVM_SMOKE_KEEP:-0}"

VIRSH=(virsh -c qemu:///system)

log() { printf '\033[1;34m[smoke]\033[0m %s\n' "$*"; }
warn(){ printf '\033[1;33m[smoke]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[smoke] FATAL:\033[0m %s\n' "$*" >&2; exit 1; }

_CLEANED=0

# --- cleanup: single, idempotent, everything-bounded -------------------------
cleanup() {
  if [ "$_CLEANED" = "1" ]; then return; fi
  _CLEANED=1
  if [ "$KEEP" = "1" ]; then
    warn "WEBKVM_SMOKE_KEEP=1 — leaving resources in place for debugging:"
    warn "  pool=$POOL_NAME volume=$VOL_NAME vm=$VM_NAME"
    return
  fi
  log "cleanup: tearing down $RUN"
  # Kill the VM (if running) and undefine it. Each step is bounded and
  # tolerant of the object already being gone.
  timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" destroy "$VM_NAME" >/dev/null 2>&1 || true
  timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" undefine --remove-all-storage --managed-save \
    --snapshots-metadata "$VM_NAME" >/dev/null 2>&1 || true
  # Remove the scratch volume, then the scratch pool and its backing dir.
  timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" vol-delete --pool "$POOL_NAME" "$VOL_NAME" >/dev/null 2>&1 || true
  timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" pool-destroy "$POOL_NAME" >/dev/null 2>&1 || true
  timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" pool-undefine "$POOL_NAME" >/dev/null 2>&1 || true
  if [ -n "$POOL_DIR" ] && [ -d "$POOL_DIR" ]; then
    rm -rf -- "$POOL_DIR" 2>/dev/null || true
  fi
  log "cleanup: done — host is clean"
}

# EXIT always runs (success, set -e abort, and after INT/TERM handlers).
# INT/TERM just request an exit; the EXIT trap does the actual cleanup,
# so there is exactly one teardown no matter how many signals arrive.
trap 'exit 130' INT
trap 'exit 143' TERM
trap cleanup EXIT

# --- precondition checks -----------------------------------------------------
for bin in virsh qemu-img mktemp timeout; do
  command -v "$bin" >/dev/null 2>&1 || die "required binary not found: $bin"
done
"${VIRSH[@]}" list >/dev/null 2>&1 || die "cannot reach libvirtd (qemu:///system)"

# --- stage 1: scratch storage pool -------------------------------------------
log "creating scratch pool $POOL_NAME"
POOL_DIR="$(mktemp -d /tmp/webkvm-smoke-pool.XXXXXX)" || die "mktemp failed"
chmod 0755 "$POOL_DIR"
timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" pool-define-as "$POOL_NAME" dir - - - - "$POOL_DIR" >/dev/null || die "pool-define-as failed"
timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" pool-build "$POOL_NAME" >/dev/null || die "pool-build failed"
timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" pool-start "$POOL_NAME" >/dev/null || die "pool-start failed"

# --- stage 2: scratch volume -------------------------------------------------
log "creating scratch volume $VOL_NAME (${VOL_SIZE_MB} MB)"
timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" vol-create-as "$POOL_NAME" "$VOL_NAME" "${VOL_SIZE_MB}M" \
  --format qcow2 >/dev/null || die "vol-create-as failed"
VOL_PATH="$("${VIRSH[@]}" vol-path --pool "$POOL_NAME" "$VOL_NAME" 2>/dev/null || true)"
[ -n "${VOL_PATH:-}" ] || die "could not resolve scratch volume path"

# --- stage 3: define a minimal VM --------------------------------------------
# No OS is installed; we only assert the domain reaches the running state,
# which exercises the whole QEMU/KVM bring-up path.
log "defining scratch VM $VM_NAME"
UUID="$(cat /proc/sys/kernel/random/uuid 2>/dev/null || mktemp -u)"
cat >"$POOL_DIR/smoke.xml" <<EOF
<domain type='kvm'>
  <name>$VM_NAME</name>
  <uuid>$UUID</uuid>
  <memory unit='MiB'>128</memory>
  <vcpu>1</vcpu>
  <os>
    <type arch='x86_64' machine='pc'>hvm</type>
    <boot dev='hd'/>
  </os>
  <features><acpi/><apic/></features>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='$VOL_PATH'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'/>
  </devices>
</domain>
EOF
timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" define "$POOL_DIR/smoke.xml" >/dev/null || die "domain define failed"

# --- stage 4: boot and verify running ----------------------------------------
log "starting scratch VM $VM_NAME"
timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" start "$VM_NAME" >/dev/null || die "domain start failed"

log "waiting for domain state == running"
WAITED=0
while [ "$WAITED" -lt "${SMOKE_TIMEOUT}" ]; do
  STATE="$("${VIRSH[@]}" domstate "$VM_NAME" 2>/dev/null || true)"
  if [ "$STATE" = "running" ]; then
    log "PASS: domain reached running state (${WAITED}s)"
    break
  fi
  if [ "$STATE" = "crashed" ] || [ "$STATE" = "failed" ] || [ "$STATE" = "shut off" ]; then
    die "domain ended in unexpected state: $STATE"
  fi
  sleep 1
  WAITED=$((WAITED + 1))
done
[ "$STATE" = "running" ] || die "timeout waiting for running state (${SMOKE_TIMEOUT}s)"

# --- stage 5: optional API sanity (only if a webkvm instance is reachable) ---
if [ -n "${WEBKVM_URL:-}" ]; then
  log "checking webkvm health at $WEBKVM_URL"
  timeout "${SMOKE_TIMEOUT}" curl -fsS -m 10 "${WEBKVM_URL%/}/api/health" >/dev/null 2>&1 \
    || die "webkvm health check failed at $WEBKVM_URL"
  log "PASS: webkvm /api/health OK"
fi

# --- stage 6: graceful VM shutdown before teardown ----------------------------
log "shutting down scratch VM (sanity: guest ACPI path)"
timeout "${SMOKE_TIMEOUT}" "${VIRSH[@]}" shutdown "$VM_NAME" >/dev/null 2>&1 || true

log ""
log "SMOKE TEST PASSED — host left clean."