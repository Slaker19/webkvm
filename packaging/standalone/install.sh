#!/usr/bin/env bash
# webkvm standalone installer.
#
# Supported families (auto-detected by package manager, covers every
# derivative): Debian/Ubuntu & derivatives (apt), Fedora/RHEL & derivatives
# (dnf/yum), Arch & derivatives (pacman).
#
# Usage: sudo ./install.sh [--dry-run]
#   --dry-run  Run all preflight checks and print what WOULD be installed;
#              performs no system changes.
#
# From a fresh server this script:
#   1. Preflight-checks KVM, RAM, disk, arch and network.
#   2. Installs runtime dependencies (libvirt, qemu, ovmf, swtpm, …).
#   3. Builds the backend+frontend from this checkout (or uses a supplied
#      binary / release URL).
#   4. Installs the binary, systemd service and DATA_DIR.
#   5. Optionally generates a self-signed TLS certificate (SAN includes
#      localhost, hostname.local, the LAN IP and an optional domain) and
#      persists server.tls_* so the backend serves HTTPS directly.
#   6. Optionally wires the libvirt default NAT network and/or a macvlan
#      bridge (br0) to the real LAN via scripts/setup-network.sh.
#   7. Health-checks the running service and prints a summary.
#
# Interactive when run on a TTY; uses defaults when piped (one-liner).
# Every input is overridable via WEBKVM_* / NETWORK_MODE / BRIDGE_* env vars.
set -Eeuo pipefail

PREFIX="${WEBKVM_PREFIX:-/usr/local}"
BIN="${PREFIX}/bin/webkvm"
DATA_DIR="${WEBKVM_DATA_DIR:-/opt/webkvm}"
DEFAULT_BIND="${WEBKVM_BIND_ADDR:-}"
DEFAULT_PORT="${WEBKVM_PORT:-}"
BIN_URL="${WEBKVM_BINARY_URL:-}"
BIN_SHA256="${WEBKVM_BINARY_SHA256:-}"
HTTPS="${WEBKVM_HTTPS:-}"
TLS_DOMAIN="${WEBKVM_TLS_DOMAIN:-}"
NETWORK_MODE="${NETWORK_MODE:-}"
BRIDGE_DHCP="${BRIDGE_DHCP:-}"
BRIDGE_STATIC_IP="${BRIDGE_STATIC_IP:-}"
BRIDGE_STATIC_GW="${BRIDGE_STATIC_GW:-}"
BRIDGE_STATIC_DNS="${BRIDGE_STATIC_DNS:-}"

SERVICE="${WEBKVM_SERVICE:-webkvm.service}"
SERVICE_PATH="/etc/systemd/system/${SERVICE}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CERT_DIR="${DATA_DIR}/certs"
CONFIG_PATH="${DATA_DIR}/config.json"
SETUP_NETWORK="${REPO_DIR}/scripts/setup-network.sh"

PREVIOUS="${BIN}.previous"
SERVICE_PREVIOUS="${SERVICE_PATH}.previous"
CONFIG_EXISTED=0
DOWNLOADED_BIN=""
HEALTH_FILE=""
HAD_BIN=0
HAD_SERVICE=0
ROLLBACK_ARMED=0
INSTALL_SUCCEEDED=0
PKG=""
PKG_INSTALL=()
PKG_CHECK=()
RUNTIME_PACKAGES=()
NONINTERACTIVE=0
[[ -t 0 ]] || NONINTERACTIVE=1
[[ "${WEBKVM_NONINTERACTIVE:-0}" == "1" ]] && NONINTERACTIVE=1
DRY_RUN=0
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=1

rollback_on_failure() {
  local rc=$?
  [[ -n "${DOWNLOADED_BIN}" ]] && rm -f -- "${DOWNLOADED_BIN}"
  if [[ "${rc}" -ne 0 && "${ROLLBACK_ARMED}" == 1 && "${INSTALL_SUCCEEDED}" == 0 ]]; then
    log "deployment failed; restoring previous installation"
    if [[ "${HAD_BIN}" == 1 && -f "${PREVIOUS}" ]]; then
      install -m 0755 "${PREVIOUS}" "${BIN}" || true
    else
      rm -f -- "${BIN}" || true
    fi
    if [[ "${HAD_SERVICE}" == 1 && -f "${SERVICE_PREVIOUS}" ]]; then
      install -m 0644 "${SERVICE_PREVIOUS}" "${SERVICE_PATH}" || true
    elif [[ "${HAD_SERVICE}" == 0 ]]; then
      rm -f -- "${SERVICE_PATH}" || true
    fi
    systemctl daemon-reload 2>/dev/null || true
    systemctl restart "${SERVICE}" 2>/dev/null || true
  fi
  exit "${rc}"
}
trap rollback_on_failure EXIT

