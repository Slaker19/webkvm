#!/usr/bin/env bash
# Static/dry-run checks for installer invariants. Does not require root,
# apt/dnf/pacman, libvirt or a running service.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INSTALL="${ROOT}/packaging/standalone/install.sh"

bash -n "${INSTALL}"
bash -n "${ROOT}/scripts/install-webkvm.sh"

# Legacy invariants that must survive.
grep -F 'WEBKVM_BINARY_SHA256' "${INSTALL}" >/dev/null
grep -F 'HEALTH_FILE' "${INSTALL}" >/dev/null
grep -F 'CONFIG_EXISTED' "${INSTALL}" >/dev/null
grep -F 'HEALTH_PORT' "${INSTALL}" >/dev/null
grep -F 'SERVICE_PREVIOUS' "${INSTALL}" >/dev/null
grep -F 'systemctl cat virtqemud.service' "${INSTALL}" >/dev/null
grep -F 'mktemp --tmpdir' "${INSTALL}" >/dev/null
grep -F 'virsh -c qemu:///system pool-list' "${INSTALL}" >/dev/null

# Multi-distro: all three package managers represented.
grep -F 'PKG="apt"' "${INSTALL}" >/dev/null
grep -F 'PKG="dnf"' "${INSTALL}" >/dev/null
grep -F 'PKG="pacman"' "${INSTALL}" >/dev/null

# Requires a precompiled binary (server never compiles); clear error otherwise.
grep -F 'WEBKVM_BINARY' "${INSTALL}" >/dev/null
grep -F 'no webkvm binary found' "${INSTALL}" >/dev/null
grep -F 'never compiles' "${INSTALL}" >/dev/null

# Deploys the CLI (webkvm-cli) alongside the main binary.
grep -F 'webkvm-cli' "${INSTALL}" >/dev/null
grep -F '${PREFIX}/bin/webkvm-cli' "${INSTALL}" >/dev/null

# HTTPS: self-signed cert generation + persisted TLS settings.
grep -F 'gen_self_signed' "${INSTALL}" >/dev/null
grep -F 'server.tls_cert' "${INSTALL}" >/dev/null
grep -F 'server.tls_key' "${INSTALL}" >/dev/null
grep -F 'server.tls_domain' "${INSTALL}" >/dev/null
grep -F '/api/system/cert' "${INSTALL}" >/dev/null

# Networks: NAT + bridge wiring is invoked through setup-network.sh.
grep -F 'setup-network.sh' "${INSTALL}" >/dev/null
grep -F 'NETWORK_MODE' "${INSTALL}" >/dev/null
grep -F -e '--bridge' "${INSTALL}" >/dev/null
grep -F -e '--nat' "${INSTALL}" >/dev/null

# One-liner bootstrap guards.
grep -F 'WEBKVM_INSTALL_REPO' "${ROOT}/scripts/install-webkvm.sh" >/dev/null
grep -F 'sudo -E' "${ROOT}/scripts/install-webkvm.sh" >/dev/null

# Preflight diagnostics.
grep -F '/dev/kvm' "${INSTALL}" >/dev/null
grep -F 'preflight' "${INSTALL}" >/dev/null

printf 'standalone installer static checks: ok\n'