#!/usr/bin/env bash
set -Eeuo pipefail
DATA_DIR="${WEBKVM_DATA_DIR:-/opt/webkvm}"
BIN="${WEBKVM_PREFIX:-/usr/local}/bin/webkvm"
# setup-network.sh defaults to a bridge named "br0", but reuses an
# existing bridge under its own name when install.sh was pointed at one
# (BRIDGE_MASTER). Set WEBKVM_BRIDGE_NAME to match if you customized it,
# or the macvlan/bridge interfaces below won't be found and removed.
BRIDGE_NAME="${WEBKVM_BRIDGE_NAME:-br0}"
[[ "${EUID}" -eq 0 ]] || { echo 'run as root' >&2; exit 1; }

case "${DATA_DIR}" in
  /opt/webkvm|/opt/webkvm/*) ;;
  *) echo "refusing unsafe WEBKVM_DATA_DIR: ${DATA_DIR}" >&2; exit 1 ;;
esac

echo "[1/4] stopping service"
systemctl disable --now webkvm.service 2>/dev/null || true
rm -f /etc/systemd/system/webkvm.service \
  /etc/systemd/system/webkvm.service.previous \
  "${BIN}" "${BIN}.previous" \
  /usr/local/bin/webkvm-cli /usr/local/bin/webkvm-cli.previous 2>/dev/null || true
systemctl daemon-reload

# ── networks ───────────────────────────────────────────────────────────
if [[ "${PURGE_NETWORKS:-0}" == 1 ]]; then
  echo "[2/4] removing libvirt networks and bridges"
  for net in br0-bridge default; do
    virsh net-autostart --disable "$net" 2>/dev/null || true
    virsh net-destroy "$net" 2>/dev/null || true
    virsh net-undefine "$net" 2>/dev/null || true
  done
  # Fallback: virbr* devices can remain if libvirtd was already stopped
  for br in virbr0 virbr1 virbr0-nic; do
    if ip link show "$br" >/dev/null 2>&1; then
      ip link set "$br" down 2>/dev/null || true
      ip link del "$br" 2>/dev/null || true
      echo "  - bridge $br removed (fallback)"
    fi
  done
  # bridge + macvlan slave created by setup-network.sh (macvlan mode)
  br="${BRIDGE_NAME}"
  mv="mv-${BRIDGE_NAME}"
  if [ -d "/sys/class/net/${br}/bridge" ]; then
    ip link set "$br" down 2>/dev/null || true
    ip link del "$br" 2>/dev/null || true
    echo "  - bridge $br removed"
  fi
  if [ -d "/sys/class/net/${mv}" ]; then
    ip link del "$mv" 2>/dev/null || true
    echo "  - macvlan slave $mv removed"
  fi
  # systemd-networkd configs + service created by setup-network.sh
  shopt -s nullglob 2>/dev/null || true
  for f in /etc/systemd/network/*"${BRIDGE_NAME}"* /etc/systemd/network/mv-*; do
    grep -qF "# Managed by webkvm" "$f" 2>/dev/null || continue
    rm -f "$f" "$f.bak" 2>/dev/null || true
    echo "  - removed $f"
  done
  for f in /etc/systemd/system/webkvm-mv-"${BRIDGE_NAME}"@*.service /etc/sysctl.d/90-webkvm-bridge.conf; do
    if [ -f "$f" ] && grep -qF "webkvm" "$f" 2>/dev/null; then
      rm -f "$f" 2>/dev/null || true
      echo "  - removed $f"
    fi
  done
  systemctl daemon-reload 2>/dev/null || true
  # firewall port opened by installer
  if command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active --quiet firewalld 2>/dev/null; then
    firewall-cmd --permanent --remove-port=8080/tcp >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    echo "  - firewalld: closed 8080/tcp"
  fi
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw delete allow 8080/tcp >/dev/null 2>&1 || true
    echo "  - ufw: removed 8080/tcp"
  fi
else
  echo "[2/4] keeping libvirt networks/bridges (use PURGE_NETWORKS=1 to remove)"
fi

# ── storage pools (only with PURGE_DATA) ───────────────────────────────
if [[ "${PURGE_DATA:-0}" == 1 ]]; then
  echo "[3/4] removing storage pools (data purge)"
  for pool in webkvm-disks ISOS; do
    virsh pool-destroy "$pool" 2>/dev/null || true
    virsh pool-undefine "$pool" 2>/dev/null || true
  done
  rm -rf -- "${DATA_DIR}"
  echo "  - removed ${DATA_DIR}"
else
  echo "[3/4] kept data at ${DATA_DIR}; use PURGE_DATA=1 to remove it"
fi

# ── runtime packages: intentionally NOT removed ────────────────────────
# install.sh's runtime dependencies (libvirt, qemu, swtpm, ovmf, dnsmasq,
# virt-install, openssl, ...) are shared system/virtualization tooling,
# not packages webkvm owns — the same libvirt/qemu install could already
# be in use by virt-manager, hand-created VMs, or anything else on this
# host before webkvm ever ran. There is no reliable way to force-remove
# them without risking exactly that: a previous PURGE_PACKAGES=1 run
# confirmed dnf computing a 468-package cascade removal on Fedora just
# from including "openssl" in the list (nearly everything depends on
# it). Uninstalling webkvm removes what webkvm itself owns — the
# binary, service, and (opt-in) its own data/networks — and leaves the
# host's package set alone.
if [[ "${PURGE_PACKAGES:-0}" == 1 ]]; then
  echo "[4/4] PURGE_PACKAGES is no longer supported: runtime packages (libvirt, qemu, ...) are shared system tooling, not webkvm's own — removing them risks breaking anything else on this host that also uses them. Uninstall them yourself with your package manager if you're sure nothing else needs them."
else
  echo "[4/4] done (runtime packages, e.g. libvirt/qemu, are left installed — they are shared system tooling, not webkvm's own)"
fi

echo "  PURGE_DATA=${PURGE_DATA:-0}  PURGE_NETWORKS=${PURGE_NETWORKS:-0}"