log() { printf '[webkvm] %s\n' "$*"; }
die() { printf '[webkvm] ERROR: %s\n' "$*" >&2; exit 1; }
confirm() { # confirm VAR "question" default
  local var="$1" q="$2" dflt="${3:-}"
  if [[ -n "${!var:-}" ]]; then return; fi
  if [[ "${NONINTERACTIVE}" == 1 ]]; then
    if [[ -t 1 && -c /dev/tty ]]; then
      local ans
      read -r -p "  ${q} [${dflt}]: " ans < /dev/tty > /dev/tty
      [[ -z "${ans}" ]] && ans="${dflt}"
      eval "${var}=\"${ans}\""
      return
    fi
    if [[ -n "${dflt}" ]]; then eval "${var}=\"${dflt}\""; fi
    return
  fi
  local ans
  read -r -p "  ${q} [${dflt}]: " ans
  [[ -z "${ans}" ]] && ans="${dflt}"
  eval "${var}=\"${ans}\""
}
prompt_select() { # prompt_select VAR "question" "opt1|label" "opt2|label" default
  local var="$1" q="$2"; shift 2
  local default="${@: -1}"
  local -a opts_raw=("${@:1:$#-1}")
  if [[ -n "${!var:-}" ]]; then return; fi
  if [[ "${NONINTERACTIVE}" == 1 ]]; then
    if [[ -t 1 && -c /dev/tty ]]; then
      # fall through to interactive prompt via /dev/tty
      :
    else
      eval "${var}=\"${default}\""
      return
    fi
  fi
  echo "  ${q}"
  local i=1 opts=() key
  for opt in "${opts_raw[@]}"; do
    key="${opt%%|*}"
    echo "    ${i}) ${opt#*|}"
    opts[$i]="${key}"
    i=$((i+1))
  done
  local ans
  if [[ "${NONINTERACTIVE}" == 1 && -t 1 && -c /dev/tty ]]; then
    read -r -p "  Choose [1-${#opts[@]}] (default ${default}): " ans < /dev/tty > /dev/tty
  else
    read -r -p "  Choose [1-${#opts[@]}] (default ${default}): " ans
  fi
  [[ -z "${ans}" ]] && ans="${default}"
  if [[ "${ans}" =~ ^[0-9]+$ ]] && [[ -n "${opts[${ans}]:-}" ]]; then
    eval "${var}=\"${opts[${ans}]}\""
  else
    eval "${var}=\"${ans}\""
  fi
}

