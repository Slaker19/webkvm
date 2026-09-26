#!/usr/bin/env bash
# nfpm postinstall — runs on fresh install AND upgrade.
#
# Fresh install (no config.json yet) seeds AND wires the host:
#   1. /opt/webkvm (+logs) and a minimal config.json (bind 0.0.0.0:8080).
#      config.json values win over the unit's BIND_ADDR default (see
#      backend/cmd/server/main.go), so the UI listens on the LAN.
#   2. libvirt daemon enabled+started (idempotent; re-runs on upgrade too).
#   3. self-signed TLS cert generated + persisted (fresh only, never
#      overwrite an existing pair).
#   4. Incus best-effort init (fresh only, never fatal).
# Upgrade: data/config/certs are NEVER touched; nothing is re-seeded.
#
# Security design rules:
#   - fixed strings only: no user input, no $1 handling, no eval, no curl.
#   - every optional step ends in `|| true` + warning: a quirky environment
#     must NEVER leave dpkg in a half-configured (broken) state.
#   - secrets: the admin password is created by the backend on first boot;
#     this script only prints WHERE it will appear, never a value.
#   - file modes: config.json 0600, TLS key 0600 (umask 077 at creation),
#     crt 0644. /etc/default/webkvm (a dpkg conffile) is NEVER edited here.
set -e
DATA_DIR="/opt/webkvm"
CONFIG="${DATA_DIR}/config.json"
CERT_DIR="${DATA_DIR}/certs"
CRT="${CERT_DIR}/webkvm.crt"
KEY="${CERT_DIR}/webkvm.key"
HELPER="/usr/share/webkvm/setup-network.sh"

# json_set <file> <key> <value>: set values.<key> only when absent (no
# clobber), atomically via tmp+replace. Never fatal.
json_set() {
  python3 - "$1" "$2" "$3" <<'PY' 2>/dev/null || { echo "warning: could not persist $2" >&2; return 0; }
import json, pathlib, sys
path, key, value = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
try:
    d = json.loads(path.read_text())
except Exception:
    d = {}
vals = d.get("values")
if not isinstance(vals, dict):
    vals = {}
    d["values"] = vals
if key not in vals:
    vals[key] = value
    tmp = pathlib.Path(str(path) + ".tmp")
    tmp.write_text(json.dumps(d, indent=2) + "\n")
    tmp.chmod(0o600)
    tmp.replace(path)
PY
}

# Best-effort LAN source IP; empty when undetectable.
lan_ip() {
  ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}' | head -1
}

