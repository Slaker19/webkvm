#!/usr/bin/env bash
# nfpm postremove — remove (keep data) vs purge (delete data).
# deb calls us with: remove | purge | upgrade | ... ; rpm with 0 | 1.
# NOTE: rpm has no purge concept, so rpm -e deliberately KEEPS
# /opt/webkvm (like deb remove) — delete it by hand if wanted.
set -e
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl daemon-reload 2>/dev/null || true
fi
case "${1:-}" in
  purge)
    # Only delete exactly /opt/webkvm (or its documented override) —
    # never anything else.
    DATA_DIR="${WEBKVM_DATA_DIR:-/opt/webkvm}"
    case "${DATA_DIR}" in
      /opt/webkvm|/opt/webkvm/*) rm -rf -- "${DATA_DIR}" ;;
      *) echo "refusing unsafe purge of ${DATA_DIR}" >&2 ;;
    esac
    ;;
esac
