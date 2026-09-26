#!/usr/bin/env bash
# Prints "<proto> <port>" for wherever the backend is ACTUALLY listening,
# so health checks and "open your browser at..." hints never hardcode
# 8080/http regardless of what the operator configured.
#
# server.bind_addr/server.port/server.tls_cert are persisted to
# {DATA_DIR}/config.json (see backend/internal/configstore) and override
# the schema defaults (127.0.0.1:8080, no TLS) as soon as an operator
# picks a custom port or enables HTTPS — from the installer prompt, the
# Settings UI, or a hand-edited config. Falls back to "http 8080" when
# the file is missing, unreadable, or not written yet (fresh install,
# before the backend's first boot).
#
# Usage: detect-webkvm-endpoint.sh [DATA_DIR]   (default: /opt/webkvm)
# config.json is 0600 root:root, so this typically needs to run as root
# (or via sudo) to read anything beyond the fallback.
set -euo pipefail
DATA_DIR="${1:-/opt/webkvm}"
CONFIG="${DATA_DIR}/config.json"

python3 - "${CONFIG}" <<'PY' 2>/dev/null || echo "http 8080"
import json, pathlib, sys

path = sys.argv[1]
try:
    values = json.loads(pathlib.Path(path).read_text()).get("values", {})
    port = int(values.get("server.port", 8080))
    if not (1 <= port <= 65535):
        port = 8080
    proto = "https" if values.get("server.tls_cert") else "http"
    print(f"{proto} {port}")
except Exception:
    print("http 8080")
PY
