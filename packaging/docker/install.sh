#!/usr/bin/env bash
# webkvm Docker installer.
#
# Supported families (auto-detected by package manager, covers every
# derivative): Debian/Ubuntu & derivatives (apt), Fedora/RHEL & derivatives
# (dnf/yum), Arch & derivatives (pacman).
#
# Usage: sudo ./install.sh [--dry-run] [--source]
#   --dry-run  Run all preflight checks and print what WOULD be installed;
#              performs no system changes.
#   --source   Build the image locally (make docker-build) instead of
#              pulling the published slaker1908/webkvm image. Requires
#              Go + Node on this host (see README's "Build from source").
#
# Unlike the native installer (packaging/standalone/install.sh), webkvm
# itself is NOT installed on this host: it runs in a container that
# connects to the libvirtd THIS script sets up here. From a fresh server
# this script:
#   1. Preflight-checks KVM, RAM, disk, arch and network.
#   2. Installs libvirt/QEMU/KVM — the same hypervisor stack the native
#      install uses, since VMs are started by THIS host's libvirtd, never
#      inside the container (see docs/DOCKER.md). Does NOT install the
#      container-side tools (xorriso, openssl, nftables, …) — those live
#      in the image.
#   3. Installs Docker Engine + the compose plugin, if missing.
#   4. Deploys docker-compose.yml from this checkout with --network host
#      and --privileged (see docs/DOCKER.md for exactly why each mount/
#      privilege is needed).
#   5. Health-checks the running container and prints a summary.
#
# Interactive when run on a TTY; uses defaults when piped (one-liner).
# Every input is overridable via WEBKVM_* env vars.
set -Eeuo pipefail

DATA_DIR="${WEBKVM_DATA_DIR:-/opt/webkvm}"
DEFAULT_PORT="${WEBKVM_PORT:-8080}"
DOCKER_IMAGE="${WEBKVM_DOCKER_IMAGE:-slaker1908/webkvm:latest}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
COMPOSE_FILE="${REPO_DIR}/docker-compose.yml"

PKG=""
YUM_BIN=""
DRY_RUN=0
BUILD_SOURCE=0
for a in "$@"; do
  case "${a}" in
    --dry-run) DRY_RUN=1 ;;
    --source)  BUILD_SOURCE=1 ;;
  esac
done
[[ "${WEBKVM_DOCKER_SOURCE:-0}" == "1" ]] && BUILD_SOURCE=1

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
log()  { printf '[webkvm-docker] %s\n' "$*"; }
die()  { printf '[webkvm-docker] ERROR: %s\n' "$*" >&2; exit 1; }

# ── Preflight ──────────────────────────────────────────────────────────
preflight() {
  [[ "${EUID}" -eq 0 ]] || die "run as root (sudo $0)"
  if command -v apt-get >/dev/null 2>&1; then
    PKG="apt"
  elif command -v dnf >/dev/null 2>&1; then
    PKG="dnf"; YUM_BIN="dnf"
  elif command -v yum >/dev/null 2>&1; then
    PKG="dnf"; YUM_BIN="yum"
  elif command -v pacman >/dev/null 2>&1; then
    PKG="pacman"
  else
    die "no supported package manager found (apt-get / dnf / yum / pacman)."
  fi
  [[ -f /etc/os-release ]] && . /etc/os-release
  log "detected: ${PRETTY_NAME:-${ID:-unknown}} (family: ${PKG})"
  command -v systemctl >/dev/null || die "systemd is required"
  [[ "$(dpkg --print-architecture 2>/dev/null || uname -m)" == "amd64" ]] || [[ "$(uname -m)" == "x86_64" ]] || die "this release currently requires amd64/x86_64"

  [[ -f "${COMPOSE_FILE}" ]] || die "docker-compose.yml not found next to this script (expected ${COMPOSE_FILE}) — run this from a webkvm checkout"

  echo "  [preflight] checking virtualization support..."
  if [[ ! -e /dev/kvm ]]; then
    echo "  [preflight] FAIL: /dev/kvm is missing — VMs cannot run on this machine." >&2
    echo "  [preflight] Fix: enable virtualization in the BIOS/UEFI (Intel VT-x / AMD-V)," >&2
    echo "  [preflight] or if this is a VM, enable nested virtualization on the hypervisor." >&2
    exit 1
  fi
  local mem_kib
  mem_kib="$(awk '/MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)"
  [[ "${mem_kib}" =~ ^[0-9]+$ && "${mem_kib}" -ge 2097152 ]] || die "at least 2 GiB RAM is required (found $((mem_kib/1024)) MiB)"
  if command -v ip >/dev/null 2>&1; then
    ip route get 1.1.1.1 >/dev/null 2>&1 || die "no usable network route found (required for --network host mode)"
  fi
  echo "  [preflight] ok — KVM, RAM, arch and network look good"
}

