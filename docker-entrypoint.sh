#!/usr/bin/env bash
# Docker entrypoint — makes a fresh container "just work" the same way a
# fresh `install.sh` run does: write an initial config.json before the
# first boot (bind_addr/port) and generate+register a self-signed cert if
# neither exists yet. Mirrors packaging/standalone/install.sh's
# gen_self_signed()/persist_setting(), which the compiled binary itself
# never does at runtime (it only loads an existing cert).
set -euo pipefail

DATA_DIR="${DATA_DIR:-/opt/webkvm}"
BIND_ADDR="${BIND_ADDR:-0.0.0.0}"
PORT="${PORT:-8080}"
CERT_DIR="${DATA_DIR}/certs"
CONFIG_PATH="${DATA_DIR}/config.json"

log() { printf '[entrypoint] %s\n' "$*"; }

install -d -m 0755 "${DATA_DIR}" "${CERT_DIR}"

if [ ! -f "${CONFIG_PATH}" ]; then
  log "writing initial settings: bind=${BIND_ADDR} port=${PORT}"
  python3 - "${CONFIG_PATH}" "${BIND_ADDR}" "${PORT}" <<'PY'
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
fi

persist_setting() {
  local key="$1" value="$2"
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

if [ ! -f "${CERT_DIR}/webkvm.crt" ] || [ ! -f "${CERT_DIR}/webkvm.key" ]; then
  hn="$(printf '%s' "${HOSTNAME:-webkvm}" | tr -c 'A-Za-z0-9.-' '-' | sed 's/-\{2,\}/-/g;s/^-//;s/-$//')"
  [ -n "${hn}" ] || hn="webkvm"
  san="DNS:webkvm,DNS:localhost,DNS:${hn}.local,IP:127.0.0.1"
  lan_ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
  [ -n "${lan_ip}" ] && san="${san},IP:${lan_ip}"
  log "generating self-signed certificate (SAN=${san})..."
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -keyout "${CERT_DIR}/webkvm.key" -out "${CERT_DIR}/webkvm.crt" \
    -subj "/O=webkvm/CN=webkvm" -addext "subjectAltName=${san}" 2>/dev/null
  chmod 0600 "${CERT_DIR}/webkvm.key"
  chmod 0644 "${CERT_DIR}/webkvm.crt"
  persist_setting "server.tls_cert" "${CERT_DIR}/webkvm.crt"
  persist_setting "server.tls_key" "${CERT_DIR}/webkvm.key"
fi

log "starting webkvm ($(webkvm version 2>/dev/null || echo 'unknown version'))"
exec webkvm
