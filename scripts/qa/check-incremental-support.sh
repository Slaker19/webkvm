#!/usr/bin/env bash
# check-incremental-support.sh — Verifies if the host hypervisor (QEMU / libvirt)
# supports libvirt incremental backups and qcow2 dirty bitmaps.
set -euo pipefail

echo "=== WebKVM Incremental Backup Capability Check ==="

HAS_ERRORS=0

# 1. Check virsh availability
if ! command -v virsh >/dev/null 2>&1; then
  echo "[-] ERROR: virsh command not found. libvirt-clients is required."
  exit 1
fi

# 2. Check qemu-img availability
if ! command -v qemu-img >/dev/null 2>&1; then
  echo "[-] ERROR: qemu-img command not found. qemu-utils is required."
  exit 1
fi

# 3. Check virsh domcapabilities for backup support
DOM_CAPS=$(virsh domcapabilities 2>/dev/null || true)
if echo "$DOM_CAPS" | grep -q "<backup supported='yes'/>"; then
  echo "[+] libvirt domain capabilities: <backup supported='yes'/> (OK)"
else
  echo "[-] WARNING: <backup supported='yes'/> NOT reported in domcapabilities."
  HAS_ERRORS=1
fi

# 4. Check QEMU version
QEMU_VER=$(qemu-system-x86_64 --version 2>/dev/null | head -n1 || true)
echo "[*] QEMU version: $QEMU_VER"

# 5. Check libvirt version
LIBVIRT_VER=$(virsh --version 2>/dev/null || true)
echo "[*] libvirt version: $LIBVIRT_VER"

# 6. Check qcow2 bitmap creation support
TMP_IMG="/tmp/opencode/qa-bitmap-check.qcow2"
mkdir -p /tmp/opencode
rm -f "$TMP_IMG"
if qemu-img create -f qcow2 "$TMP_IMG" 64M >/dev/null 2>&1 && \
   qemu-img bitmap --add "$TMP_IMG" test-bitmap >/dev/null 2>&1 && \
   qemu-img info "$TMP_IMG" | grep -q "test-bitmap"; then
  echo "[+] qemu-img dirty bitmap support: OK"
  rm -f "$TMP_IMG"
else
  echo "[-] ERROR: qemu-img dirty bitmap operations failed."
  rm -f "$TMP_IMG"
  HAS_ERRORS=1
fi

if [ "$HAS_ERRORS" -eq 0 ]; then
  echo "[+] RESULT: Host is fully compatible with libvirt incremental backups."
  exit 0
else
  echo "[-] RESULT: Host has missing or incomplete capabilities for incremental backups."
  exit 2
fi