# ── Package management (same helpers/conventions as packaging/standalone) ─
pkg_available() {
  local p="$1"
  case "${PKG}" in
    apt)    dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q 'install ok installed' ;;
    dnf)    "${YUM_BIN:-dnf}" -q "$p" >/dev/null 2>&1 ;;
    pacman) pacman -Q "$p" >/dev/null 2>&1 ;;
  esac
}
pkg_install() {
  local failed=0
  for p in "$@"; do
    pkg_available "$p" && continue
    case "${PKG}" in
      apt) apt-get install -y --no-install-recommends "$p" ;;
      dnf) "${YUM_BIN:-dnf}" install -y "$p" ;;
      pacman) pacman -S --needed --noconfirm "$p" ;;
    esac || { echo "    failed to install: $p" >&2; failed=1; }
  done
  [[ "${failed}" == 1 ]] && return 1
  return 0
}
pkg_update() {
  case "${PKG}" in
    apt) apt-get update ;;
    dnf) "${YUM_BIN:-dnf}" check-update >/dev/null 2>&1 || true ;;
    pacman)
      pacman -Sy --noconfirm archlinux-keyring >/dev/null 2>&1 || true
      pacman -Sy ;;
  esac
}

# Host only needs the hypervisor stack (VMs run under THIS host's
# libvirtd, never inside the container) — NOT xorriso/openssl/python3/
# nftables/etc., which the container brings itself. Keep this list in
# sync with packaging/standalone/install.sh's RUNTIME_PACKAGES minus the
# webkvm-binary-only tooling.
libvirt_packages() {
  case "${PKG}" in
    apt)    echo libvirt-daemon-system libvirt-clients libvirt-daemon-driver-qemu qemu-system-x86 qemu-utils ovmf swtpm swtpm-tools virtinst bridge-utils dnsmasq-base ;;
    dnf)    echo libvirt-daemon-kvm libvirt-client qemu-kvm qemu-img edk2-ovmf swtpm-tools virt-install bridge-utils dnsmasq ;;
    pacman) echo libvirt qemu-full qemu-img swtpm edk2-ovmf virt-install dnsmasq ;;
  esac
}

# Docker Engine + the compose plugin. Prefers each distro's own repos
# (no extra third-party repo to add) — good enough for `docker compose`.
# Fedora ships no package literally named "docker" (trademark); if
# moby-engine isn't available either, we stop and point at Docker's own
# install docs rather than silently adding their apt/dnf repo ourselves.
install_docker() {
  if command -v docker >/dev/null 2>&1; then
    log "Docker already installed: $(docker --version)"
  else
    log "installing Docker Engine..."
    case "${PKG}" in
      apt)    pkg_install docker.io docker-compose-v2 || die "could not install docker.io — see https://docs.docker.com/engine/install/" ;;
      dnf)    pkg_install moby-engine docker-compose-plugin || die "docker isn't in Fedora/RHEL's default repos under that name — add Docker's official repo per https://docs.docker.com/engine/install/fedora/ and re-run" ;;
      pacman) pkg_install docker docker-compose || die "could not install docker — see https://docs.docker.com/engine/install/archlinux/" ;;
    esac
  fi
  systemctl enable --now docker
  if [[ -n "${SUDO_USER:-}" ]] && ! id -nG "${SUDO_USER}" 2>/dev/null | tr ' ' '\n' | grep -qx docker; then
    log "adding ${SUDO_USER} to the 'docker' group (re-login required for manual 'docker' use without sudo)"
    usermod -aG docker "${SUDO_USER}" || true
  fi
}

