#!/usr/bin/env bash
set -Eeuo pipefail
DATA_DIR="${WEBKVM_DATA_DIR:-/opt/webkvm}"
BIN="${WEBKVM_PREFIX:-/usr/local}/bin/webkvm"
[[ "${EUID}" -eq 0 ]] || { echo 'run as root' >&2; exit 1; }

case "${DATA_DIR}" in
  /opt/webkvm|/opt/webkvm/*) ;;
  *) echo "refusing unsafe WEBKVM_DATA_DIR: ${DATA_DIR}" >&2; exit 1 ;;
esac

systemctl disable --now webkvm.service 2>/dev/null || true
rm -f /etc/systemd/system/webkvm.service \
  /etc/systemd/system/webkvm.service.previous \
  "${BIN}" "${BIN}.previous"
systemctl daemon-reload
if [[ "${PURGE_DATA:-0}" == 1 ]]; then
  rm -rf -- "${DATA_DIR}"
  echo "removed ${DATA_DIR}"
else
  echo "kept data at ${DATA_DIR}; use PURGE_DATA=1 to remove it"
fi
