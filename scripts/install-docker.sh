#!/usr/bin/env bash
# webkvm Docker one-liner installer - clones repo to /opt/webkvm-repo and
# installs the Docker-based deployment from there (see docs/DOCKER.md).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/<release-tag>/scripts/install-docker.sh -o /tmp/webkvm-docker-install.sh && sudo bash /tmp/webkvm-docker-install.sh
#   (use a release tag such as v0.1.6, not "main", and read the script first)
#
# The repo is checked out at a RELEASE TAG (WEBKVM_INSTALL_REF, default: the
# latest GitHub release), never at the moving main branch, because the
# installer it contains runs as root. Set WEBKVM_INSTALL_REF=<tag-or-commit>
# to pin explicitly. GitHub source archives have no published checksum, so
# the no-git fallback is only as trustworthy as TLS to github.com; install
# git to get a verified clone.
#
# After install, update anytime with:
#   cd /opt/webkvm-repo && git fetch --tags && git checkout <new-tag> && docker compose pull && docker compose up -d
set -Eeuo pipefail

REPO_URL="${WEBKVM_INSTALL_REPO:-https://github.com/Slaker19/webkvm}"
# WEBKVM_INSTALL_BRANCH is kept as a legacy alias for WEBKVM_INSTALL_REF.
BRANCH="${WEBKVM_INSTALL_REF:-${WEBKVM_INSTALL_BRANCH:-}}"
REPO_DIR="/opt/webkvm-repo"

LOG_FILE="${WEBKVM_INSTALL_LOG:-/var/log/webkvm-docker-install.log}"

red()   { printf "\033[31m%s\033[0m\n" "$*"; }

log() { printf "[webkvm-docker] %s\n" "$*"; printf "[webkvm-docker] %s\n" "$*" >>"${LOG_FILE}" 2>/dev/null || true; }

log "=== webkvm Docker one-liner installer ==="

for cmd in curl tar; do
  command -v "${cmd}" >/dev/null || { red "ERROR: ${cmd} is required but not installed."; exit 1; }
done
if [[ -z "${BRANCH}" ]]; then
  api_repo="${REPO_URL#https://github.com/}"
  BRANCH="$(curl -fsSL --retry 3 --proto '=https' --tlsv1.2 "https://api.github.com/repos/${api_repo%/}/releases/latest" 2>/dev/null | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4 || true)"
  [[ -n "${BRANCH}" ]] || { red "ERROR: could not determine the latest release tag; set WEBKVM_INSTALL_REF=<tag>"; exit 1; }
fi
[[ "${BRANCH}" =~ ^[A-Za-z0-9._/-]+$ ]] || { red "ERROR: invalid ref ${BRANCH}"; exit 1; }
HAVE_GIT=0
command -v git >/dev/null 2>&1 && HAVE_GIT=1

if [[ -d "${REPO_DIR}/.git" ]]; then
  log "Updating existing repo at ${REPO_DIR}..."
  cd "${REPO_DIR}"
  git fetch --depth 1 origin "${BRANCH}" >>"${LOG_FILE}" 2>&1
  git reset --hard FETCH_HEAD >>"${LOG_FILE}" 2>&1
else
  if [[ "${HAVE_GIT}" == 1 ]]; then
    log "Cloning webkvm (${BRANCH}) to ${REPO_DIR}..."
    git clone --depth 1 -b "${BRANCH}" "${REPO_URL}" "${REPO_DIR}" >>"${LOG_FILE}" 2>&1 || { red "ERROR: could not clone ${REPO_URL}"; exit 1; }
  else
    TAR_URL="${REPO_URL%/}/archive/${BRANCH}.tar.gz"
    log "Downloading webkvm (${BRANCH}) tarball (no git; archive is NOT checksum-verified, install git for a verified clone)..."
    mkdir -p "${REPO_DIR}"
    TAR_TMP="$(mktemp /tmp/webkvm-src.XXXXXX.tar.gz)"
    curl -fsSL --retry 3 --proto '=https' --tlsv1.2 "${TAR_URL}" -o "${TAR_TMP}" >>"${LOG_FILE}" 2>&1 || { rm -f "${TAR_TMP}"; red "ERROR: could not download ${TAR_URL}"; exit 1; }
    tar xzf "${TAR_TMP}" -C "${REPO_DIR}" --strip-components=1 >>"${LOG_FILE}" 2>&1 || { rm -f "${TAR_TMP}"; red "ERROR: could not extract ${TAR_URL}"; exit 1; }
    rm -f "${TAR_TMP}"
  fi
fi

INSTALLER="${REPO_DIR}/packaging/docker/install.sh"
[[ -x "${INSTALLER}" ]] || { red "ERROR: installer not found at ${INSTALLER}"; exit 1; }

bash "${INSTALLER}"

log "Done. Repo kept at ${REPO_DIR} for future updates."
log "To update later: cd ${REPO_DIR} && git fetch --tags && git checkout <new-tag> && docker compose pull && docker compose up -d"