# ── Preflight ──────────────────────────────────────────────────────────
preflight() {
  [[ "${EUID}" -eq 0 ]] || die "run as root (sudo $0)"
  # Family detection by PACKAGE MANAGER presence — covers every derivative
  # (Mint/Pop/Kali→apt, Nobara/Bazzite-workstations→dnf, Manjaro/CachyOS→pacman)
  # without maintaining a distro ID list. /etc/os-release is only logged.
  if command -v apt-get >/dev/null 2>&1; then
    PKG="apt"
  elif command -v dnf >/dev/null 2>&1; then
    PKG="dnf"; YUM_BIN="dnf"
  elif command -v yum >/dev/null 2>&1; then
    PKG="dnf"; YUM_BIN="yum"   # legacy RHEL: same repo/args, yum binary
  elif command -v pacman >/dev/null 2>&1; then
    PKG="pacman"
  else
    die "no supported package manager found (apt-get / dnf / yum / pacman). Supported families: Debian/Ubuntu & derivatives, Fedora/RHEL & derivatives, Arch & derivatives."
  fi
  [[ -f /etc/os-release ]] && . /etc/os-release
  log "detected: ${PRETTY_NAME:-${ID:-unknown}} (family: ${PKG})"
  command -v systemctl >/dev/null || die "systemd is required"
  [[ "$(dpkg --print-architecture 2>/dev/null || uname -m)" == "amd64" ]] || [[ "$(uname -m)" == "x86_64" ]] || die "this release currently requires amd64/x86_64"

  echo "  [preflight] checking virtualization support..."
  if [[ ! -e /dev/kvm ]]; then
    echo "  [preflight] FAIL: /dev/kvm is missing — VMs cannot run on this machine." >&2
    echo "  [preflight] Fix: enable virtualization in the BIOS/UEFI (Intel VT-x / AMD-V)," >&2
    echo "  [preflight] or if this is a VM, enable nested virtualization on the hypervisor." >&2
    exit 1
  fi
  local mem_kib avail_kib
  mem_kib="$(awk '/MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)"
  [[ "${mem_kib}" =~ ^[0-9]+$ && "${mem_kib}" -ge 2097152 ]] || die "at least 2 GiB RAM is required (found $((mem_kib/1024)) MiB)"
  avail_kib="$(df -Pk "$(dirname "${DATA_DIR}")" 2>/dev/null | awk 'NR==2 {print $4}')"
  if [[ "${avail_kib:-}" =~ ^[0-9]+$ ]]; then
    [[ "${avail_kib}" -ge 5242880 ]] || die "at least 5 GiB free space is required on ${DATA_DIR} (found $((avail_kib/1024)) MiB)"
  fi
  if command -v ip >/dev/null 2>&1; then
    ip route get 1.1.1.1 >/dev/null 2>&1 || die "no usable network route found"
  fi
  echo "  [preflight] ok — KVM, RAM, disk, arch and network look good"
}

# ── Package management ─────────────────────────────────────────────────
pkg_available() { # package provided by this distro?
  local p="$1"
  case "${PKG}" in
    apt)    dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q 'install ok installed' ;;
    dnf)    "${YUM_BIN:-dnf}" -q "$p" >/dev/null 2>&1 ;;
    pacman) pacman -Q "$p" >/dev/null 2>&1 ;;
  esac
}
pkg_install() {
  local pkg_installed=0 failed=0
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
      # Old ISOs ship a stale keyring -> signature errors on EVERY package.
      pacman -Sy --noconfirm archlinux-keyring >/dev/null 2>&1 || true
      pacman -Sy ;;
  esac
}

setup_package_map() {
  case "${PKG}" in
    apt)
      RUNTIME_PACKAGES=(ca-certificates curl openssl xorriso dnsmasq-base libvirt-daemon-system libvirt-clients libvirt-daemon-driver-qemu qemu-system-x86 qemu-utils ovmf swtpm swtpm-tools virtinst bridge-utils python3 iproute2 procps util-linux tar zstd)
      ;;
    dnf)
      RUNTIME_PACKAGES=(ca-certificates curl openssl xorriso dnsmasq libvirt-daemon-kvm libvirt-client qemu-kvm qemu-img edk2-ovmf swtpm-tools virt-install bridge-utils python3 iproute procps-ng util-linux tar zstd)
      ;;
    pacman)
      # Arch: bridge-utils was dropped (brctl replaced by `ip` from iproute2, already listed);
      # the setup-network bridge uses `ip link add ... type macvlan` and never needs brctl.
      # dnsmasq is required for the libvirt default NAT network (otherwise
      # "could not find dnsmasq in $PATH" and VMs never get an IP).
      RUNTIME_PACKAGES=(ca-certificates curl openssl xorriso libvirt qemu-full qemu-img swtpm edk2-ovmf python iproute2 procps-ng util-linux tar zstd virt-install dnsmasq)
      ;;
  esac
}

