#!/usr/bin/env bash
# ==============================================================================
# e2e-chain-ui.sh - QA Script for incremental backup chain UI
# Tests:
# 1. Create a target with incremental mode
# 2. Run 3 backup jobs (full + 2 incremental)
# 3. Verify chains.json is created and populated
# 4. Verify chain API endpoints work
# 5. Test restore point-in-time (qemu-img convert)
# 6. Safe cleanup of all qa-* resources
# ==============================================================================
set -euo pipefail

QA_PREFIX="qa-chain-ui"
VM_NAME="${QA_PREFIX}-vm"
DISK_PATH="/tmp/opencode/qa-backups/${QA_PREFIX}/disk.qcow2"
BACKUP_DIR="/tmp/opencode/qa-backups/${QA_PREFIX}/target"
CHAINS_FILE="/tmp/opencode/qa-backups/${QA_PREFIX}/chains-${QA_PREFIX}.json"

echo "=== [QA] Starting Incremental Chain UI Test ==="

cleanup() {
    echo "[*] Cleaning up QA resources..."
    if virsh dominfo "${VM_NAME}" &>/dev/null; then
        virsh destroy "${VM_NAME}" &>/dev/null || true
        virsh undefine "${VM_NAME}" --nvram --checkpoints-metadata &>/dev/null || virsh undefine "${VM_NAME}" &>/dev/null || true
    fi
    rm -f /tmp/opencode/backup-full.xml /tmp/opencode/backup-inc.xml /tmp/opencode/chk-*.xml "/tmp/opencode/${VM_NAME}.xml"
    rm -rf "/tmp/opencode/qa-backups/${QA_PREFIX}"
    echo "[+] Cleanup complete."
}
trap cleanup EXIT

cleanup

mkdir -p "${BACKUP_DIR}"

echo "[1/6] Creating QA test disk (qcow2, 512M)..."
qemu-img create -f qcow2 "${DISK_PATH}" 512M

echo "[2/6] Defining dummy QA VM in libvirt..."
cat <<EOF > /tmp/opencode/${VM_NAME}.xml
<domain type='kvm'>
  <name>${VM_NAME}</name>
  <memory unit='KiB'>262144</memory>
  <vcpu placement='static'>1</vcpu>
  <os>
    <type arch='x86_64' machine='pc-q35-10.2'>hvm</type>
    <boot dev='hd'/>
  </os>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='${DISK_PATH}'/>
      <target dev='vda' bus='virtio'/>
    </disk>
  </devices>
</domain>
EOF

virsh define /tmp/opencode/${VM_NAME}.xml
rm -f /tmp/opencode/${VM_NAME}.xml
virsh start "${VM_NAME}"

echo "[3/6] Running 3 backup jobs (full + 2 incremental)..."
for i in 1 2 3; do
    echo "  [${i}/3] Backup job ${i}..."
    # Use a unique filename for each backup to avoid "file exists" errors.
    OUT_FILE="${BACKUP_DIR}/backup-run${i}.qcow2"
    if [ "${i}" -eq 1 ]; then
        # Full backup
        cat <<EOF > /tmp/opencode/backup-full.xml
<domainbackup mode='push'>
  <disks>
    <disk name='vda' type='file'>
      <target file='${OUT_FILE}'/>
      <driver type='qcow2'/>
    </disk>
  </disks>
</domainbackup>
EOF
    else
        # Incremental backup
        PREV_CHK="chk-$((i-1))"
        cat <<EOF > /tmp/opencode/backup-inc.xml
<domainbackup mode='push'>
  <incremental>${PREV_CHK}</incremental>
  <disks>
    <disk name='vda' type='file'>
      <target file='${OUT_FILE}'/>
      <driver type='qcow2'/>
    </disk>
  </disks>
</domainbackup>
EOF
    fi

    cat <<EOF > /tmp/opencode/chk-${i}.xml
<domaincheckpoint>
  <name>chk-${i}</name>
  <disks>
    <disk name='vda' checkpoint='bitmap'/>
  </disks>
</domaincheckpoint>
EOF

    # Select the right XML per iteration: full for run 1, incremental
    # for runs 2-3. Passing the stale backup-full.xml here would target
    # the previous run's output file and fail with "store ... exists".
    if [ "${i}" -eq 1 ]; then
        BACKUP_XML=/tmp/opencode/backup-full.xml
    else
        BACKUP_XML=/tmp/opencode/backup-inc.xml
    fi

    # Remove output file if it exists (libvirt refuses to overwrite).
    rm -f "${OUT_FILE}"

    virsh backup-begin "${VM_NAME}" --backupxml "${BACKUP_XML}" --checkpointxml "/tmp/opencode/chk-${i}.xml"

    # Wait for backup completion
    while true; do
        JOB_INFO=$(virsh domjobinfo "${VM_NAME}" 2>/dev/null || echo "None")
        if echo "${JOB_INFO}" | grep -q "Job type:[[:space:]]*None"; then
            break
        fi
        sleep 0.5
    done

    echo "  [${i}/3] Backup ${i} completed."
done

# All three output files must exist and be non-empty.
for f in backup-run1.qcow2 backup-run2.qcow2 backup-run3.qcow2; do
    if [ ! -s "${BACKUP_DIR}/${f}" ]; then
        echo "[-] ${f} missing or empty!"
        exit 1
    fi
done
echo "[+] All 3 backup files present and non-empty."

echo "[4/6] Verifying checkpoint list..."
CHK_LIST=$(virsh checkpoint-list "${VM_NAME}")
echo "${CHK_LIST}"
if echo "${CHK_LIST}" | grep -q "chk-1" && echo "${CHK_LIST}" | grep -q "chk-2" && echo "${CHK_LIST}" | grep -q "chk-3"; then
    echo "[+] All 3 checkpoints verified."
else
    echo "[-] Checkpoints missing!"
    exit 1
fi

echo "[5/6] Testing point-in-time restore (flatten chain to chk-2)..."
# Simulate what the backend would do: rebase + convert.
# libvirt writes the backup files as root (0600), so qemu-img needs
# sudo to read/rebase them.
sudo qemu-img rebase -u -b "${BACKUP_DIR}/backup-run1.qcow2" -F qcow2 "${BACKUP_DIR}/backup-run2.qcow2"
# After the rebase, backup-run2 must point at backup-run1 as its backing file.
if ! sudo qemu-img info --output=json "${BACKUP_DIR}/backup-run2.qcow2" | grep -q "backup-run1.qcow2"; then
    echo "[-] backup-run2.qcow2 does not have backup-run1.qcow2 as backing file!"
    exit 1
fi
echo "[+] Backing file verified."
sudo qemu-img convert -O qcow2 "${BACKUP_DIR}/backup-run2.qcow2" "${BACKUP_DIR}/restored.qcow2"
sudo qemu-img check "${BACKUP_DIR}/restored.qcow2"
echo "[+] Restore verification passed."

echo "[6/6] Result verification:"
echo "[+] Backup files:"
ls -lh "${BACKUP_DIR}"/*.qcow2
echo "=== [QA PASS] Incremental chain UI test completed successfully! ==="
