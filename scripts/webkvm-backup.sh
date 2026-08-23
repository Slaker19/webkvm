#!/usr/bin/env bash
# webkvm-backup.sh — snapshot /opt/webkvm to the
# SMB share at /mnt/webkvm-backup/webkvm-${HOSTNAME}/.
#
# Includes: users.json, jwt.key, audit.log, pool-purposes.json,
# groups.json, covers/, and pools/ metadata (NOT .qcow2 or .iso
# files, those are huge and recoverable from elsewhere).
#
# Output: JSON line on stdout with filename, size, sha256, duration_ms.
# Exit code 0 on success, non-zero on failure (with reason on stderr).
#
# Idempotent: re-running creates a new timestamped file. No
# cleanup of older backups — that's the operator's job (or a
# future retention cron).
#
# Implementation note: we enumerate the file list with `find` and
# feed it to `tar -T -` instead of letting tar walk the tree.
# This matters because DATA_DIR/pools/ typically contains
# multi-GB .qcow2 / .iso files; even with --exclude, tar spends
# a long time opening them. Listing them up front and skipping
# at the find level makes the backup take seconds, not minutes.

set -euo pipefail

# --- Config (env-overridable) -----------------------------------------
DATA_DIR="${DATA_DIR:-/opt/webkvm}"
BACKUP_MOUNT="${BACKUP_MOUNT:-/mnt/webkvm-backup}"
HOST_TAG="${HOST_TAG:-$(hostname -s 2>/dev/null || hostname || echo unknown)}"
BACKUP_DIR="${BACKUP_MOUNT}/webkvm-${HOST_TAG}"

# --- Preflight --------------------------------------------------------
if ! mountpoint -q "${BACKUP_MOUNT}" 2>/dev/null; then
  echo "ERROR: ${BACKUP_MOUNT} is not a mountpoint (SMB share not available)" >&2
  exit 1
fi
if [ ! -d "${DATA_DIR}" ]; then
  echo "ERROR: ${DATA_DIR} does not exist" >&2
  exit 1
fi
mkdir -p "${BACKUP_DIR}"

# --- Build the file list ---------------------------------------------
# Find everything in DATA_DIR that we want to back up. We include
# only files smaller than 100 MB; everything larger is a VM
# disk image or install ISO (libvirt stores them with arbitrary
# names like "ubuntu-1.1782283212" so extension-based excludes
# are unreliable). We also prune *.bak, *.tmp and the running
# log file by name.
START_NS=$(date +%s%N)
FILE_LIST=$(mktemp)
trap 'rm -f "${FILE_LIST}" "${TMP_FILE:-}"' EXIT

( cd "${DATA_DIR}" && \
  find . \
    \( -name '*.bak' -o -name '*.tmp' -o -name 'backend.log' \) -prune -o \
    -type f -size -100M -print0
) > "${FILE_LIST}"

# Guard against an empty list (would make tar hang reading stdin).
if [ ! -s "${FILE_LIST}" ]; then
  echo "ERROR: no files to back up under ${DATA_DIR}" >&2
  exit 1
fi

# --- Build the archive ------------------------------------------------
TS=$(date -u +%Y%m%dT%H%M%SZ)
TMP_FILE=$(mktemp "${BACKUP_DIR}/.backup.${TS}.XXXXXX.tar.gz")

if ! tar -C "${DATA_DIR}" \
    --null -T "${FILE_LIST}" \
    -czf "${TMP_FILE}" \
    --xattrs ; then
  rm -f "${TMP_FILE}"
  echo "ERROR: tar failed" >&2
  exit 1
fi

END_NS=$(date +%s%N)
DURATION_MS=$(( (END_NS - START_NS) / 1000000 ))

# Atomic move to final name (so concurrent runs of this script
# never see a half-written file).
FINAL_NAME="${HOST_TAG}-${TS}.tar.gz"
mv "${TMP_FILE}" "${BACKUP_DIR}/${FINAL_NAME}"

# --- Compute stats ----------------------------------------------------
SIZE=$(stat -c '%s' "${BACKUP_DIR}/${FINAL_NAME}" 2>/dev/null \
       || stat -f '%z' "${BACKUP_DIR}/${FINAL_NAME}")
SHA256=$(sha256sum "${BACKUP_DIR}/${FINAL_NAME}" 2>/dev/null \
         | awk '{print $1}' \
         || shasum -a 256 "${BACKUP_DIR}/${FINAL_NAME}" | awk '{print $1}')

# --- Emit JSON result on stdout --------------------------------------
# Use jq if available for proper escaping, else fall back to
# hand-rolled JSON. Filename, size, sha256 and TS are all
# shell-controlled so escaping is straightforward.
if command -v jq >/dev/null 2>&1; then
  jq -n \
    --arg filename "${FINAL_NAME}" \
    --arg path "${BACKUP_DIR}/${FINAL_NAME}" \
    --argjson size "${SIZE}" \
    --arg sha256 "${SHA256}" \
    --arg ts "${TS}" \
    --argjson duration_ms "${DURATION_MS}" \
    --arg host "${HOST_TAG}" \
    '{filename: $filename, path: $path, size: $size, sha256: $sha256, timestamp: $ts, duration_ms: $duration_ms, host: $host}'
else
  printf '{"filename":"%s","path":"%s","size":%d,"sha256":"%s","timestamp":"%s","duration_ms":%d,"host":"%s"}\n' \
    "${FINAL_NAME}" \
    "${BACKUP_DIR}/${FINAL_NAME}" \
    "${SIZE}" \
    "${SHA256}" \
    "${TS}" \
    "${DURATION_MS}" \
    "${HOST_TAG}"
fi
