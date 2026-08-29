#!/usr/bin/env bash
# webkvm updater — pulls latest release and reinstalls.
#
# Usage:
#   sudo ./update.sh              # update from latest GitHub release
#   sudo ./update.sh --source     # update from local repo (git pull)
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BIN="${WEBKVM_BIN:-}"
SERVICE="webkvm.service"
SOURCE_MODE=0
[[ "${1:-}" == "--source" ]] && SOURCE_MODE=1

log()  { printf '[webkvm-update] %s\n' "$*"; }
die()  { printf '[webkvm-update] ERROR: %s\n' "$*" >&2; exit 1; }

# ── Preflight ──────────────────────────────────────────────────────────
[[ $EUID -eq 0 ]] || die "run as root"
command -v systemctl >/dev/null || die "systemctl not found"
systemctl list-unit-files "${SERVICE}" >/dev/null 2>&1 || die "webkvm service not found — run install.sh first"

# Auto-detect binary path from the running systemd unit. install.sh's
# default is PREFIX=/usr/local (i.e. /usr/local/bin/webkvm) — /opt/webkvm
# is only the DATA_DIR, never the binary location — so that's tried last,
# purely as a last-ditch guess for a nonstandard setup.
if [[ -z "${BIN}" ]]; then
  BIN="$(systemctl show "${SERVICE}" -p ExecStart --value 2>/dev/null | grep -oP 'path=\K[^ ;]+' || true)"
  [[ -x "${BIN}" ]] || BIN="/usr/local/bin/webkvm"
  [[ -x "${BIN}" ]] || BIN="/opt/webkvm/webkvm"
fi

CURRENT_VER=""
if [[ -x "${BIN}" ]]; then
  CURRENT_VER=$("${BIN}" version 2>/dev/null | head -1 || echo "unknown")
fi
log "current: ${CURRENT_VER:-installed}"

# ── Fetch updates ──────────────────────────────────────────────────────
if [[ "${SOURCE_MODE}" == 1 ]]; then
  # Update from local git repo
  [[ -d "${REPO_DIR}/.git" ]] || die "not a git repo: ${REPO_DIR}"
  log "pulling latest from origin..."
  git -C "${REPO_DIR}" pull --ff-only origin main || die "git pull failed"
  log "rebuilding from source..."
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
    (cd backend && CGO_ENABLED=1 go build -trimpath -o webkvm ./cmd/server)
  )
  INSTALL_SRC="${REPO_DIR}/backend/webkvm"
  # Write version into the data dir .env so the running binary picks it up
  GIT_TAG="$(git -C "${REPO_DIR}" describe --tags --abbrev=0 2>/dev/null || echo "dev")"
  DATA_DIR="$(systemctl show "${SERVICE}" -p Environment --value 2>/dev/null | grep -o 'DATA_DIR=[^ ]*' | cut -d= -f2 || true)"
  [[ -d "${DATA_DIR}" ]] || DATA_DIR="/opt/webkvm"
  if [[ -d "${DATA_DIR}" ]]; then
    if grep -q '^WEBKVM_VERSION=' "${DATA_DIR}/.env" 2>/dev/null; then
      sed -i "s/^WEBKVM_VERSION=.*/WEBKVM_VERSION=${GIT_TAG}/" "${DATA_DIR}/.env"
    else
      echo "WEBKVM_VERSION=${GIT_TAG}" >> "${DATA_DIR}/.env"
    fi
    log "set WEBKVM_VERSION=${GIT_TAG} in ${DATA_DIR}/.env"
  fi
else
  # Prefer a proper GitHub release + its SHA256SUMS asset (same source
  # install.sh's own fallback trusts) so the downloaded binary can
  # actually be checksum-verified before it's installed and run as root.
  TMP="$(mktemp /tmp/webkvm-update.XXXXXX)"
  RELEASE_API="https://api.github.com/repos/Slaker19/webkvm/releases/latest"
  BIN_URL=$(curl -fsSL "${RELEASE_API}" 2>/dev/null | grep -o '"browser_download_url": *"[^"]*webkvm[^"]*linux_amd64[^"]*"' | head -1 | cut -d'"' -f4 || true)
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
      log "WARNING: could not verify checksum (no SHA256SUMS entry found for this asset)"
    fi
  else
    # No GitHub release published — fall back to the binary committed
    # directly in the repo (see .gitignore: backend/webkvm is
    # intentionally versioned). /releases/latest has proper "latest"
    # semantics; /tags does not, so this branch — not the tags list —
    # is what a truly-latest lookup should prefer. No checksum is
    # available on this path; the health check + rollback below is the
    # only safety net.
    TAG="$(curl -fsSL "https://api.github.com/repos/Slaker19/webkvm/tags" 2>/dev/null | grep -o '"name": *"[^"]*"' | head -1 | cut -d'"' -f4 || echo "main")"
    log "no GitHub release found; downloading binary committed at tag ${TAG} (unverified)..."
    curl --fail --location --retry 3 --proto '=https' --tlsv1.2 \
      "https://raw.githubusercontent.com/Slaker19/webkvm/${TAG}/backend/webkvm" -o "${TMP}" \
      || die "could not download binary"
    chmod 0755 "${TMP}"
  fi
  INSTALL_SRC="${TMP}"
fi

# ── Stop old service ───────────────────────────────────────────────────
log "stopping service..."
systemctl stop "${SERVICE}" 2>/dev/null || true

# ── Backup & install ──────────────────────────────────────────────────
PREV="${BIN}.previous"
[[ -f "${BIN}" ]] && cp -f "${BIN}" "${PREV}"
install -D -m 0755 "${INSTALL_SRC}" "${BIN}"

# ── Restart ────────────────────────────────────────────────────────────
log "restarting service..."
systemctl daemon-reload
systemctl start "${SERVICE}"

# ── Health check ───────────────────────────────────────────────────────
CONFIG_PATH="/opt/webkvm/config.json"
HEALTH_PORT="$(python3 - "${CONFIG_PATH}" 8080 <<'PY'
import json, pathlib, sys
try:
    values = json.loads(pathlib.Path(sys.argv[1]).read_text()).get("values", {})
    port = int(values.get("server.port", sys.argv[2]))
    print(port if 1 <= port <= 65535 else sys.argv[2])
except Exception:
    print(sys.argv[2])
PY
)"
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
else
  log "WARNING: health check failed — rolling back"
  [[ -f "${PREV}" ]] && install -D -m 0755 "${PREV}" "${BIN}"
  systemctl restart "${SERVICE}" 2>/dev/null || true
  die "update failed; restored previous binary"
fi

# Cleanup
[[ -n "${INSTALL_SRC:-}" && -f "${INSTALL_SRC}" && "${INSTALL_SRC}" == /tmp/* ]] && rm -f "${INSTALL_SRC}"
