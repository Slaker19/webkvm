#!/usr/bin/env bash
# webkvm updater — installs the latest release, or rebuilds from a checkout.
#
# Usage:
#   sudo ./update.sh              # install the latest verified GitHub release
#   sudo ./update.sh --source     # git pull + rebuild WEBKVM_REPO_DIR
#
# Run `./update.sh --help` for the full argument and environment list.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Where the git checkout lives — only used by --source. Installed outside the
# tree (as /usr/local/bin/webkvm-update) SCRIPT_DIR/../.. would resolve to
# /usr/local, so callers — the in-app updater above all — pass
# WEBKVM_REPO_DIR explicitly.
REPO_DIR="${WEBKVM_REPO_DIR:-$(cd "${SCRIPT_DIR}/../.." && pwd)}"
BIN="${WEBKVM_BIN:-}"
SERVICE="webkvm.service"
SOURCE_MODE=0

log()  { printf '[webkvm-update] %s\n' "$*"; }
die()  { printf '[webkvm-update] ERROR: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'USAGE'
webkvm updater — installs the latest release, or rebuilds from a checkout.

Usage:
  webkvm-update            update from the latest verified GitHub release
  webkvm-update --source   update from the local checkout (git pull + build)

Environment:
  WEBKVM_REPO_DIR   checkout to rebuild from in --source mode
  WEBKVM_BIN        binary to replace (default: read from the systemd unit)
USAGE
}

# Reject anything we do not understand instead of silently falling through
# to a release update: a typo like --sources must not install a different
# build than the operator asked for.
case "${1:-}" in
  "")         ;;
  --source)   SOURCE_MODE=1 ;;
  -h|--help)  usage; exit 0 ;;
  *)          usage >&2; die "unknown argument: $1" ;;