FRESH=0
install -d -m 0755 "${DATA_DIR}" "${DATA_DIR}/logs" /etc/webkvm
if [ ! -f "${CONFIG}" ]; then
  FRESH=1
  python3 - "${CONFIG}" <<'PY' 2>/dev/null || echo "warning: could not seed ${CONFIG}" >&2
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
d = {"values": {"server.bind_addr": "0.0.0.0", "server.port": 8080}}
tmp = pathlib.Path(str(path) + ".tmp")
tmp.write_text(json.dumps(d, indent=2) + "\n")
tmp.chmod(0o600)
tmp.replace(path)
PY
fi

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  # --- libvirt: enable+start (idempotent; mirrors standalone/install.sh) ---
  LIBVIRT_SVC=""
  if systemctl --no-pager cat libvirtd.service >/dev/null 2>&1; then
    LIBVIRT_SVC="libvirtd.service"
  elif systemctl --no-pager cat virtqemud.service >/dev/null 2>&1; then
    LIBVIRT_SVC="virtqemud.service"
  fi
  if [ -n "${LIBVIRT_SVC}" ]; then
    systemctl enable --now "${LIBVIRT_SVC}" >/dev/null 2>&1 \
      || echo "warning: could not enable ${LIBVIRT_SVC}; VMs will not work until libvirt runs" >&2
    for unit in virtqemud.socket virtstoraged.socket virtnetworkd.socket virtlogd.socket virtnodedevd.socket; do
      systemctl enable --now "${unit}" >/dev/null 2>&1 || true
    done
  else
    echo "warning: no libvirtd/virtqemud unit found; VMs will not work until libvirt runs" >&2
  fi

  if [ "${FRESH}" -eq 1 ]; then
    # --- self-signed TLS cert (fresh only, never overwrite) ---
    if [ ! -s "${CRT}" ] || [ ! -s "${KEY}" ]; then
      if command -v openssl >/dev/null 2>&1; then
        hn="$(printf '%s' "${HOSTNAME:-webkvm}" | tr -c 'A-Za-z0-9.-' '-' | sed 's/-\{2,\}/-/g;s/^-//;s/-$//')"
        [ -n "${hn}" ] || hn="webkvm"
        san="DNS:webkvm,DNS:localhost,DNS:${hn}.local,IP:127.0.0.1"
        lip="$(lan_ip)"
        [ -n "${lip}" ] && san="${san},IP:${lip}"
        install -d -m 0755 "${CERT_DIR}"
        if ( umask 077 && openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
              -keyout "${KEY}" -out "${CRT}" \
              -subj "/O=webkvm/CN=webkvm" -addext "subjectAltName=${san}" 2>/dev/null ); then
          chmod 0600 "${KEY}"
          chmod 0644 "${CRT}"
        else
          echo "warning: self-signed certificate generation failed; UI will serve plain HTTP" >&2
          rm -f "${KEY}" "${CRT}"
        fi
      else
        echo "warning: openssl not found; UI will serve plain HTTP" >&2
      fi
    fi
    if [ -s "${CRT}" ] && [ -s "${KEY}" ]; then
      json_set "${CONFIG}" "server.tls_cert" "${CRT}"
      json_set "${CONFIG}" "server.tls_key" "${KEY}"
    fi

    # --- Incus best-effort (fresh only, never fatal) ---
    if command -v incus >/dev/null 2>&1; then
      systemctl enable --now incus >/dev/null 2>&1 || true
      # Unprivileged containers need a root subuid/subgid range; append
      # only when the exact line is missing (never duplicate, never edit).
      if ! grep -qs "^root:1000000:1000000000" /etc/subuid 2>/dev/null; then
        printf '%s\n' "root:1000000:1000000000" >> /etc/subuid 2>/dev/null || true
        printf '%s\n' "root:1000000:1000000000" >> /etc/subgid 2>/dev/null || true
      fi
      if ! incus admin init --auto >/dev/null 2>&1 && ! incus info >/dev/null 2>&1; then
        echo "warning: incus is installed but 'incus admin init --auto' failed; containers need manual init" >&2
      else
        incus profile set default security.nesting=true >/dev/null 2>&1 || true
      fi
    fi
  fi

  systemctl daemon-reload 2>/dev/null || echo "warning: systemctl daemon-reload failed" >&2
  systemctl enable webkvm.service >/dev/null 2>&1 || true
  # On upgrade, restart so the new binary takes over; on fresh install,
  # start it. Either way the admin password (if generated) lands in
  # ${DATA_DIR}/admin-password.initial on first boot.
  systemctl restart webkvm.service 2>/dev/null || systemctl start webkvm.service 2>/dev/null \
    || echo "warning: webkvm.service did not start; inspect with: journalctl -u webkvm" >&2

  # --- honest final report (computed live, true on fresh AND upgrade) ---
  LIP="$(lan_ip)"; [ -n "${LIP}" ] || LIP="<this-host>"
  SCHEME="http"
  TLS_NOTE=" (plain HTTP — no TLS certificate configured; set server.tls_cert/server.tls_key on the Settings page for HTTPS)"
  if [ -s "${CRT}" ] && [ -s "${KEY}" ]; then
    SCHEME="https"
    TLS_NOTE=" (self-signed certificate)"
  fi
  LIBVIRT_STATE="inactive"
  if [ -n "${LIBVIRT_SVC}" ] && systemctl is-active --quiet "${LIBVIRT_SVC}" 2>/dev/null; then
    LIBVIRT_STATE="active (${LIBVIRT_SVC})"
  fi
  INCUS_STATE="absent"
  if command -v incus >/dev/null 2>&1; then
    if incus info >/dev/null 2>&1; then INCUS_STATE="ready"; else INCUS_STATE="needs-init"; fi
  fi
  echo "webkvm installed — UI at ${SCHEME}://${LIP}:8080${TLS_NOTE}"
  echo "  libvirt: ${LIBVIRT_STATE} | incus: ${INCUS_STATE}"
  echo "  initial admin password: ${DATA_DIR}/admin-password.initial (created on first boot — save it, then delete the file)"
  echo "  service: systemctl status webkvm | logs: journalctl -u webkvm -f"
  echo "Next steps for fully working VMs:"
  echo "  1) Host network: sudo ${HELPER}   (preview first: WEBKVM_DETECT_ONLY=1 sudo ${HELPER}; help: sudo ${HELPER} --help)"
  echo "  2) Open TCP 8080 in the host firewall if needed (ufw / firewalld)"
else
  echo "webkvm installed (no systemd detected — start /usr/local/bin/webkvm manually with DATA_DIR=${DATA_DIR})"
  echo "Then wire the host yourself: enable libvirtd, provide server.tls_cert/server.tls_key for HTTPS, run ${HELPER} for VM networking."
fi
