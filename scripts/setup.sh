#!/usr/bin/env bash
# webkvm one-shot installer.
# After this script finishes successfully, the user only needs to open
# a browser — no more shell commands are required to use the app.
#
# For a fully automated install:
#   sudo BRIDGE_STATIC_IP=192.168.1.100/24 \   # optional; omit for DHCP bridge
#        BRIDGE_STATIC_GW=192.168.1.1 \
#        BRIDGE_STATIC_DNS=1.1.1.1,8.8.8.8 \
#        BRIDGE_DHCP=false \                    # default: true
#        NETWORK_MODE=both \
#        BINARY_URL=https://github.com/Slaker19/webkvm/releases/download/v1.0.0/webkvm \
#        INSTALL_CADDY=true \
#        ./scripts/setup.sh
# NETWORK_MODE: nat | bridge | both (default: both)
# BRIDGE_DHCP: true (bridge gets IP via DHCP) | false (use BRIDGE_STATIC_IP)
# Or just run interactively and answer the prompts.
# If BINARY_URL is set, the script downloads a pre-built backend binary
# instead of compiling from source (no Go/Node.js toolchain needed).
# INSTALL_CADDY defaults to true; set to false to skip HTTPS proxy setup.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$SCRIPT_DIR"

VERSION="$(git describe --tags --always 2>/dev/null || echo 'dev')"

echo "=== webkvm setup (v${VERSION}) ==="
echo ""

# Make sure the calling user has sudo cached for the non-interactive parts.
sudo -v
# Keep the sudo timestamp fresh for the rest of the script (re-ask every 5 min).
while true; do sudo -n true; sleep 300; kill -0 "$$" 2>/dev/null || exit; done 2>/dev/null &
SUDO_KEEPER_PID=$!
trap 'kill $SUDO_KEEPER_PID 2>/dev/null || true' EXIT

# --- 0. Static IP configuration (optional) --------------------------------
# webkvm supports two bridge modes:
#   - macvlan (default): bridge gets its own DHCP/static IP; host untouched
#   - direct-bridge: moves the host's static IP to br0 (ethernet only)
# In macvlan DHCP mode (default), no static IP is needed at all — the
# bridge obtains its own lease from the router.
#
# For NAT-only mode, the host IP is left untouched by default. A static
# IP is only needed if you want a fixed address for the webUI.
#
# Resolution order: env vars → auto-detect → interactive prompt.
# The interactive prompt lives in setup.sh (this file) on purpose:
# setup-network.sh is the idempotent re-runnable engine and must NEVER
# block on stdin, while setup.sh is the onboarding wrapper.
if [ -z "${BRIDGE_STATIC_IP:-}" ] && [ "${BRIDGE_DHCP:-}" != "true" ]; then
    echo "[0/8] Static IP configuration"
    echo "  In macvlan mode (default), the bridge gets its own IP via DHCP."
    echo "  No changes to the host's existing config are needed."
    echo
    echo "  Options:"
    echo "    0) Skip — use DHCP for the bridge (default, host stays as-is)"
    echo "    1) Set a static IP for the bridge (choose this for servers)"
    echo

    while true; do
        # Try auto-detect first (WEBKVM_DETECT_ONLY=1 does read-only queries).
        DETECTED=""
        if DETECTED=$(WEBKVM_DETECT_ONLY=1 bash "${SCRIPT_DIR}/scripts/setup-network.sh" 2>/dev/null) \
            && [ -n "${DETECTED}" ]; then
            eval "${DETECTED}"
            export BRIDGE_STATIC_IP BRIDGE_STATIC_GW BRIDGE_STATIC_DNS
            echo "  Auto-detected from running config:"
            echo "    IP:  $BRIDGE_STATIC_IP"
            echo "    GW:  $BRIDGE_STATIC_GW"
            echo "    DNS: $BRIDGE_STATIC_DNS"
            echo
        fi
        read -r -p "  Choose 0 (DHCP) or 1 (static) [default 0]: " STATIC_CHOICE
        STATIC_CHOICE="${STATIC_CHOICE:-0}"
        case "$STATIC_CHOICE" in
            0)
                # DHCP mode: unset BRIDGE_STATIC_IP so setup-network.sh
                # knows to use DHCP.
                unset BRIDGE_STATIC_IP BRIDGE_STATIC_GW BRIDGE_STATIC_DNS
                export BRIDGE_DHCP=true
                echo "  Using DHCP — the bridge will obtain its own IP."
                break
                ;;
            1)
                if [ -n "${BRIDGE_STATIC_IP:-}" ]; then
                    # Already auto-detected above
                    export BRIDGE_DHCP=false
                    break
                fi
                read -r -p "  Static IP for the bridge (CIDR, e.g. 192.168.1.100/24): " BRIDGE_STATIC_IP
                if [ -z "$BRIDGE_STATIC_IP" ]; then
                    echo "  A static IP is required for this option."
                    continue
                fi
                read -r -p "  Default gateway (e.g. 192.168.1.1): " BRIDGE_STATIC_GW
                if [ -z "$BRIDGE_STATIC_GW" ]; then
                    echo "  A gateway is required."
                    continue
                fi
                read -r -p "  DNS servers (comma-separated, e.g. 1.1.1.1,8.8.8.8): " BRIDGE_STATIC_DNS
                if [ -z "$BRIDGE_STATIC_DNS" ]; then
                    BRIDGE_STATIC_DNS="1.1.1.1,8.8.8.8"
                    echo "  (using default DNS: $BRIDGE_STATIC_DNS)"
                fi
                export BRIDGE_STATIC_IP BRIDGE_STATIC_GW BRIDGE_STATIC_DNS BRIDGE_DHCP=false
                break
                ;;
            *) echo "  Please enter 0 or 1" ;;
        esac
    done