esac
[[ $# -le 1 ]] || die "too many arguments"

# ── Preflight ──────────────────────────────────────────────────────────
[[ $EUID -eq 0 ]] || die "run as root"
command -v systemctl >/dev/null || die "systemctl not found"
systemctl list-unit-files "${SERVICE}" >/dev/null 2>&1 || die "webkvm service not found — run install.sh first"

# ── Single instance ────────────────────────────────────────────────────
# Two concurrent runs would race over the same paths: two backups (the
# second overwriting the first with an already-replaced binary), two stops
# and two renames onto ${BIN}. /run is tmpfs, so the lock never survives a
# reboot and can't go stale.
if command -v flock >/dev/null; then
  exec 9>/run/webkvm-update.lock || die "cannot create /run/webkvm-update.lock"
  flock -n 9 || die "another update is already running"
else
  log "WARNING: flock not found; cannot guard against concurrent updates"
fi

# Remove a half-downloaded release binary on every exit path, not just the
# happy one: a checksum mismatch or a failed stop used to leave it behind.
TMP=""
cleanup() {
  if [[ -n "${TMP}" && -f "${TMP}" ]]; then rm -f "${TMP}"; fi
  return 0
}
trap cleanup EXIT

# Auto-detect binary path from the running systemd unit. install.sh's
# default is PREFIX=/usr/local (i.e. /usr/local/bin/webkvm) — /opt/webkvm
# is only the DATA_DIR, never the binary location — so that's tried last,
# purely as a last-ditch guess for a nonstandard setup.
if [[ -z "${BIN}" ]]; then
  BIN="$(systemctl show "${SERVICE}" -p ExecStart --value 2>/dev/null | grep -oP 'path=\K[^ ;]+' || true)"
  [[ -x "${BIN}" ]] || BIN="/usr/local/bin/webkvm"
  [[ -x "${BIN}" ]] || BIN="/opt/webkvm/webkvm"
fi

# DATA_DIR drives both the .env written by --source and the config.json the
# health check reads its port from. Detected once, here: hardcoding
# /opt/webkvm for the health check made a non-default install probe the
# wrong port and roll back updates that had actually succeeded.
DATA_DIR="$(systemctl show "${SERVICE}" -p Environment --value 2>/dev/null | tr ' ' '\n' | sed -n 's/^DATA_DIR=//p' | head -1 || true)"
[[ -n "${DATA_DIR}" && -d "${DATA_DIR}" ]] || DATA_DIR="/opt/webkvm"

CURRENT_VER=""
if [[ -x "${BIN}" ]]; then
  CURRENT_VER=$("${BIN}" version 2>/dev/null | head -1 || echo "unknown")
fi
log "current: ${CURRENT_VER:-installed}"

# ── Fetch updates ──────────────────────────────────────────────────────
if [[ "${SOURCE_MODE}" == 1 ]]; then
  # Update from local git repo
  [[ -e "${REPO_DIR}/.git" ]] || die "not a git repo: ${REPO_DIR}"
  # Follow the branch that is actually checked out. Hardcoding `origin main`
  # would fast-forward a maintenance or feature branch onto main and install
  # code the operator never asked for.
  BRANCH="$(git -C "${REPO_DIR}" rev-parse --abbrev-ref HEAD 2>/dev/null || echo HEAD)"
  [[ "${BRANCH}" != "HEAD" ]] || die "checkout is in detached HEAD state; check out a branch before updating from source"
  UPSTREAM="$(git -C "${REPO_DIR}" rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null || true)"
  [[ -n "${UPSTREAM}" ]] || die "branch ${BRANCH} has no upstream; set one with 'git branch --set-upstream-to=origin/${BRANCH}'"
  log "pulling ${UPSTREAM} into ${BRANCH}..."
  git -C "${REPO_DIR}" pull --ff-only || die "git pull failed (local commits or a diverged branch block a fast-forward)"
  # Strip the tag's leading "v": the backend must report exactly what
  # frontend/package.json and the release assets carry (0.1.0, not v0.1.0),
  # otherwise /api/system/status disagrees with the UI and with isNewer().
  GIT_TAG="$(git -C "${REPO_DIR}" describe --tags --abbrev=0 2>/dev/null || echo "dev")"
  VERSION="${GIT_TAG#v}"
  log "rebuilding from source (version ${VERSION})..."
  (
    cd "${REPO_DIR}"
    # Frontend
    if [[ -d frontend ]]; then
      log "building frontend..."
      (cd frontend && npm ci && npm run build)
      rm -rf backend/internal/frontend/dist
      cp -r frontend/dist backend/internal/frontend/dist
    fi
    # Backend
    log "building backend..."
    (cd backend && CGO_ENABLED=1 go build -trimpath \
      -ldflags "-s -w -X main.Version=${VERSION}" -o webkvm ./cmd/server)
  )
  INSTALL_SRC="${REPO_DIR}/backend/webkvm"
  # Write version into the data dir .env so the running binary picks it up
  if [[ -d "${DATA_DIR}" ]]; then
    if grep -q '^WEBKVM_VERSION=' "${DATA_DIR}/.env" 2>/dev/null; then
      sed -i "s/^WEBKVM_VERSION=.*/WEBKVM_VERSION=${VERSION}/" "${DATA_DIR}/.env"
    else
      echo "WEBKVM_VERSION=${VERSION}" >> "${DATA_DIR}/.env"
    fi
    log "set WEBKVM_VERSION=${VERSION} in ${DATA_DIR}/.env"
  fi
else
  # Prefer a proper GitHub release + its SHA256SUMS asset (same source
  # install.sh's own fallback trusts) so the downloaded binary can
  # actually be checksum-verified before it's installed and run as root.
  TMP="$(mktemp /tmp/webkvm-update.XXXXXX)"  # removed by the EXIT trap
  RELEASE_API="https://api.github.com/repos/Slaker19/webkvm/releases/latest"
  # Raw binary asset published by `make dist`/`make release`
  # (dist/webkvm-<version>-linux_amd64). The tarball is NOT used here:
  # this script installs a binary, and installing a .tar.gz as the
  # binary would brick the service. Exclude webkvm-cli explicitly.
  BIN_URL=$(curl -fsSL "${RELEASE_API}" 2>/dev/null | grep -o '"browser_download_url": *"[^"]*webkvm-[^"]*linux_amd64[^"]*"' | grep -v 'webkvm-cli' | head -1 | cut -d'"' -f4 || true)
  if [[ -n "${BIN_URL}" ]]; then
    log "downloading release binary from ${BIN_URL}"
    curl --fail --location --retry 3 --proto '=https' --tlsv1.2 "${BIN_URL}" -o "${TMP}" || die "could not download binary"
    chmod 0755 "${TMP}"
    SHA256_URL=$(curl -fsSL "${RELEASE_API}" 2>/dev/null | grep -o '"browser_download_url": *"[^"]*SHA256SUMS[^"]*"' | head -1 | cut -d'"' -f4 || true)
    BIN_SHA256=""
    if [[ -n "${SHA256_URL}" ]]; then
      BIN_NAME=$(basename "${BIN_URL}")
      BIN_SHA256=$(curl -fsSL "${SHA256_URL}" 2>/dev/null | grep "${BIN_NAME}" | awk '{print $1}' || true)
    fi
    if [[ "${BIN_SHA256}" =~ ^[[:xdigit:]]{64}$ ]]; then
      printf '%s  %s\n' "${BIN_SHA256}" "${TMP}" | sha256sum --check --status || die "downloaded binary checksum mismatch"
      log "checksum verified"
    else
      # Fail closed: this binary runs as root.
      die "no SHA256SUMS entry found for this asset; refusing to install an unverified binary as root"
    fi
  else
    # No GitHub release with SHA256SUMS: there is nothing verifiable to
    # fetch (no binary is committed to git). Fail closed — this binary
    # runs as root.
    die "no GitHub release with SHA256SUMS found for Slaker19/webkvm. Update from a checkout with --source, or reinstall with install.sh WEBKVM_BINARY=<path>."
  fi
  INSTALL_SRC="${TMP}"
fi

# ── Backup current binary ─────────────────────────────────────────────
# Before the stop: copying a running binary is harmless, whereas a failed
# backup *after* the stop would leave the service down for good.
PREV="${BIN}.previous"
if [[ -f "${BIN}" ]]; then
  cp -f "${BIN}" "${PREV}" || die "could not back up ${BIN}; nothing was changed"
fi

# ── Stop old service ───────────────────────────────────────────────────
log "stopping service..."
systemctl stop "${SERVICE}" 2>/dev/null || true

# ── Install ────────────────────────────────────────────────────────────
# Stage beside the target and rename: `install` writing straight over ${BIN}
# could leave a truncated binary behind if it failed halfway.
NEWBIN="${BIN}.new"
if ! install -D -m 0755 "${INSTALL_SRC}" "${NEWBIN}"; then
  rm -f "${NEWBIN}"
  systemctl start "${SERVICE}" 2>/dev/null || true
  die "staging the new binary failed; ${BIN} was left untouched and the service restarted"
fi
if ! mv -f "${NEWBIN}" "${BIN}"; then
  rm -f "${NEWBIN}"
  systemctl start "${SERVICE}" 2>/dev/null || true
  die "could not move the new binary into place; the service was restarted with the old one"
fi

# ── Restart ────────────────────────────────────────────────────────────
log "restarting service..."
systemctl daemon-reload
systemctl start "${SERVICE}"

# ── Health check ───────────────────────────────────────────────────────
CONFIG_PATH="${DATA_DIR}/config.json"
DEFAULT_PORT="$(systemctl show "${SERVICE}" -p Environment --value 2>/dev/null | tr ' ' '\n' | sed -n 's/^PORT=//p' | head -1 || true)"
DEFAULT_PORT="${DEFAULT_PORT:-8080}"
HEALTH_PORT=""
# `|| HEALTH_PORT=` keeps set -e from killing the run before the health
# check: without python3 the default port below is used instead.
HEALTH_PORT="$(python3 - "${CONFIG_PATH}" "${DEFAULT_PORT}" <<'PY'
import json, pathlib, sys
try:
    values = json.loads(pathlib.Path(sys.argv[1]).read_text()).get("values", {})
    port = int(values.get("server.port", sys.argv[2]))
    print(port if 1 <= port <= 65535 else sys.argv[2])
except Exception:
    print(sys.argv[2])
PY
)" || HEALTH_PORT=""
HEALTH_PORT="${HEALTH_PORT:-${DEFAULT_PORT}}"
log "waiting for health endpoint on port ${HEALTH_PORT}..."
ok=0
for _ in $(seq 1 30); do
  if curl -kfsS --max-time 2 "https://127.0.0.1:${HEALTH_PORT}/api/health" >/dev/null 2>&1; then ok=1; break; fi
  if curl -fsS --max-time 2 "http://127.0.0.1:${HEALTH_PORT}/api/health" >/dev/null 2>&1; then ok=1; break; fi
  sleep 1
done

NEW_VER=""
if [[ -x "${BIN}" ]]; then
  NEW_VER=$("${BIN}" version 2>/dev/null | head -1 || echo "unknown")
fi

if [[ "${ok}" == 1 ]]; then
  log "update complete: ${CURRENT_VER:-?} -> ${NEW_VER:-?}"
  log "service is running"
  exit 0
fi

# ── Rollback ───────────────────────────────────────────────────────────
# Every step here is guarded. Under `set -e` a bare `install` that failed
# (read-only /usr/local, full disk, corrupt backup) aborted the script on
# the spot: no restart attempt, no error message, service left stopped —
# the worst possible outcome of the one path that exists to recover.
log "WARNING: health check failed — rolling back"
restored=0
if [[ -f "${PREV}" ]]; then
  # Stage + rename, like the install above: writing straight onto ${BIN}
  # can truncate it, and at this point there is nothing left to fall back
  # on.
  if install -m 0755 "${PREV}" "${BIN}.rollback" 2>/dev/null && mv -f "${BIN}.rollback" "${BIN}" 2>/dev/null; then
    restored=1
  else
    rm -f "${BIN}.rollback" 2>/dev/null || true
    log "ERROR: could not restore ${PREV} onto ${BIN}"
  fi
else
  log "ERROR: no backup at ${PREV} to restore"
fi

systemctl restart "${SERVICE}" 2>/dev/null || true

if [[ "${restored}" == 1 ]]; then
  die "update failed health check; restored the previous binary and restarted the service"
fi
die "update failed health check AND the rollback failed — the service may be down. Reinstall with install.sh, or restore ${PREV} onto ${BIN} by hand."
