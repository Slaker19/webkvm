#!/usr/bin/env bash
# nfpm preremove — stop/disable ONLY on real removal, never on upgrade.
# $1 is remove|upgrade|failed-upgrade|... (deb) or 0/1 (rpm: 0=uninstall).
set -e
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  case "${1:-}" in
    upgrade|failed-upgrade|1) : ;; # rpm upgrade / deb upgrade: leave running
    *) systemctl disable --now webkvm.service >/dev/null 2>&1 || true ;;
  esac
fi