fi
if [ "${BRIDGE_DHCP:-}" = "true" ]; then
    echo "  Bridge IP: DHCP (auto-assigned by router)"
else
    echo "  Static IP:  ${BRIDGE_STATIC_IP:-<not set>}"
    echo "  Gateway:    ${BRIDGE_STATIC_GW:-<not set>}"
    echo "  DNS:        ${BRIDGE_STATIC_DNS:-<not set>}"
fi
echo

# --- 0b. Network mode selection -----------------------------------------
# Modes: nat | bridge | both (default: both)
# nat:      VMs use libvirt NAT (192.168.122.x), reach Internet via host
# bridge:   Macvlan bridge br0, VMs visible on LAN, host IP untouched
# both:     both NAT and Bridge available
if [ -z "${NETWORK_MODE:-}" ]; then
    echo "[0b/8] Network mode"
    echo "  Choose how VMs connect to the network:"
    echo "    1) NAT      — VMs in 192.168.122.x, Internet via host NAT"
    echo "    2) Bridge   — Macvlan bridge br0, VMs on LAN with own MAC"
    echo "                   (works on WiFi & ethernet, host IP untouched)"
    echo "    3) Both     — NAT + Bridge, choose per VM (recommended)"
    echo
    while true; do
        read -r -p "  Network mode [1/2/3] (default 3): " MODE_CHOICE
        MODE_CHOICE="${MODE_CHOICE:-3}"
        case "$MODE_CHOICE" in
            1) NETWORK_MODE="nat"; break ;;
            2) NETWORK_MODE="bridge"; break ;;
            3) NETWORK_MODE="both"; break ;;
            *) echo "  Please enter 1, 2, or 3" ;;
        esac
    done
    export NETWORK_MODE
fi
echo "  Network mode: $NETWORK_MODE"
echo

# --- 0c. HTTPS/Caddy selection -------------------------------------------
# Caddy provides HTTPS termination with self-signed certs for LAN IPs.
if [ -z "${INSTALL_CADDY:-}" ]; then
    echo "[0c/8] HTTPS (Caddy reverse proxy)"
    echo "  Caddy provides HTTPS termination with self-signed certs for LAN IPs."
    echo "  Options:"
    echo "    1) Yes, install Caddy (HTTPS on :443, self-signed certs for LAN IP)"
    echo "    2) No, HTTP only on :8080 (for Tailscale, ngrok, or behind another proxy)"
    echo
    while true; do
        read -r -p "  Install Caddy? [1/2] (default 1): " CADDY_CHOICE
        CADDY_CHOICE="${CADDY_CHOICE:-1}"
        case "$CADDY_CHOICE" in
            1) INSTALL_CADDY=true; break ;;
            2) INSTALL_CADDY=false; break ;;
            *) echo "  Please enter 1 or 2" ;;
        esac
    done
    export INSTALL_CADDY