# ── Interactive settings ───────────────────────────────────────────────
prompt_settings() {
  echo ""
  confirm DEFAULT_PORT "Backend HTTP/HTTPS port" "8080"
  confirm DEFAULT_BIND "Bind address" "0.0.0.0"
  # Pregunta clara: SSL (IP o dominio) vs solo IP (HTTP). Cubre el caso
  prompt_select HTTPS "How do you want to access WebKVM?" "no|IP only (plain HTTP)" "yes|With SSL — self-signed certificate (works for IP and domain names, recommended)" "yes"
  if [[ "${HTTPS}" == "yes" ]]; then
    confirm TLS_DOMAIN "Certificate domain (optional — e.g. webkvm.example.com; empty = IP/hostname only, SAN covers LAN IP too)" ""
  fi
  # Piped installs (curl | sudo bash) run non-interactive: default to NAT,
  # NEVER touch the machine's LAN config without asking.
  local net_default="nat"
  [[ "${NONINTERACTIVE}" == 1 ]] || net_default="both"
  prompt_select NETWORK_MODE "How should VMs reach the network?" "nat|NAT (Internet via host)" "bridge|Bridge to the real LAN (macvlan br0)" "both|Both" "${net_default}"
  if [[ "${NETWORK_MODE}" == "both" || "${NETWORK_MODE}" == "bridge" ]]; then
    if [[ -z "${BRIDGE_DHCP}" && -z "${BRIDGE_STATIC_IP}" && "${NONINTERACTIVE}" == 0 ]]; then
      local ans
      read -r -p "  Bridge IP: DHCP (recommended) or static? [dhcp/static] (default dhcp): " ans
      if [[ "${ans}" == "static" ]]; then
        BRIDGE_DHCP="false"
        read -r -p "  Static IP (CIDR, e.g. 192.168.1.100/24): " BRIDGE_STATIC_IP
        read -r -p "  Gateway (e.g. 192.168.1.1): " BRIDGE_STATIC_GW
        read -r -p "  DNS (comma-separated): " BRIDGE_STATIC_DNS
      else
        BRIDGE_DHCP="true"
      fi
    fi
  fi
}

# ── Self-signed certificate ────────────────────────────────────────────
gen_self_signed() {
  command -v openssl >/dev/null || die "openssl is required for HTTPS"
  install -d -m 0755 "${CERT_DIR}"
  local hn
  hn="$(printf '%s' "${HOSTNAME:-webkvm}" | tr -c 'A-Za-z0-9.-' '-' | sed 's/-\{2,\}/-/g;s/^-//;s/-$//')"
  [[ -n "${hn}" ]] || hn="webkvm"
  local san="DNS:webkvm,DNS:localhost,DNS:${hn}.local,IP:127.0.0.1"
  local lan_ip
  # hostname(1) may be missing on minimal Arch — do not let pipefail trigger ERR trap.
  lan_ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
  if [[ -z "${lan_ip}" ]] && command -v ip >/dev/null 2>&1; then
    lan_ip="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}' || true)"
  fi
  [[ -n "${lan_ip}" ]] && san="${san},IP:${lan_ip}"
  [[ -n "${TLS_DOMAIN}" ]] && san="${san},DNS:${TLS_DOMAIN}"
  log "generating self-signed certificate (SAN=${san})..."
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -keyout "${CERT_DIR}/webkvm.key" -out "${CERT_DIR}/webkvm.crt" \
    -subj "/O=webkvm/CN=webkvm" -addext "subjectAltName=${san}" 2>/dev/null
  chmod 0600 "${CERT_DIR}/webkvm.key"
  chmod 0644 "${CERT_DIR}/webkvm.crt"
}

# Persist a scalar into config.json values without clobbering anything else.
persist_setting() {
  local key="$1" value="$2"
  [[ -f "${CONFIG_PATH}" ]] || return 0
  python3 - "${CONFIG_PATH}" "${key}" "${value}" <<'PY'
import json
import pathlib
import sys
path, key, value = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.loads(pathlib.Path(path).read_text())
d.setdefault("values", {})[key] = value
tmp = pathlib.Path(path + ".tmp")
tmp.write_text(json.dumps(d, indent=2) + "\n")
tmp.chmod(0o600)
tmp.replace(path)
PY
}

# ── Main ───────────────────────────────────────────────────────────────
bold() { printf "\033[1m%s\033[0m\n" "$*"; }

echo ""
bold "webkvm standalone installer"
echo ""

preflight
setup_package_map

