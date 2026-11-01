#!/usr/bin/env bash
# webkvm setup.sh (Compatibility Wrapper)
# Redirects to the canonical standalone installer in packaging/standalone/install.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CANONICAL_INSTALLER="${SCRIPT_DIR}/packaging/standalone/install.sh"

if [[ -f "${CANONICAL_INSTALLER}" ]]; then
  echo "==> Routing setup.sh to canonical installer (${CANONICAL_INSTALLER})..."
  exec bash "${CANONICAL_INSTALLER}" "$@"
else
  echo "ERROR: ${CANONICAL_INSTALLER} not found." >&2
  exit 1
fi
