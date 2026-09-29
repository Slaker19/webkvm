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
grep -F 'systemctl --no-pager cat virtqemud.service' "${INSTALL}" >/dev/null
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

# One-liner bootstrap guards (piped runs go strictly unattended).
grep -F 'WEBKVM_INSTALL_REPO' "${ROOT}/scripts/install-webkvm.sh" >/dev/null
grep -F 'WEBKVM_NONINTERACTIVE' "${ROOT}/scripts/install-webkvm.sh" >/dev/null

# Preflight diagnostics.
grep -F '/dev/kvm' "${INSTALL}" >/dev/null
grep -F 'preflight' "${INSTALL}" >/dev/null

# Socket detection must use --no-pager (plain `systemctl cat` dies with
# SIGPIPE rc=141 in non-TTY contexts, falsely reading as "unit missing").
grep -F 'systemctl --no-pager cat' "${INSTALL}" >/dev/null

# Runtime deps the backend shells out to (firewall nft/iptables, xz for
# appliances) must exist in ALL THREE distro lists.
for pkg in nftables iptables; do
  grep -F "$pkg" "${INSTALL}" >/dev/null
done
grep -F 'xz-utils' "${INSTALL}" >/dev/null
grep -F 'swtpm swtpm-tools' "${INSTALL}" >/dev/null

# Storage > Host Disks format/mount (host_disks.go) shells out to sgdisk
# (gdisk/gptfdisk), parted/partprobe (parted) and one mkfs.* per entry in
# the curated filesystem catalog (e2fsprogs/xfsprogs/btrfs-progs/
# f2fs-tools) — must exist in every distro's RUNTIME_PACKAGES, plus the
# nfpm/PKGBUILD manifests and the Docker image (a passed-through disk is
# formatted from inside the container, not the host).
for pkg in parted e2fsprogs xfsprogs btrfs-progs f2fs-tools; do
  grep -F "$pkg" "${INSTALL}" >/dev/null
  grep -F "$pkg" "${ROOT}/packaging/nfpm.deb.yaml" >/dev/null
  grep -F "$pkg" "${ROOT}/packaging/nfpm.rpm.yaml" >/dev/null
  grep -F "$pkg" "${ROOT}/packaging/arch/PKGBUILD" >/dev/null
  grep -F "$pkg" "${ROOT}/Dockerfile" >/dev/null
done
# sgdisk's package is named differently per distro (gdisk on apt/dnf,
# gptfdisk on Arch) — checked separately instead of in the loop above.
grep -F 'gdisk' "${INSTALL}" >/dev/null
grep -F 'gptfdisk' "${INSTALL}" >/dev/null
grep -F 'gdisk' "${ROOT}/packaging/nfpm.deb.yaml" >/dev/null
grep -F 'gdisk' "${ROOT}/packaging/nfpm.rpm.yaml" >/dev/null
grep -F 'gptfdisk' "${ROOT}/packaging/arch/PKGBUILD" >/dev/null
grep -F 'gdisk' "${ROOT}/Dockerfile" >/dev/null

# update.sh must match what `make dist` actually publishes (raw binary
# asset + SHA256SUMS with bare filenames), never a tarball as binary.
grep -F 'webkvm-cli' "${ROOT}/packaging/standalone/update.sh" >/dev/null
grep -F 'SHA256SUMS' "${ROOT}/packaging/standalone/update.sh" >/dev/null
bash -n "${ROOT}/packaging/standalone/update.sh"
bash -n "${ROOT}/packaging/standalone/uninstall.sh"

# The backend runs packaging/standalone/update.sh as webkvm-update, so every
# install flavour must ship it: nfpm packages, the tarball and release.yml's
# own copy of that tarball list (they drift apart otherwise).
for shipped_in in \
  "${ROOT}/packaging/nfpm.deb.yaml" \
  "${ROOT}/packaging/nfpm.rpm.yaml" \
  "${ROOT}/Makefile" \
  "${ROOT}/.github/workflows/release.yml"; do
  grep -F 'packaging/standalone/update.sh' "${shipped_in}" >/dev/null
done

# ...and every unit template must set the gate the backend enforces,
# otherwise the button answers 403 no matter how the binary was installed.
grep -F 'WEBKVM_ALLOW_UPDATE=1' "${ROOT}/scripts/webkvm.service" >/dev/null
grep -F 'WEBKVM_ALLOW_UPDATE=1' "${ROOT}/packaging/standalone/install.sh" >/dev/null

# update.sh replaces the binary it is running under: the guards below are the
# difference between a failed update and a host left with no service at all.
# A single instance (two runs race over backup/stop/rename)...
grep -F 'flock -n 9' "${ROOT}/packaging/standalone/update.sh" >/dev/null
# ...a rollback that cannot abort under `set -e` before it restarts the
# service (a bare `install` there used to exit silently, leaving it down)...
grep -F 'restored=1' "${ROOT}/packaging/standalone/update.sh" >/dev/null
# ...and cleanup of the downloaded binary on every exit path, not just the
# happy one.
grep -F 'trap cleanup EXIT' "${ROOT}/packaging/standalone/update.sh" >/dev/null

# uninstall.sh must remove everything the updater can leave behind.
for leftover in webkvm-update '"${BIN}.previous"'; do
  grep -F "${leftover}" "${ROOT}/packaging/standalone/uninstall.sh" >/dev/null
done

# Dockerfile: pinned base (never :rolling), backend shell-outs present,
# HEALTHCHECK with https+http fallback.
grep -F 'FROM ubuntu:' "${ROOT}/Dockerfile" | grep -v rolling >/dev/null
grep -F 'HEALTHCHECK' "${ROOT}/Dockerfile" >/dev/null
for pkg in util-linux procps iptables login curl; do
  grep -F "$pkg" "${ROOT}/Dockerfile" >/dev/null
done

# Native packages: nfpm configs + scripts + make targets exist.
test -f "${ROOT}/packaging/nfpm.deb.yaml"
test -f "${ROOT}/packaging/nfpm.rpm.yaml"
test -f "${ROOT}/packaging/nfpm/postinstall.sh"
grep -F 'postinstall' "${ROOT}/packaging/nfpm.deb.yaml" >/dev/null
grep -F 'purge' "${ROOT}/packaging/nfpm/postremove.sh" >/dev/null
grep -qE '^deb:' "${ROOT}/Makefile"
grep -qE '^rpm:' "${ROOT}/Makefile"

printf 'standalone installer static checks: ok\n'