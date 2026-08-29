#!/usr/bin/env bash
# webkvm Docker one-liner installer - clones repo to /opt/webkvm-repo and
# installs the Docker-based deployment from there (see docs/DOCKER.md).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-docker.sh -o /tmp/webkvm-docker-install.sh && sudo bash /tmp/webkvm-docker-install.sh
#
# After install, update anytime with:
#   cd /opt/webkvm-repo && git pull && docker compose pull && docker compose up -d
set -Eeuo pipefail

REPO_URL="${WEBKVM_INSTALL_REPO:-https://github.com/Slaker19/webkvm}"
BRANCH="${WEBKVM_INSTALL_BRANCH:-main}"
REPO_DIR="/opt/webkvm-repo"

LOG_FILE="${WEBKVM_INSTALL_LOG:-/var/log/webkvm-docker-install.log}"

red()   { printf "\033[31m%s\033[0m\n" "$*"; }

log() { printf "[webkvm-docker] %s\n" "$*"; printf "[webkvm-docker] %s\n" "$*" >>"${LOG_FILE}" 2>/dev/null || true; }

log "=== webkvm Docker one-liner installer ==="

for cmd in curl tar; do
  command -v "${cmd}" >/dev/null || { red "ERROR: ${cmd} is required but not installed."; exit 1; }
done
HAVE_GIT=0
command -v git >/dev/null 2>&1 && HAVE_GIT=1

if [[ -d "${REPO_DIR}/.git" ]]; then
  log "Updating existing repo at ${REPO_DIR}..."
  cd "${REPO_DIR}"
  git fetch origin "${BRANCH}" >>"${LOG_FILE}" 2>&1
  git reset --hard "origin/${BRANCH}" >>"${LOG_FILE}" 2>&1
else
  if [[ "${HAVE_GIT}" == 1 ]]; then
    log "Cloning webkvm (${BRANCH}) to ${REPO_DIR}..."
    git clone --depth 1 -b "${BRANCH}" "${REPO_URL}" "${REPO_DIR}" >>"${LOG_FILE}" 2>&1 || { red "ERROR: could not clone ${REPO_URL}"; exit 1; }
  else
    TAR_URL="${REPO_URL%/}/archive/refs/heads/${BRANCH}.tar.gz"
    log "Downloading webkvm (${BRANCH}) tarball (no git)..."
    mkdir -p "${REPO_DIR}"
    curl -fsSL --retry 3 "${TAR_URL}" | tar xz -C "${REPO_DIR}" --strip-components=1 >>"${LOG_FILE}" 2>&1 || { red "ERROR: could not download ${TAR_URL}"; exit 1; }
  fi
fi

INSTALLER="${REPO_DIR}/packaging/docker/install.sh"
[[ -x "${INSTALLER}" ]] || { red "ERROR: installer not found at ${INSTALLER}"; exit 1; }

bash "${INSTALLER}"

log "Done. Repo kept at ${REPO_DIR} for future updates."
log "To update later: cd ${REPO_DIR} && git pull && docker compose pull && docker compose up -d"