if [[ "${DRY_RUN}" == 1 ]]; then
  echo ""
  log "── DRY-RUN (no se ha instalado nada) ────────────"
  log "familia de paquetes : ${PKG}"
  log "runtime packages that would be installed:"
  local_pkg=""
  for local_pkg in "${RUNTIME_PACKAGES[@]}"; do log "  · ${local_pkg}"; done
  log "binario destino     : ${BIN}"
  log "servicio            : ${SERVICE} (enable --now)"
  log "datos               : ${DATA_DIR}"
  if [[ -x "${WEBKVM_BINARY:-}" ]]; then
    log "fuente binario      : ${WEBKVM_BINARY} (local)"
  elif [[ -x "${REPO_DIR}/backend/webkvm" ]]; then
    log "fuente binario      : ${REPO_DIR}/backend/webkvm (repo checkout)"
  elif [[ -n "${BIN_URL}" && -n "${BIN_SHA256}" ]]; then
    log "fuente binario      : descarga desde BIN_URL"
  else
    die "dry-run: sin fuente de binario (define WEBKVM_BINARY o compila con make dist)"
  fi
  log "dry-run OK — sistema intacto."
  exit 0
fi

prompt_settings

# Values are interpolated into a systemd unit. Reject whitespace / control
# chars / malformed paths rather than producing an ambiguous unit file.
[[ "${PREFIX}" == /* && "${PREFIX}" != *[[:space:]]* && "${PREFIX}" != *$'\n'* ]] || die "WEBKVM_PREFIX must be an absolute path without whitespace"
[[ "${DATA_DIR}" == /* && "${DATA_DIR}" != *[[:space:]]* && "${DATA_DIR}" != *$'\n'* ]] || die "WEBKVM_DATA_DIR must be an absolute path without whitespace"
[[ "${DEFAULT_BIND}" != *[[:space:]]* && "${DEFAULT_BIND}" != *$'\n'* ]] || die "WEBKVM_BIND_ADDR contains whitespace"
[[ "${DEFAULT_PORT}" =~ ^[0-9]+$ && ${DEFAULT_PORT} -ge 1 && ${DEFAULT_PORT} -le 65535 ]] || die "WEBKVM_PORT must be between 1 and 65535"

export DEBIAN_FRONTEND=noninteractive

# Port already bound? Fail EARLY with a clear message instead of a
# confusing rollback after packages were installed.
if command -v ss >/dev/null 2>&1 && ss -ltnH 2>/dev/null | grep -q ":${DEFAULT_PORT} "; then
  if ! systemctl is-active --quiet webkvm 2>/dev/null; then
    die "port ${DEFAULT_PORT} is already in use by another process; free it or choose another port with WEBKVM_PORT"
  fi
fi

log "installing runtime dependencies (${PKG})"
pkg_update
pkg_install "${RUNTIME_PACKAGES[@]}" || die "runtime dependencies could not be installed"

# --no-pager: 'systemctl cat' can die with SIGPIPE (rc=141) in
# non-TTY contexts, falsely reading as "unit not found".
if systemctl --no-pager cat libvirtd.service >/dev/null 2>&1; then
  systemctl enable --now libvirtd.service
elif systemctl --no-pager cat virtqemud.service >/dev/null 2>&1; then
  systemctl enable --now virtqemud.service
else
  die "libvirt daemon service was not found after installation"
fi
install -d -m 0755 "${DATA_DIR}" "${DATA_DIR}/logs" "${DATA_DIR}/source" "${CERT_DIR}"
[[ -f "${CONFIG_PATH}" ]] && CONFIG_EXISTED=1

# ── Source of the binary ───────────────────────────────────────────────
# webkvm is distributed as a precompiled binary that embeds the frontend.
# The server only needs runtime packages (libvirt/qemu), never a toolchain.
SOURCE_BIN=""
if [[ -x "${WEBKVM_BINARY:-}" ]]; then
  SOURCE_BIN="${WEBKVM_BINARY}"
elif [[ -n "${BIN_URL}" ]]; then
  [[ "${BIN_URL}" == https://* ]] || die "WEBKVM_BINARY_URL must use HTTPS"
  [[ "${BIN_SHA256}" =~ ^[[:xdigit:]]{64}$ ]] || die "WEBKVM_BINARY_SHA256 must be a 64-character SHA-256 when downloading a binary"
  DOWNLOADED_BIN="$(mktemp /tmp/webkvm.XXXXXX)"
  log "downloading release binary"
  curl --fail --location --retry 3 --proto '=https' --tlsv1.2 "${BIN_URL}" -o "${DOWNLOADED_BIN}"
  chmod 0755 "${DOWNLOADED_BIN}"
  printf '%s  %s\n' "${BIN_SHA256}" "${DOWNLOADED_BIN}" | sha256sum --check --status || die "downloaded binary checksum mismatch"
  SOURCE_BIN="${DOWNLOADED_BIN}"
elif [[ -x "${REPO_DIR}/backend/webkvm" ]]; then
  SOURCE_BIN="${REPO_DIR}/backend/webkvm"
else
  # Try to fetch latest release binary from GitHub
  log "fetching latest release binary from GitHub..."
  RELEASE_API="https://api.github.com/repos/Slaker19/webkvm/releases/latest"
  BIN_URL=$(curl -fsSL "${RELEASE_API}" 2>/dev/null | grep -o '"browser_download_url": *"[^"]*webkvm[^"]*linux_amd64[^"]*"' | head -1 | cut -d'"' -f4 || true)
  SHA256_URL=$(curl -fsSL "${RELEASE_API}" 2>/dev/null | grep -o '"browser_download_url": *"[^"]*SHA256SUMS[^"]*"' | head -1 | cut -d'"' -f4 || true)
  if [[ -n "${BIN_URL}" && -n "${SHA256_URL}" ]]; then
    DOWNLOADED_BIN="$(mktemp /tmp/webkvm.XXXXXX)"
    log "downloading release binary from ${BIN_URL}"
    curl --fail --location --retry 3 --proto '=https' --tlsv1.2 "${BIN_URL}" -o "${DOWNLOADED_BIN}"
    chmod 0755 "${DOWNLOADED_BIN}"
    # Fetch SHA256 for this binary
    BIN_NAME=$(basename "${BIN_URL}")
    BIN_SHA256=$(curl -fsSL "${SHA256_URL}" 2>/dev/null | grep "${BIN_NAME}" | awk '{print $1}' || true)
    if [[ -n "${BIN_SHA256}" && "${BIN_SHA256}" =~ ^[[:xdigit:]]{64}$ ]]; then
      printf '%s  %s\n' "${BIN_SHA256}" "${DOWNLOADED_BIN}" | sha256sum --check --status || die "downloaded binary checksum mismatch"
    else
      log "WARNING: could not verify checksum (no SHA256SUMS in release)"
    fi
    SOURCE_BIN="${DOWNLOADED_BIN}"
  else
    die "no webkvm binary found. Provide one with WEBKVM_BINARY=<path>, WEBKVM_BINARY_URL=<https>+WEBKVM_BINARY_SHA256, or build it first with 'make dist' (the server never compiles)."
  fi
fi
[[ -x "${SOURCE_BIN}" ]] || die "binary not found: ${SOURCE_BIN}"
for command_name in curl python3 ip virsh qemu-img tar zstd; do
  command -v "${command_name}" >/dev/null || die "required command is missing: ${command_name}"
done

# ── Deploy binary + service ────────────────────────────────────────────
if [[ -f "${BIN}" ]]; then HAD_BIN=1; cp -f "${BIN}" "${PREVIOUS}"; fi
if [[ -f "${SERVICE_PATH}" ]]; then HAD_SERVICE=1; cp -f "${SERVICE_PATH}" "${SERVICE_PREVIOUS}"; fi
ROLLBACK_ARMED=1
# Upgrades may hand us the very binary that is already installed
# (WEBKVM_BINARY=/usr/local/bin/webkvm). install(1) fails on src==dst.
if [[ ! -e "${BIN}" ]] || [[ "${SOURCE_BIN}" != "${BIN}" && "$(realpath -e "${SOURCE_BIN}" 2>/dev/null)" != "$(realpath -e "${BIN}" 2>/dev/null)" ]]; then
  install -D -m 0755 "${SOURCE_BIN}" "${BIN}"
fi

# Deploy the CLI (webkvm-cli) if present next to the main binary.
CLI_SRC=""
if [[ -x "${REPO_DIR}/backend/webkvm-cli" ]]; then
  CLI_SRC="${REPO_DIR}/backend/webkvm-cli"
elif [[ -n "${BIN_URL}" ]]; then
  # If we downloaded the main binary, try to fetch the CLI from the same
  # URL base (e.g. .../webkvm -> .../webkvm-cli). Best-effort.
  cli_url="${BIN_URL%-*}-cli"
  if curl --fail --silent --location --retry 2 --proto '=https' --tlsv1.2 "${cli_url}" -o "${DOWNLOADED_BIN}.cli" 2>/dev/null; then
    CLI_SRC="${DOWNLOADED_BIN}.cli"
    chmod 0755 "${CLI_SRC}"
  fi
fi
if [[ -n "${CLI_SRC}" && -x "${CLI_SRC}" ]]; then
  install -D -m 0755 "${CLI_SRC}" "${PREFIX}/bin/webkvm-cli"
  log "CLI installed -> ${PREFIX}/bin/webkvm-cli"
fi

# Pass the initial admin password through to the backend's first boot.
ADMIN_PW_LINE=""
[[ -n "${WEBKVM_ADMIN_PASSWORD:-}" ]] && ADMIN_PW_LINE="Environment=WEBKVM_ADMIN_PASSWORD=${WEBKVM_ADMIN_PASSWORD}"

install -D -m 0644 /dev/stdin "${SERVICE_PATH}" <<EOF
[Unit]
Description=WebKVM (standalone)
After=libvirtd.service virtqemud.service virtstoraged.service virtnetworkd.service virtlogd.service network-online.target
Wants=libvirtd.service virtqemud.service virtstoraged.service virtnetworkd.service virtlogd.service network-online.target

[Service]
Type=simple
User=root
Group=root
Environment=DATA_DIR=${DATA_DIR}
Environment=REPO_DIR=${DATA_DIR}/source
Environment=BIND_ADDR=127.0.0.1
Environment=PORT=${DEFAULT_PORT}
Environment=TMPDIR=/var/tmp
Environment=WEBKVM_LOG_FILE=${DATA_DIR}/logs/backend.log
${ADMIN_PW_LINE}
WorkingDirectory=${DATA_DIR}
ExecStart=${BIN}
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
LimitNOFILE=65536
StandardOutput=journal
StandardError=journal
SyslogIdentifier=webkvm
ReadWritePaths=${DATA_DIR} /var/lib/libvirt /var/tmp

[Install]
WantedBy=multi-user.target
EOF

for unit in virtqemud.socket virtstoraged.socket virtnetworkd.socket virtlogd.socket; do
  if systemctl cat "${unit}" >/dev/null 2>&1; then
    systemctl enable --now "${unit}"
  fi
done

# ── Persist settings BEFORE the first start ────────────────────────────
# The backend reads server.bind_addr / server.port / server.tls_* from
# config.json and overrides the systemd unit env. Writing them before the
# first boot means a fresh install binds the requested port immediately
# (not the schema default 8080) and starts with HTTPS already enabled.
if [[ "${CONFIG_EXISTED}" == 0 ]]; then
  python3 - "${CONFIG_PATH}" "${DEFAULT_BIND}" "${DEFAULT_PORT}" <<'PY'
import json
import pathlib
import sys
path, bind, port = sys.argv[1], sys.argv[2], sys.argv[3]
d = {"values": {"server.bind_addr": bind, "server.port": int(port)}}
tmp = pathlib.Path(path + ".tmp")
tmp.write_text(json.dumps(d, indent=2) + "\n")
tmp.chmod(0o600)
tmp.replace(path)
PY
  log "wrote initial settings: bind=${DEFAULT_BIND} port=${DEFAULT_PORT}"
else
  log "preserving existing persistent server settings"
fi

# ── HTTPS ──────────────────────────────────────────────────────────────
if [[ "${HTTPS}" == "yes" ]]; then
  gen_self_signed
  persist_setting "server.tls_cert" "${CERT_DIR}/webkvm.crt"
  persist_setting "server.tls_key" "${CERT_DIR}/webkvm.key"
  if [[ -n "${TLS_DOMAIN}" ]]; then
    persist_setting "server.tls_domain" "${TLS_DOMAIN}"
  fi
fi

systemctl daemon-reload
systemctl enable "${SERVICE}"
systemctl restart "${SERVICE}"

# ── Networks: NAT + bridge to the real LAN ─────────────────────────────
if [[ -x "${SETUP_NETWORK}" && -n "${NETWORK_MODE}" && "${NETWORK_MODE}" != "none" ]]; then
  log "wiring networks (mode=${NETWORK_MODE})..."
  args=()
  case "${NETWORK_MODE}" in
    nat) args+=(--nat) ;;
    bridge) args+=(--bridge) ;;
    both) args+=(--both) ;;
  esac
  if [[ "${NETWORK_MODE}" == "both" || "${NETWORK_MODE}" == "bridge" ]]; then
    if [[ "${BRIDGE_DHCP}" == "false" || -n "${BRIDGE_STATIC_IP}" ]]; then
      export BRIDGE_DHCP=false BRIDGE_STATIC_IP BRIDGE_STATIC_GW BRIDGE_STATIC_DNS
    else
      export BRIDGE_DHCP=true
    fi
  fi
  bash "${SETUP_NETWORK}" "${args[@]}" || log "network setup reported an error (see above); the service is already running"
fi

# ── Firewall: open the web UI port when a firewall is active ──────────
open_firewall_port() {
  local port="$1"
  if command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active --quiet firewalld; then
    firewall-cmd --permanent --add-port="${port}/tcp" >/dev/null 2>&1 && firewall-cmd --reload >/dev/null 2>&1 \
      && log "firewalld: opened port ${port}/tcp"
  elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw allow "${port}/tcp" >/dev/null 2>&1 && log "ufw: allowed ${port}/tcp"
  else
    log "no active firewalld/ufw detected; nothing to open for port ${port}"
  fi
}
open_firewall_port "${DEFAULT_PORT}"

# ── Health check ───────────────────────────────────────────────────────
HEALTH_PORT="$(python3 - "${CONFIG_PATH}" "${DEFAULT_PORT}" <<'PY'
import json, pathlib, sys
try:
    values = json.loads(pathlib.Path(sys.argv[1]).read_text()).get("values", {})
    port = int(values.get("server.port", sys.argv[2]))
    print(port if 1 <= port <= 65535 else sys.argv[2])
except Exception:
    print(sys.argv[2])
PY
)"
PROTO="http"
if [[ "${HTTPS}" == "yes" ]]; then PROTO="https"; fi
HEALTH_FILE="$(mktemp --tmpdir webkvm-health.XXXXXX)"
chmod 0600 "${HEALTH_FILE}"
ok=0
for _ in $(seq 1 30); do
  if curl -kfsS --max-time 2 "${PROTO}://127.0.0.1:${HEALTH_PORT}/api/health" >"${HEALTH_FILE}" 2>/dev/null; then ok=1; break; fi
  sleep 1
done
if [[ "${ok}" != 1 ]]; then
  journalctl -u "${SERVICE}" -n 80 --no-pager >&2 || true
  die "health check failed; previous binary was restored when available"
fi

systemctl is-active --quiet "${SERVICE}" || die "webkvm service is not active"
virsh -c qemu:///system list --all >/dev/null || die "libvirt qemu:///system is unavailable"
virsh -c qemu:///system pool-list --all >/dev/null || die "libvirt storage pools are unavailable"

INSTALL_SUCCEEDED=1
rm -f -- "${HEALTH_FILE}"

# ── Summary ────────────────────────────────────────────────────────────
lan_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo ""
bold "=== webkvm installed successfully ==="
echo ""
echo "  Web UI:  ${PROTO}://${lan_ip}:${HEALTH_PORT}"
echo "           ${PROTO}://localhost:${HEALTH_PORT}"
if [[ "${HTTPS}" == "yes" ]]; then
  echo "  Certificate: ${CERT_DIR}/webkvm.crt  (download it from the UI at /api/system/cert and trust it to remove the warning)"
  if [[ -n "${TLS_DOMAIN}" ]]; then
    echo "  Domain:   ${TLS_DOMAIN} (Let's Encrypt automatic — fallback to self-signed if unreachable)"
  fi
fi
if [[ -f "${DATA_DIR}/admin-password.initial" ]]; then
  echo "  Admin password: $(cat "${DATA_DIR}/admin-password.initial")"
fi
echo ""
echo "  Networks:"
echo "    - NAT: 192.168.122.0/24 (VMs reach the Internet through the host)"
if [[ "${NETWORK_MODE}" == "both" || "${NETWORK_MODE}" == "bridge" ]]; then
  echo "    - Bridge br0 (macvlan): VMs on the real LAN with their own IP"
fi
echo ""
echo "  Commands:"
echo "    systemctl status webkvm   # service status"
echo "    journalctl -u webkvm -f   # follow logs"
echo ""