#!/usr/bin/env bash
# webkvm — Docker installer (thin wrapper)
# Delegates to packaging/docker/install.sh, which installs the host
# libvirt/QEMU stack + Docker Engine, then deploys the container via
# docker-compose.yml. See docs/DOCKER.md for what this does and why.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

red()  { printf "\033[31m%s\033[0m\n" "$*"; }
bold() { printf "\033[1m%s\033[0m\n" "$*"; }
die()  { red "ERROR: $*"; exit 1; }

check_sudo() {
    sudo -v
    # Fully detached (stdin/stdout/stderr closed) so this background
    # keep-alive never holds a piped/SSH invocation's shell open waiting
    # for it to exit.
    (while true; do sudo -n true; sleep 300; kill -0 "$$" 2>/dev/null || exit; done) </dev/null >/dev/null 2>&1 &
    disown
}

has_kvm() { [[ -e /dev/kvm ]]; }

bold "webkvm Docker installer"
echo ""

check_sudo

if ! has_kvm; then
    die "KVM not available (/dev/kvm not found). Enable virtualization in BIOS/UEFI (Intel VT-x / AMD-V) or nested virtualization if this is a VM."
fi

if ! command -v systemctl >/dev/null 2>&1; then
    die "systemd is required (not found)."
fi

exec "${SCRIPT_DIR}/packaging/docker/install.sh" "$@"
