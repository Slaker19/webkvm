#!/usr/bin/env bash
set -Eeuo pipefail
DATA_DIR="${WEBKVM_DATA_DIR:-/opt/webkvm}"
BIN="${WEBKVM_PREFIX:-/usr/local}/bin/webkvm"
[[ "${EUID}" -eq 0 ]] || { echo 'run as root' >&2; exit 1; }

case "${DATA_DIR}" in
  /opt/webkvm|/opt/webkvm/*) ;;
  *) echo "refusing unsafe WEBKVM_DATA_DIR: ${DATA_DIR}" >&2; exit 1 ;;
esac

echo "[1/5] stopping service"
systemctl disable --now webkvm.service 2>/dev/null || true
rm -f /etc/systemd/system/webkvm.service \
  /etc/systemd/system/webkvm.service.previous \
  "${BIN}" "${BIN}.previous" \
  /usr/local/bin/webkvm-cli /usr/local/bin/webkvm-cli.previous 2>/dev/null || true
systemctl daemon-reload

# ── networks ───────────────────────────────────────────────────────────
if [[ "${PURGE_NETWORKS:-0}" == 1 ]]; then
  echo "[2/5] removing libvirt networks and bridges"
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
  # bridge devices created by setup-network.sh (macvlan mode)
  for br in br0 mv-br0; do
    if [ -d "/sys/class/net/${br}/bridge" ]; then
      ip link set "$br" down 2>/dev/null || true
      ip link del "$br" 2>/dev/null || true
      echo "  - bridge $br removed"
    fi
    mv="mv-${br}"
    if [ -d "/sys/class/net/${mv}/bridge" ] || [ -d "/sys/class/net/${mv}" ]; then
      ip link del "$mv" 2>/dev/null || true
    fi
  done
  # systemd-networkd configs + service created by setup-network.sh
  shopt -s nullglob 2>/dev/null || true
  for f in /etc/systemd/network/*br0* /etc/systemd/network/mv-*; do
    grep -qF "# Managed by webkvm" "$f" 2>/dev/null || continue
    rm -f "$f" "$f.bak" 2>/dev/null || true
    echo "  - removed $f"
  done
  for f in /etc/systemd/system/webkvm-mv-br0@*.service /etc/sysctl.d/90-webkvm-bridge.conf; do
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
  echo "[2/5] keeping libvirt networks/bridges (use PURGE_NETWORKS=1 to remove)"
fi

# ── storage pools (only with PURGE_DATA) ───────────────────────────────
if [[ "${PURGE_DATA:-0}" == 1 ]]; then
  echo "[3/5] removing storage pools (data purge)"
  for pool in webkvm-disks ISOS; do
    virsh pool-destroy "$pool" 2>/dev/null || true
    virsh pool-undefine "$pool" 2>/dev/null || true
  done
  rm -rf -- "${DATA_DIR}"
  echo "  - removed ${DATA_DIR}"
else
  echo "[3/5] kept data at ${DATA_DIR}; use PURGE_DATA=1 to remove it"
fi

# ── runtime packages ───────────────────────────────────────────────────
if [[ "${PURGE_PACKAGES:-0}" == 1 ]]; then
  echo "[4/5] removing runtime packages"
  if command -v apt-get >/dev/null 2>&1; then
    apt-get remove -y --purge libvirt-daemon-system libvirt-clients qemu-system-x86 qemu-utils ovmf swtpm swtpm-tools virtinst dnsmasq-base xorriso openssl 2>/dev/null || true
    apt-get autoremove -y 2>/dev/null || true
  elif command -v dnf >/dev/null 2>&1; then
    dnf remove -y libvirt-daemon-kvm libvirt-client qemu-kvm qemu-img edk2-ovmf swtpm-tools virt-install dnsmasq openssl xorriso 2>/dev/null || true
  elif command -v yum >/dev/null 2>&1; then
    yum remove -y libvirt-daemon-kvm libvirt-client qemu-kvm qemu-img edk2-ovmf swtpm-tools virt-install dnsmasq openssl xorriso 2>/dev/null || true
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Rs --noconfirm libvirt qemu-full qemu-img swtpm edk2-ovmf virt-install dnsmasq openssl xorriso 2>/dev/null || true
  fi
  echo "  - runtime packages removed (where installed)"
else
  echo "[4/5] keeping runtime packages (use PURGE_PACKAGES=1 to remove)"
fi

echo "[5/5] done"
echo "  PURGE_DATA=${PURGE_DATA:-0}  PURGE_NETWORKS=${PURGE_NETWORKS:-0}  PURGE_PACKAGES=${PURGE_PACKAGES:-0}"
