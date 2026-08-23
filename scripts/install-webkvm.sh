#!/usr/bin/env bash
# webkvm one-liner installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-webkvm.sh | sudo bash
#
# Clones the repository and runs the standalone installer.
#
# Clones the webkvm repository and runs the standalone installer
# (packaging/standalone/install.sh), which supports Debian/Ubuntu (apt),
# Fedora/RHEL (dnf) and Arch (pacman), installs runtime packages
# (libvirt/qemu) and deploys the precompiled binary. The server never
# compiles — no Go/Node/build toolchain required. Offers HTTPS
# (self-signed + optional Let's Encrypt domain) and wires the default
# NAT network plus an optional macvlan bridge.
#
# Interactive when run on a TTY; uses sensible defaults when piped.
# Every setting is overridable with WEBKVM_* / NETWORK_MODE / BRIDGE_*
# environment variables (passed through with sudo -E).
set -Eeuo pipefail

REPO_URL="${WEBKVM_INSTALL_REPO:-https://github.com/Slaker19/webkvm}"
BRANCH="${WEBKVM_INSTALL_BRANCH:-main}"

# Non-technical users: never work in /tmp (often mounted noexec) and
# ALWAYS leave a persistent install log they can share when asking for
# help. The workdir survives failures; it is cleaned only on success.
LOG_FILE="${WEBKVM_INSTALL_LOG:-/var/log/webkvm-install.log}"
WORK_BASE="${WEBKVM_WORKDIR_BASE:-/var/tmp}"
TMP_ROOT="$(mktemp -d "${WORK_BASE}/webkvm-install.XXXXXX")"
SUCCESS=0
cleanup() {
  if [[ "${SUCCESS}" == 1 ]]; then
    rm -rf "${TMP_ROOT}"
  else
    printf '\n%s\n' "Install files kept for debugging at: ${TMP_ROOT}"
    [[ -f "${LOG_FILE}" ]] && printf '%s\n' "Full log: ${LOG_FILE} (attach it when asking for help)"
  fi
}
trap cleanup EXIT

red()   { printf "\033[31m%s\033[0m\n" "$*"; }
green() { printf "\033[32m%s\033[0m\n" "$*"; }

# Persist every line of the install output (best effort: needs root or a
# writable /var/log; skipped otherwise).
if [[ "${EUID}" == 0 || -w "$(dirname "${LOG_FILE}")" ]] 2>/dev/null; then
  exec > >(tee -a "${LOG_FILE}") 2>&1
fi

echo ""
echo "=== webkvm one-liner installer ==="
echo ""

for cmd in curl tar; do
  command -v "${cmd}" >/dev/null || { red "ERROR: ${cmd} is required but not installed."; exit 1; }
done
HAVE_GIT=0
command -v git >/dev/null 2>&1 && HAVE_GIT=1

mkdir -p "${TMP_ROOT}/webkvm"
if [[ "${HAVE_GIT}" == 1 ]]; then
  echo "Cloning webkvm (${BRANCH})..."
  if ! git clone --depth 1 -b "${BRANCH}" "${REPO_URL}" "${TMP_ROOT}/webkvm"; then
    red "ERROR: could not clone ${REPO_URL}. Check the URL and your network,"
    red "or set WEBKVM_INSTALL_REPO (and optionally WEBKVM_INSTALL_BRANCH) and retry."
    exit 1
  fi
else
  # Sin git: tarball del branch.
  TAR_URL="${REPO_URL%/}/archive/refs/heads/${BRANCH}.tar.gz"
  echo "Downloading webkvm (${BRANCH}) tarball (no git)..."
  if ! curl -fsSL --retry 3 "${TAR_URL}" | tar xz -C "${TMP_ROOT}/webkvm" --strip-components=1; then
    red "ERROR: could not download ${TAR_URL}. Set WEBKVM_INSTALL_REPO and retry."
    exit 1
  fi
fi

cd "${TMP_ROOT}/webkvm"
INSTALLER="packaging/standalone/install.sh"
[[ -x "${INSTALLER}" ]] || INSTALLER="packaging/standalone/install.sh"

finish_ok() { SUCCESS=1; }

if [[ "${EUID}" == 0 ]]; then
  bash "${INSTALLER}"
  finish_ok
else
  # Run as a normal user: escalate. NEVER hide stderr — a silent failure
  # here looks exactly like "the installer does nothing".
  if ! sudo -v; then
    red "ERROR: se necesitan privilegios de root para instalar."
    red "Ejecuta: curl -fsSL <url> | sudo bash   — o revisa que tu usuario tenga sudo."
    exit 1
  fi
  sudo -E bash "${INSTALLER}"
  finish_ok
fi