# Sets COMPOSE (array) to the working invocation: the v2 plugin
# ("docker compose") if present, else the standalone v1 binary
# ("docker-compose") — both understand this repo's docker-compose.yml.
detect_compose() {
  if docker compose version >/dev/null 2>&1; then
    COMPOSE=(docker compose)
  elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE=(docker-compose)
  else
    die "neither 'docker compose' nor 'docker-compose' is available after installing Docker"
  fi
}

enable_libvirtd() {
  if systemctl --no-pager cat libvirtd.service >/dev/null 2>&1; then
    systemctl enable --now libvirtd.service
  elif systemctl --no-pager cat virtqemud.service >/dev/null 2>&1; then
    systemctl enable --now virtqemud.service
  else
    die "libvirt daemon service was not found after installation"
  fi
  for i in $(seq 1 10); do
    virsh -c qemu:///system list --all >/dev/null 2>&1 && return 0
    sleep 1
  done
  die "libvirt qemu:///system did not become available after starting the daemon"
}

# ── Main ────────────────────────────────────────────────────────────────
bold "webkvm Docker installer"
echo ""
preflight

read -r -a LIBVIRT_PKGS <<< "$(libvirt_packages)"
if [[ "${DRY_RUN}" == 1 ]]; then
  echo ""
  bold "=== DRY RUN — no changes will be made ==="
  echo "  Would install (libvirt/QEMU host packages): ${LIBVIRT_PKGS[*]}"
  echo "  Would install Docker Engine + compose plugin (if missing)"
  echo "  Would enable+start libvirtd and docker"
  echo "  Would run: ${COMPOSE_FILE}"
  if [[ "${BUILD_SOURCE}" == 1 ]]; then
    echo "  Would build the image locally: make -C ${REPO_DIR} docker-build"
  else
    echo "  Would pull image: ${DOCKER_IMAGE}"
  fi
  echo "  Would create DATA_DIR: ${DATA_DIR}"
  exit 0
fi

log "updating package index..."
pkg_update

log "installing libvirt/QEMU (host hypervisor stack: ${LIBVIRT_PKGS[*]})..."
pkg_install "${LIBVIRT_PKGS[@]}" || die "one or more libvirt/QEMU packages failed to install"
enable_libvirtd

install_docker
detect_compose

install -d -m 0755 "${DATA_DIR}"

cd "${REPO_DIR}"
if [[ "${BUILD_SOURCE}" == 1 ]]; then
  log "building the image from source (make docker-build)..."
  make docker-build
else
  log "pulling ${DOCKER_IMAGE}..."
  "${COMPOSE[@]}" pull
fi

log "starting webkvm (network_mode: host, privileged: true — see docs/DOCKER.md)..."
"${COMPOSE[@]}" up -d

log "waiting for the backend to come up..."
ok=0
for i in $(seq 1 30); do
  if curl -kfsS --max-time 2 "https://127.0.0.1:${DEFAULT_PORT}/api/health" >/dev/null 2>&1; then ok=1; break; fi
  sleep 1
done
if [[ "${ok}" != 1 ]]; then
  "${COMPOSE[@]}" logs --tail=80 webkvm >&2 || true
  die "health check failed — see the logs above ('docker compose logs -f webkvm' at ${REPO_DIR})"
fi

echo ""
bold "=== webkvm (Docker) installed successfully ==="
echo ""
lan_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo "  Web UI:  https://${lan_ip}:${DEFAULT_PORT}"
echo "           https://localhost:${DEFAULT_PORT}"
echo "  Certificate: ${DATA_DIR}/certs/webkvm.crt  (self-signed, generated on first boot)"
if [[ -f "${DATA_DIR}/admin-password.initial" ]]; then
  echo "  Admin password: $(cat "${DATA_DIR}/admin-password.initial")"
fi
echo ""
echo "  Manage it with: cd ${REPO_DIR} && docker compose {logs -f,restart,down}"
echo "  Update it with: cd ${REPO_DIR} && git pull && docker compose pull && docker compose up -d"
echo "  Full docs: docs/DOCKER.md"