fi
echo "  Caddy (HTTPS): $INSTALL_CADDY"
echo

# --- 0d. Configure temporary DNS for package installation ---
# We need working DNS for apt update and git/curl before the full network
# setup in step 6. Use the detected/provided DNS or fallback to 1.1.1.1/8.8.8.8.
if [ -n "${BRIDGE_STATIC_DNS:-}" ]; then
    DNS_SERVERS="${BRIDGE_STATIC_DNS}"
else
    DNS_SERVERS="1.1.1.1,8.8.8.8"
fi
echo "[0d/9] Configuring temporary DNS (${DNS_SERVERS})..."
sudo systemctl stop systemd-resolved 2>/dev/null || true
# Remove immutable flag if present (from a previous run), then backup
sudo chattr -i /etc/resolv.conf 2>/dev/null || true
[ -f /etc/resolv.conf.bak.pre-webkvm ] || sudo cp /etc/resolv.conf /etc/resolv.conf.bak.pre-webkvm 2>/dev/null || true
# Write temporary resolv.conf with our DNS servers
printf "nameserver %s\n" ${DNS_SERVERS//,/ } | sudo tee /etc/resolv.conf >/dev/null
# Make it immutable to prevent systemd-resolved from overwriting it
sudo chattr +i /etc/resolv.conf 2>/dev/null || true
echo "  Temporary DNS configured"
echo

# --- 1. Go (needed only to build from source) ---
BINARY_URL="${BINARY_URL:-}"
if [ -z "$BINARY_URL" ]; then
  echo "[3/10] Checking Go..."
  if ! command -v go &>/dev/null; then
      echo "  Go will be installed with system dependencies"
  fi
  export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"
  echo "  Go: $(go version 2>/dev/null || echo 'not installed yet')"
else
  echo "[1/8] Go (skipped — using pre-built binary)"
fi

# --- 2. Detect distro and install system deps ---
echo ""
echo "[4/10] Installing system dependencies..."
NEED_BUILD_TOOLS=false
if [ -z "$BINARY_URL" ]; then
  NEED_BUILD_TOOLS=true
fi
if command -v apt &>/dev/null; then
    DISTRO="debian"
    PKGS=(libvirt-daemon-system libvirt-dev qemu-system-x86 swtpm ovmf virtinst bridge-utils curl ca-certificates)
if [ "$NEED_BUILD_TOOLS" = true ]; then
        PKGS+=(gcc libc6-dev make golang nodejs npm)
    fi
    sudo apt update
    sudo apt install -y "${PKGS[@]}"

    if [ "$NEED_BUILD_TOOLS" = true ]; then
        # Install Node 20+ from system package manager (Ubuntu 24.04+ has Node 20+)
        PKGS+=(gcc libc6-dev make golang nodejs)
    fi
    sudo apt update
    sudo apt install -y "${PKGS[@]}"

    if [ "$NEED_BUILD_TOOLS" = true ]; then
        # Verify npm is available in PATH
        if ! command -v npm &>/dev/null; then
            echo "  WARNING: npm not in PATH, attempting to fix..."
            export PATH="/usr/bin:$PATH"
            if ! command -v npm &>/dev/null; then
                echo "  ERROR: npm not available after Node.js install"
                exit 1
            fi
        fi
    fi
elif command -v pacman &>/dev/null; then
    DISTRO="arch"
    PKGS=(libvirt qemu-full swtpm edk2-ovmf dmidecode curl)
    if [ "$NEED_BUILD_TOOLS" = true ]; then
        PKGS+=(nodejs npm git base-devel go)
    fi
    sudo pacman -S --needed --noconfirm "${PKGS[@]}"
else
    echo "Unsupported distro. Please install libvirt, qemu, swtpm, ovmf, and the backend binary manually."
    exit 1
fi
echo "  Detected: $DISTRO"
echo "  Node: $(node -v 2>/dev/null || echo 'not found')"

# --- 3. Enable libvirtd + virtlogd ---
echo ""
echo "[5/10] Enabling libvirt services..."
sudo systemctl enable --now libvirtd 2>/dev/null || true
sudo systemctl enable --now virtlogd 2>/dev/null || true
# Make sure the running user is in the libvirt group so qemu:///system
# works for them too (the backend runs as root via systemd, but
# `virsh`/virt-manager used manually also needs this).
if ! id -nG "$(whoami)" | tr ' ' '\n' | grep -qx 'libvirt'; then
    sudo usermod -aG libvirt "$(whoami)" || true
    NEED_RELOG=1
else
    NEED_RELOG=0
fi

# --- 4. Create state directories ---
# The data dir is also the install dir for bare-metal installs.
echo ""
echo "[6/10] Creating state directories..."
DATA_DIR="${DATA_DIR:-/opt/webkvm}"
export DATA_DIR
sudo install -d -m 0755 "$DATA_DIR"
sudo install -d -m 0755 "$DATA_DIR/logs"
sudo install -d -m 0755 /var/log/webkvm
# CIFS secrets store. The backend uses this file to persist SMB
# credentials across libvirtd restarts (the libvirt secret store
# is ephemeral). An empty JSON object is sufficient for a fresh
# install; the backend populates it as operators create CIFS pools.
if [ ! -f "$DATA_DIR/cifs-secrets.json" ]; then
    echo '{}' | sudo tee "$DATA_DIR/cifs-secrets.json" >/dev/null
    sudo chmod 600 "$DATA_DIR/cifs-secrets.json"
    echo "  created $DATA_DIR/cifs-secrets.json"
fi
# If the user cloned the repo somewhere other than /opt/webkvm, symlink it
# so the in-app updater (which expects $REPO_DIR) can find it.
# Skip silently if the symlink already exists.
if [ ! -e /opt/webkvm/source ] && [ "$SCRIPT_DIR" != "/opt/webkvm" ]; then
    sudo ln -sfn "$SCRIPT_DIR" /opt/webkvm/source
    echo "  Linked repo source to /opt/webkvm/source (used by in-app updater)"
fi

# --- 5. Network + static IP setup ---
# Configures the host's physical interface with a STATIC IP and
# sets up libvirt networks based on NETWORK_MODE:
#   nat    — libvirt default NAT network (192.168.122.x)
#   bridge — Linux bridge br0 + libvirt bridge network br0-bridge
#   both   — both NAT and Bridge (default)
# This step is idempotent and skips itself inside containers.
echo ""
echo "[7/10] Configuring host network (mode: ${NETWORK_MODE})..."
# Build setup-network.sh args
NET_ARGS="--${NETWORK_MODE}"
if [ "${BRIDGE_DHCP:-true}" = "true" ]; then
    NET_ARGS="${NET_ARGS} --dhcp"
else
    NET_ARGS="${NET_ARGS} --static"
fi
sudo BRIDGE_STATIC_IP="${BRIDGE_STATIC_IP:-}" BRIDGE_STATIC_GW="${BRIDGE_STATIC_GW:-}" BRIDGE_STATIC_DNS="${BRIDGE_STATIC_DNS:-}" bash "${SCRIPT_DIR}/scripts/setup-network.sh" ${NET_ARGS} || {
    echo "  ! network setup failed; you can re-run scripts/setup-network.sh ${NET_ARGS} later"
}

# --- 6. Build or download binary ---
echo ""
echo "[8/10] Building from source (local compile, no network calls)..."
# Ensure npm is available before building frontend
if ! command -v npm &>/dev/null; then
  echo "  ERROR: npm not found in PATH. Node.js installation may have failed."
  exit 1
fi
make build

# --- 8. Install systemd service ---
echo ""
echo "[9/10] Installing systemd service..."
sudo install -d -m 0755 /usr/local/bin
if [ -n "$BINARY_URL" ]; then
  # Binary was already installed in step 6 — nothing to do.
  :
elif [ -f backend/webkvm ]; then
  sudo install -m 0755 backend/webkvm /usr/local/bin/webkvm
else
  echo "  ERROR: no binary found (backend/webkvm) and no BINARY_URL set." >&2
  exit 1
fi
sudo install -d -m 0755 /etc/systemd/system
sudo install -m 0644 scripts/webkvm.service /etc/systemd/system/webkvm.service
sudo install -m 0644 scripts/webkvm.logrotate /etc/logrotate.d/webkvm 2>/dev/null || true
# Write /etc/default/webkvm so the service unit's EnvironmentFile
# picks up the DATA_DIR.
sudo install -d -m 0755 /etc/default
if [ ! -f /etc/default/webkvm ]; then
    sudo tee /etc/default/webkvm >/dev/null <<EOF
# webkvm defaults — sourced by the systemd unit as
# EnvironmentFile. Empty values fall through to the unit's inline
# defaults, so every key here is optional.
DATA_DIR=${DATA_DIR}
REPO_DIR=/opt/webkvm/source
EOF
    sudo chmod 0644 /etc/default/webkvm
    echo "  wrote /etc/default/webkvm (DATA_DIR=${DATA_DIR})"
fi
sudo systemctl daemon-reload
sudo systemctl enable --now webkvm

# Copy .env.example to .env on first install. The backend auto-loads
# .env from CWD via godotenv (since W3.1). -n so we don't clobber an
# existing .env with the operator's overrides. The systemd unit
# already sets the required env vars, so .env is purely a
# convenience for the operator (e.g. for `make dev`).
if [ ! -f .env ]; then
    cp -n .env.example .env
    echo "  created .env (edit to override defaults; systemd unit wins)"
fi

# Detect the LAN IP for the webUI. Prefers the address from the default
# route (most likely to be externally reachable), falls back to hostname.
detect_ip() {
    local ip
    # Try the default route first — this gives the src IP of the interface
    # that reaches the internet (most useful for LAN access).
    ip="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '/src/ {print $7; exit}')"
    [[ -n "$ip" ]] && { echo "$ip"; return; }
    ip="$(hostname -i 2>/dev/null | awk '{print $1}')"
    [[ -n "$ip" && "$ip" != "127.0.0.1" && "$ip" != "127.0.1.1" ]] && { echo "$ip"; return; }
    echo "localhost"
}
IP="$(detect_ip)"

# --- 10. Install Caddy (HTTPS termination) ---
INSTALLED_CADDY=false
if [ "$INSTALL_CADDY" = "true" ]; then
  echo ""
  echo "[10/10] Installing Caddy (HTTPS reverse proxy)..."
  if [ -f "${SCRIPT_DIR}/scripts/install-caddy-systemd.sh" ]; then
    if sudo bash "${SCRIPT_DIR}/scripts/install-caddy-systemd.sh"; then
      INSTALLED_CADDY=true
    else
      echo "  ! Caddy installation failed (non-fatal). The backend is still reachable on http://${IP}:8080"
    fi
  else
    echo "  ! install-caddy-systemd.sh not found — skipping Caddy setup"
  fi
else
  echo ""
  echo "[10/10] Caddy installation skipped (INSTALL_CADDY=false)"
fi

echo ""
echo "Done."
echo ""

if [ "$INSTALLED_CADDY" = "true" ]; then
  PROTO=https
  PORT=""
else
  PROTO=http
  PORT=":8080"
fi

cat <<EOF

=================================================
  ✅ webkvm v${VERSION} is installed and running
=================================================

  Open:    ${PROTO}://${IP}${PORT}
  Login:   admin / admin
  Network mode: ${NETWORK_MODE}${BRIDGE_STATIC_IP:+ (static bridge IP: ${BRIDGE_STATIC_IP})}
  Host IP: ${IP} (untouched by webkvm)

  Service: systemctl status webkvm
  Logs:    journalctl -u webkvm -f
  Update:  open the app → "System" sidebar entry

  The backend runs as root so it can manage libvirt
  and read existing VM disk images automatically.

  VM network options (configure per VM in Networks page):
    - default (NAT): 192.168.122.x, Internet via host
EOF
if [[ "${NETWORK_MODE}" == "bridge" || "${NETWORK_MODE}" == "both" ]]; then
    cat <<EOF
    - br0-bridge (Bridge): VMs on LAN with own MAC, host IP untouched
EOF
fi
cat <<EOF
  To add custom networks (NAT, isolated, bridge via existing Linux
  bridge), open the "Networks" page in the webUI.

EOF

if [[ "$NEED_RELOG" == "1" ]]; then
    cat <<EOF
  ⚠️  You were added to the 'libvirt' group.
      Log out and back in (or run: newgrp libvirt)
      before using 'virsh' / 'virt-manager' manually.
      The systemd service already runs as root, so
      the web UI works without re-logging in.
EOF
fi
