#!/usr/bin/env bash
# webkvm — unified installer
# Delegates to the standalone installer, which installs runtime packages
# (libvirt/qemu) and deploys the precompiled binary. The server never
# compiles, so no Go/Node/build toolchain is required.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# ── helpers ──────────────────────────────────────────────────────────
red()   { printf "\033[31m%s\033[0m\n" "$*"; }
bold()  { printf "\033[1m%s\033[0m\n" "$*"; }
die()   { red "ERROR: $*"; exit 1; }

check_sudo() {
    sudo -v
    while true; do sudo -n true; sleep 300; kill -0 "$$" 2>/dev/null || exit; done 2>/dev/null &
}

has_kvm() { [[ -e /dev/kvm ]]; }

# ── main ─────────────────────────────────────────────────────────────

bold "webkvm installer"
echo ""

check_sudo

if ! has_kvm; then
    die "KVM not available (/dev/kvm not found). Enable virtualization in BIOS/UEFI (Intel VT-x / AMD-V) or nested virtualization if this is a VM."
fi

if ! command -v systemctl >/dev/null 2>&1; then
    die "systemd is required (not found). webkvm installs as a systemd service."
fi

exec "${SCRIPT_DIR}/packaging/standalone/install.sh"
