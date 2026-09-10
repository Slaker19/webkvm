#!/usr/bin/env bash
# Wire up networking on a fresh webkvm install.
#
# Modes:
#   --bridge       : (DEFAULT) shared L2 Linux bridge — Proxmox-style. Reuses
#                    an existing host bridge (vmbr0/br0) when present, otherwise
#                    creates a macvlan slave + br0 so VMs/containers reach the
#                    LAN directly. The physical interface is never touched if it
#                    can be avoided. Never creates an isolated NAT network.
#   --nat          : OPT-IN isolation: create libvirt default NAT network only.
#                    If BRIDGE_STATIC_IP is set, writes it to the physical
#                    interface; otherwise leaves the host's network untouched.
#   --both         : both shared-L2 bridge and NAT (explicit opt-in).
#   --direct-bridge: use the OLD bridge mode that enslaves the physical
#                    interface directly (ethernet only, drops IP during setup).
#
# Bridge IP:
#   --dhcp         : the bridge obtains its IP via DHCP through the macvlan.
#                    The physical interface is left untouched (default).
#   --static       : the bridge uses the IP from BRIDGE_STATIC_IP/GW/DNS env
#                    vars (or auto-detected values). For bridge mode, the env
#                    vars describe the BRIDGE's IP, not the physical interface.
#
# Env var NET_MODE=nat|bridge|both also works (for CI/automation).
#
# Idempotent: safe to re-run on an existing install.
# Skipped automatically when running inside a container.
#
# Side effect:
#   - In macvlan mode (default): NetworkManager is LEFT RUNNING to manage
#     the physical interface (especially WiFi). Only dhcpcd is stopped
#     on the physical interface to avoid DHCP conflicts. systemd-networkd
#     manages only mv-br0 + br0.
#   - In --direct-bridge mode: both NM and dhcpcd are stopped (the
#     physical interface is fully managed by systemd-networkd).
#   - systemd-networkd is enabled+started if not already running.
#
# Static IP: env vars BRIDGE_STATIC_IP / BRIDGE_STATIC_GW /
# BRIDGE_STATIC_DNS are honoured. In macvlan mode (default), these
# describe the BRIDGE's address — the physical interface keeps its
# existing config. In --direct-bridge or NAT-only mode, they describe
# the physical interface's address (same as before).
#
# For DHCP mode (--dhcp), the auto-detect is purely informational
# (shown in the summary). The bridge gets its IP from the router.
#
# WEBKVM_DETECT_ONLY=1 makes the script exit after detecting and
# printing the would-be values, without touching the system.

set -euo pipefail

# --- Mode parsing ----------------------------------------------------------
MODE="${NET_MODE:-bridge}"
DIRECT_BRIDGE=false
BRIDGE_DHCP=false  # default: the current address is pinned as STATIC on the bridge
while [[ $# -gt 0 ]]; do
    case "$1" in
        --nat)              MODE="nat" ;;
        --bridge)           MODE="bridge" ;;
        --both)             MODE="both" ;;
        --direct-bridge)    DIRECT_BRIDGE=true ;;
        --dhcp)             BRIDGE_DHCP=true ;;
        --static)           BRIDGE_DHCP=false ;;
        *)                  echo "Unknown flag: $1" >&2; exit 1 ;;
    esac
    shift
done

# If BRIDGE_STATIC_IP is set explicitly, override BRIDGE_DHCP
if [ -n "${BRIDGE_STATIC_IP:-}" ]; then
    BRIDGE_DHCP=false
fi

# In --direct-bridge mode, the old behaviour is always static (move IP)
# so DHCP makes no sense there.
if [ "$DIRECT_BRIDGE" = "true" ] && [ "$BRIDGE_DHCP" = "true" ]; then
    echo "WARNING: --direct-bridge requires a static IP. BRIDGE_STATIC_IP must be set." >&2
    echo "         Either set the env var or use --static instead of --dhcp." >&2
    exit 1
fi

# --- minimal pre-detection helpers (for WEBKVM_DETECT_ONLY) ------------------
pick_physical_iface() {
    local name type operstate
    # Pass 1: UP WiFi or ethernet
    for path in /sys/class/net/*; do
        name="$(basename "${path}")"
        case "${name}" in lo|virbr*|vnet*|docker*|br-*|tun*|tap*|veth*) continue ;; esac
        [ -d "${path}/bridge" ] && continue
        [ -d "${path}/brport" ] && continue
        [ -d "${path}/device" ] || continue
        type="$(cat "${path}/type" 2>/dev/null || echo 0)"
        operstate="$(cat "${path}/operstate" 2>/dev/null || echo down)"
        if { [ "${type}" = "1" ] || [ -d "${path}/wireless" ]; } && [ "${operstate}" = "up" ]; then
            echo "${name}"; return 0
        fi
    done
    # Pass 2: any UP interface
    for path in /sys/class/net/*; do
        name="$(basename "${path}")"
        case "${name}" in lo|virbr*|vnet*|docker*|br-*|tun*|tap*|veth*) continue ;; esac
        [ -d "${path}/bridge" ] && continue
        [ -d "${path}/brport" ] && continue
        [ -d "${path}/device" ] || continue
        operstate="$(cat "${path}/operstate" 2>/dev/null || echo down)"
        if [ "${operstate}" = "up" ]; then echo "${name}"; return 0; fi
    done
    # Pass 3: interface from default route
    name="$(ip route show default 2>/dev/null | awk '{print $5; exit}')"
    if [ -n "${name}" ] && [ -d "/sys/class/net/${name}/device" ]; then
        echo "${name}"; return 0
    fi
    # Pass 4: any interface with /device
    for path in /sys/class/net/*; do
        name="$(basename "${path}")"
        case "${name}" in lo|virbr*|vnet*|docker*|br-*|tun*|tap*|veth*) continue ;; esac
        [ -d "${path}/bridge" ] && continue
        [ -d "${path}/brport" ] && continue
        [ -d "${path}/device" ] || continue
        echo "${name}"; return 0
    done
    return 1
}

detect_static_for_iface() {
    local iface="$1" ip gw dns
    ip=$(ip -4 -o addr show dev "${iface}" scope global 2>/dev/null | awk '{print $4}' | head -1)
    gw=$(ip route 2>/dev/null | awk '/^default/ {print $3; exit}')
    dns=$(resolvectl dns "${iface}" 2>/dev/null \
        | sed -E 's/^[^:]*:[[:space:]]*//' \
        | tr -s '[:space:]' ',' | sed 's/,$//')
    if [ -z "${dns}" ] || [ "${dns}" = "" ]; then
        dns=$(grep '^nameserver' /etc/resolv.conf 2>/dev/null \
            | awk '{print $2}' | grep -v '^127\.0\.0\.53$' | paste -sd ',' -)
    fi
    if [ -z "${ip}" ] || [ -z "${gw}" ]; then
        return 1
    fi
    [ -z "${dns}" ] && dns="1.1.1.1,8.8.8.8"
    echo "BRIDGE_STATIC_IP=${ip}"
    echo "BRIDGE_STATIC_GW=${gw}"
    echo "BRIDGE_STATIC_DNS=${dns}"
}

# --- 0. WEBKVM_DETECT_ONLY fast path -----------------------------------------
if [ "${WEBKVM_DETECT_ONLY:-0}" = "1" ]; then
    detect_iface="$(pick_physical_iface 2>/dev/null || true)"
    if [ -n "${detect_iface}" ] && detect_static_for_iface "${detect_iface}" >/dev/null 2>&1; then
        detect_static_for_iface "${detect_iface}"
    fi
    exit 0
fi

# --- container detection ----------------------------------------------------
in_container() {
    [ -f /run/.containerenv ] && return 0
    grep -qE 'docker|lxc|containerd' /proc/1/cgroup 2>/dev/null && return 0
    return 1
}
if in_container; then
    echo "=== webkvm network setup: skipped (running in a container)"
    exit 0
fi

# --- managed-file helper ----------------------------------------------------
MANAGED_MARKER="# Managed by webkvm"

write_managed_file() {
    local path="$1" content="$2"
    if [ -f "$path" ] && ! grep -qF "$MANAGED_MARKER" "$path"; then
        echo "  ! ${path}: exists without our marker, leaving alone"
        return 0
    fi
    local tmp
    tmp="$(mktemp)"
    {
        echo "$MANAGED_MARKER"
        echo "# DO NOT EDIT: re-run scripts/setup-network.sh to regenerate."
        echo
        echo "$content"
    } > "$tmp"
    sudo install -m 0644 "$tmp" "$path"
    rm -f "$tmp"
    echo "  + ${path}: written"
}

backup_managed_file() {
    local path="$1"
    [ -f "$path" ] || return 0
    grep -qF "$MANAGED_MARKER" "$path" || return 0
    sudo cp "${path}" "${path}.bak" 2>/dev/null || true
    echo "  ! ${path}.bak: backup created (use it to roll back from console)"
}

# --- pick the first physical interface -------------------------------------
pick_physical_iface() {
    # First pass: prefer wired ethernet or WiFi interfaces that are UP
    for path in /sys/class/net/*; do
        local name
        name="$(basename "${path}")"
        case "${name}" in
            lo|virbr*|vnet*|docker*|br-*|tun*|tap*|veth*) continue ;;
        esac
        [ -d "${path}/bridge" ] && continue
        [ -d "${path}/brport" ] && continue
        [ -d "${path}/device" ] || continue
        local type operstate
        type="$(cat "${path}/type" 2>/dev/null || echo 0)"
        operstate="$(cat "${path}/operstate" 2>/dev/null || echo down)"
        # WiFi interfaces may have a /wireless dir but always have type=1
        # Accept type 1 (ARPHRD_ETHER) OR presence of /wireless directory
        if { [ "${type}" = "1" ] || [ -d "${path}/wireless" ]; } && [ "${operstate}" = "up" ]; then
            echo "${name}"
            return 0
        fi
    done
    # Second pass: any UP interface with /device
    for path in /sys/class/net/*; do
        local name
        name="$(basename "${path}")"
        case "${name}" in
            lo|virbr*|vnet*|docker*|br-*|tun*|tap*|veth*) continue ;;
        esac
        [ -d "${path}/bridge" ] && continue
        [ -d "${path}/brport" ] && continue
        [ -d "${path}/device" ] || continue
        local operstate
        operstate="$(cat "${path}/operstate" 2>/dev/null || echo down)"
        if [ "${operstate}" = "up" ]; then
            echo "${name}"
            return 0
        fi
    done
    # Third pass: check interface from the default route
    local default_iface
    default_iface="$(ip route show default 2>/dev/null | awk '{print $5; exit}')"
    if [ -n "${default_iface}" ] && [ -d "/sys/class/net/${default_iface}/device" ]; then
        local name
        name="$(basename "${default_iface}")"
        case "${name}" in
            lo|virbr*|vnet*|docker*|br-*|tun*|tap*|veth*) ;;
            *) echo "${name}"; return 0 ;;
        esac
    fi
    # Fourth pass: any interface with /device (UP or not — carrier might be down)
    for path in /sys/class/net/*; do
        local name
        name="$(basename "${path}")"
        case "${name}" in
            lo|virbr*|vnet*|docker*|br-*|tun*|tap*|veth*) continue ;;
        esac
        [ -d "${path}/bridge" ] && continue
        [ -d "${path}/brport" ] && continue
        [ -d "${path}/device" ] || continue
        echo "${name}"
        return 0
    done
    return 1
}

# --- detect_static_for_iface ------------------------------------------------
detect_static_for_iface() {
    local iface="$1"
    local ip gw dns

    ip=$(ip -4 -o addr show dev "${iface}" scope global 2>/dev/null | awk '{print $4}' | head -1)
    gw=$(ip route 2>/dev/null | awk '/^default/ {print $3; exit}')

    dns=$(resolvectl dns "${iface}" 2>/dev/null \
        | sed -E 's/^[^:]*:[[:space:]]*//' \
        | tr -s '[:space:]' ',' | sed 's/,$//')

    if [ -z "${dns}" ] || [ "${dns}" = "" ]; then
        dns=$(grep '^nameserver' /etc/resolv.conf 2>/dev/null \
            | awk '{print $2}' \
            | grep -v '^127\.0\.0\.53$' \
            | paste -sd ',' -)
    fi

    if [ -z "${ip}" ] || [ -z "${gw}" ]; then
        return 1
    fi
    if [ -z "${dns}" ]; then
        dns="1.1.1.1,8.8.8.8"
    fi

    echo "BRIDGE_STATIC_IP=${ip}"
    echo "BRIDGE_STATIC_GW=${gw}"
    echo "BRIDGE_STATIC_DNS=${dns}"
}

# --- is_iface_dhcp ----------------------------------------------------------
# Detect whether an interface gets its address from DHCP, across every
# config manager. The kernel route (proto dhcp) is the runtime truth; the
# config files cover setups where the route isn't marked as dhcp.
is_iface_dhcp() {
    local iface="$1"

    # 1. Runtime truth: the default route was added by a DHCP client.
    if ip route 2>/dev/null | grep -qE "^default .* dev ${iface} .* proto dhcp"; then
        return 0
    fi

    # 2. NetworkManager: the active connection uses auto (DHCP).
    if command -v nmcli >/dev/null 2>&1; then
        local con
        con="$(nmcli -t -f NAME,DEVICE con show --active 2>/dev/null | grep ":${iface}$" | cut -d: -f1 | head -1)"
        if [ -n "${con}" ] && [ "$(nmcli -g ipv4.method con show "${con}" 2>/dev/null)" = "auto" ]; then
            return 0
        fi
    fi

    # 3. netplan: dhcp4 enabled for the interface.
    if [ -d /etc/netplan ]; then
        if grep -rqsE "^[[:space:]]+${iface}:" /etc/netplan/*.yaml 2>/dev/null \
            && grep -rqsE "dhcp4:[[:space:]]*true" /etc/netplan/*.yaml 2>/dev/null; then
            return 0
        fi
    fi

    # 4. systemd-networkd: DHCP= on the interface's .network.
    if grep -rqsE "^Name=${iface}\b" /etc/systemd/network/*.network 2>/dev/null \
        && grep -rqsE "^DHCP=(yes|ipv4|ipv6)\b" /etc/systemd/network/*.network 2>/dev/null; then
        return 0
    fi

    # 5. ifupdown (/etc/network/interfaces): inet dhcp.
    if grep -rqsE "^iface[[:space:]]+${iface}[[:space:]]+inet[[:space:]]+dhcp\b" /etc/network/interfaces* 2>/dev/null; then
        return 0
    fi

    return 1
}

# confirm_pin_static asks the operator (y/n) to accept that the current DHCP
# lease will be pinned as a STATIC address on the bridge — the router still
# treats the IP as part of its pool until a reservation is added, and could
# reassign it to another device. Interactive runs prompt; automatic runs
# proceed. Returns 0 (pin it) or 1 (abort → keep DHCP).
confirm_pin_static() {
    local iface="$1" ip="$2"
    if [ -t 0 ]; then
        local ans=""
        echo
        read -r -p "  ⚠  DHCP detectado en ${iface} (IP actual: ${ip}).
     Esta IP se FIJARÁ como estática en el bridge vmbr0.
     Tu router seguirá viéndola en su pool DHCP hasta que añadas una reserva
     (si no, podría asignarla a otro dispositivo). ¿Aceptar? [Y/n] " ans </dev/tty || true
        case "${ans:-y}" in
            y|Y|"") return 0 ;;
            *) return 1 ;;
        esac
    fi
    return 0
}

# --- show_dhcp_to_static_warning --------------------------------------------
show_dhcp_to_static_warning() {
    local iface="$1" ip="$2" gw="$3" dns="$4"
    local mac
    mac=$(cat "/sys/class/net/${iface}/address" 2>/dev/null || echo "?")

    cat >&2 <<EOF
==============================================================
⚠️  Converting DHCP IP to static on ${iface}
==============================================================
  Current IP:  ${ip} (DHCP lease, scope global dynamic)
  Will become: ${ip} (static, via systemd-networkd)
  Gateway:     ${gw}
  DNS:         ${dns}
  MAC:         ${mac}

  Your router still considers this IP part of its DHCP pool.
  Until you tell the router otherwise, it can assign ${ip} to
  another device (a phone, a laptop), causing an IP conflict
  that is very hard to debug weeks later.

  ACTION REQUIRED on your router (any of these works):
    1. Add a DHCP RESERVATION for MAC ${mac} bound to ${ip}, OR
    2. Exclude ${ip} from the DHCP range (e.g. shrink the range
       so it ends at .145 and ${ip} is outside the pool)

  After you do ONE of those, the host can keep ${ip} forever
  without the router handing it out to someone else.
==============================================================
EOF

    if [ -t 0 ]; then
        local _
        read -r -p "Press Enter to continue, Ctrl-C to abort. " _ </dev/tty || true
    fi
}

# --- conflicting DHCP clients -----------------------------------------------
disable_conflicting_dhcp_clients() {
    local changed=0
    if [ "$DIRECT_BRIDGE" = "true" ]; then
        # Direct-bridge mode: systemd-networkd takes over the physical
        # interface, so kill any DHCP client that would interfere.
        if systemctl is-active --quiet NetworkManager 2>/dev/null; then
            sudo systemctl disable --now NetworkManager >/dev/null 2>&1 || true
            echo "  - NetworkManager: stopped and disabled (direct-bridge mode)"
            changed=1
        fi
        if systemctl is-active --quiet dhcpcd 2>/dev/null; then
            sudo systemctl disable --now dhcpcd >/dev/null 2>&1 || true
            echo "  - dhcpcd: stopped and disabled (direct-bridge mode)"
            changed=1
        fi
    else
        # Macvlan mode: physical interface stays managed by NM/dhcpcd.
        # Only ensure systemd-networkd is running for macvlan + bridge.
        if systemctl is-active --quiet NetworkManager 2>/dev/null; then
            echo "  = NetworkManager running (left active — macvlan mode)"
        fi
        if systemctl is-active --quiet dhcpcd 2>/dev/null; then
            echo "  = dhcpcd running (left active — macvlan mode)"
        fi
        if systemctl is-active --quiet systemd-networkd 2>/dev/null; then
            :
        elif systemctl list-unit-files systemd-networkd.service &>/dev/null; then
            sudo systemctl enable --now systemd-networkd >/dev/null 2>&1 || true
            echo "  + systemd-networkd: enabled and started for macvlan bridge management"
        else
            echo "  ! systemd-networkd not available (unexpected on this system)"
        fi
    fi
    if [ $changed -eq 0 ]; then
        echo "  = no conflicting DHCP clients running"
    fi
}

# --- libvirt default NAT network --------------------------------------------
ensure_default_network() {
    if ! sudo virsh net-list --all --name 2>/dev/null | grep -qx default; then
        echo "  - libvirt default network is not defined (NAT mode: start it manually if needed)"
        return 0
    fi
    if ! sudo virsh net-list --name 2>/dev/null | grep -qx default; then
        sudo virsh net-start default >/dev/null 2>&1 || true
        echo "  + libvirt default network: started"
    fi
    if ! sudo virsh net-list --autostart --name 2>/dev/null | grep -qx default; then
        sudo virsh net-autostart default >/dev/null 2>&1 || true
        echo "  + libvirt default network: autostart enabled"
    fi
}

# --- systemd-networkd config for physical interface (NAT mode) --------------
ensure_networkd_config_physical() {
    local iface="$1"

    backup_managed_file "/etc/systemd/network/${iface}.network"

    local dns_block=""
    local IFS=','
    for d in ${BRIDGE_STATIC_DNS}; do
        d="$(echo "${d}" | xargs)"
        [ -z "${d}" ] && continue
        dns_block="${dns_block}
DNS=${d}"
    done
    unset IFS

    write_managed_file "/etc/systemd/network/${iface}.network" \
"[Match]
Name=${iface}

[Network]
# Static IP — committed at install time, survives reboots and
# DHCP lease renewals on the LAN.
Address=${BRIDGE_STATIC_IP}
Gateway=${BRIDGE_STATIC_GW}${dns_block}
IPv6AcceptRA=yes
"
}

# --- systemd-networkd config for bridge (Bridge mode) -----------------------
ensure_networkd_config_bridge() {
    local br_name="$1" slave_iface="$2"

    backup_managed_file "/etc/systemd/network/${br_name}.netdev"
    backup_managed_file "/etc/systemd/network/${br_name}.network"
    backup_managed_file "/etc/systemd/network/${slave_iface}.network"

    write_managed_file "/etc/systemd/network/${br_name}.netdev" \
"[NetDev]
Name=${br_name}
Kind=bridge
"

    local dns_block=""
    local IFS=','
    for d in ${BRIDGE_STATIC_DNS}; do
        d="$(echo "${d}" | xargs)"
        [ -z "${d}" ] && continue
        dns_block="${dns_block}
DNS=${d}"
    done
    unset IFS

    write_managed_file "/etc/systemd/network/${br_name}.network" \
"[Match]
Name=${br_name}

[Network]
# Static IP — committed at install time, survives reboots and
# DHCP lease renewals on the LAN.
Address=${BRIDGE_STATIC_IP}
Gateway=${BRIDGE_STATIC_GW}${dns_block}
IPv6AcceptRA=yes
"

    write_managed_file "/etc/systemd/network/${slave_iface}.network" \
"[Match]
Name=${slave_iface}

[Network]
Bridge=${br_name}
DHCP=no
IPv6AcceptRA=no
IPv6PrivacyExtensions=no
"
}

# --- ARP flux prevention ----------------------------------------------------
# When the host has two IPs on the same subnet (e.g. wlan0 + br0), the
# kernel might respond to ARP requests for one IP from the wrong interface.
# These sysctls fix that:
#   arp_ignore=1  — reply only if target IP is on the receiving interface
#   arp_announce=2 — always use the best local address for the interface
ensure_sysctl_arp_flux() {
    local sysctl_file="/etc/sysctl.d/90-webkvm-bridge.conf"
    if [ -f "$sysctl_file" ] && grep -qF "$MANAGED_MARKER" "$sysctl_file"; then
        echo "  = $sysctl_file already in place"
        return 0
    fi
    if [ -f "$sysctl_file" ]; then
        echo "  ! $sysctl_file exists without our marker, leaving alone"
        return 0
    fi
    local tmp
    tmp="$(mktemp)"
    {
        echo "# $MANAGED_MARKER"
        echo "# Prevent ARP flux when host has multiple IPs on the same subnet"
        echo "# (e.g. wlan0/eth0 + br0 on the same LAN via macvlan)."
        echo "net.ipv4.conf.all.arp_ignore=1"
        echo "net.ipv4.conf.all.arp_announce=2"
    } > "$tmp"
    sudo install -m 0644 "$tmp" "$sysctl_file"
    rm -f "$tmp"
    sudo sysctl -p "$sysctl_file" >/dev/null 2>&1 || true
    echo "  + $sysctl_file: written and applied"
}

# --- systemd-networkd config for macvlan + bridge (Bridge mode, macvlan) ---
ensure_networkd_config_macvlan() {
    local br_name="$1" slave_iface="$2"
    local mv_name="mv-${br_name}"  # e.g. mv-br0

    backup_managed_file "/etc/systemd/network/${mv_name}.netdev"
    backup_managed_file "/etc/systemd/network/${mv_name}.network"
    backup_managed_file "/etc/systemd/network/${br_name}.netdev"
    backup_managed_file "/etc/systemd/network/${br_name}.network"

    # macvlan .netdev — defines the macvlan device (Parent= is NOT valid in
    # [MACVLAN]; systemd uses a .network file on the parent iface instead).
    # We work around this with a boot-time oneshot service (see below).
    write_managed_file "/etc/systemd/network/${mv_name}.netdev" \
"[NetDev]
Name=${mv_name}
Kind=macvlan

[MACVLAN]
Mode=bridge
"

    # macvlan .network — enslaves the macvlan to the bridge
    write_managed_file "/etc/systemd/network/${mv_name}.network" \
"[Match]
Name=${mv_name}

[Network]
Bridge=${br_name}
"

    # bridge .netdev
    write_managed_file "/etc/systemd/network/${br_name}.netdev" \
"[NetDev]
Name=${br_name}
Kind=bridge
"

    # bridge .network — DHCP or static
    if [ "$BRIDGE_DHCP" = "true" ]; then
        write_managed_file "/etc/systemd/network/${br_name}.network" \
"[Match]
Name=${br_name}

[Network]
DHCP=yes
"
    else
        local dns_block=""
        local IFS=','
        for d in ${BRIDGE_STATIC_DNS}; do
            d="$(echo "${d}" | xargs)"
            [ -z "${d}" ] && continue
            dns_block="${dns_block}
DNS=${d}"
        done
        unset IFS

        write_managed_file "/etc/systemd/network/${br_name}.network" \
"[Match]
Name=${br_name}

[Network]
# Static IP — committed at install time, survives reboots.
# No Gateway: the host keeps using the physical interface for its default route.
Address=${BRIDGE_STATIC_IP}${dns_block}
"
    fi

    # Boot-time oneshot service that recreates the macvlan slave.
    # This works around the fact that systemd-networkd's .netdev files
    # cannot specify the parent interface for a macvlan — only a .network
    # file on the parent can do that, and we avoid touching wlan0 because
    # NetworkManager manages it.
    local svc_name="webkvm-${mv_name}@${slave_iface}.service"
    backup_managed_file "/etc/systemd/system/${svc_name}"
    write_managed_file "/etc/systemd/system/${svc_name}" \
"[Unit]
Description=webkvm macvlan ${mv_name} on ${slave_iface}
After=network-online.target systemd-networkd.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=ip link add link ${slave_iface} name ${mv_name} type macvlan mode bridge
ExecStart=/bin/sh -c 'for i in \$(seq 1 10); do [ -d /sys/class/net/${br_name}/bridge ] && break; sleep 1; done'
ExecStart=ip link set ${mv_name} master ${br_name}
ExecStart=ip link set ${mv_name} up
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
"
    systemctl daemon-reload 2>/dev/null || true
    systemctl enable "${svc_name}" 2>/dev/null || true
}

# --- runtime macvlan + bridge creation --------------------------------------
ensure_macvlan_bridge() {
    local br_name="$1" slave_iface="$2"
    local mv_name="mv-${br_name}"
    local created_mv=false created_br=false

    # If this function exits abnormally, clean up what we created
    cleanup_macvlan() {
        local ec=$?
        if [ "$created_mv" = "true" ] && [ -d "/sys/class/net/${mv_name}" ]; then
            sudo ip link set "${mv_name}" nomaster 2>/dev/null || true
            sudo ip link delete "${mv_name}" 2>/dev/null || true
        fi
        if [ "$created_br" = "true" ] && [ -d "/sys/class/net/${br_name}/bridge" ]; then
            sudo ip link set "${br_name}" down 2>/dev/null || true
            sudo ip link delete "${br_name}" 2>/dev/null || true
        fi
        return $ec
    }

    if [ -d "/sys/class/net/${br_name}/bridge" ]; then
        echo "  = Linux bridge ${br_name} already exists"
        return 0
    fi

    if [ ! -d "/sys/class/net/${slave_iface}" ]; then
        echo "  ! interface ${slave_iface} not found; skipping bridge creation"
        return 0
    fi

    trap cleanup_macvlan EXIT

    # If a macvlan with our name already exists, skip creation
    if [ ! -d "/sys/class/net/${mv_name}" ]; then
        echo "  + creating macvlan ${mv_name} (parent: ${slave_iface})"
        sudo ip link add "${mv_name}" link "${slave_iface}" type macvlan mode bridge
        created_mv=true
    else
        echo "  = macvlan ${mv_name} already exists"
    fi

    echo "  + creating Linux bridge ${br_name}"
    sudo ip link add name "${br_name}" type bridge
    created_br=true

    if [ ! -d "/sys/class/net/${mv_name}" ]; then
        echo "  ! macvlan ${mv_name} was expected but does not exist; aborting"
        return 1
    fi

    echo "  + attaching ${mv_name} to ${br_name}"
    sudo ip link set "${mv_name}" master "${br_name}"

    sudo ip link set "${mv_name}" up
    sudo ip link set "${br_name}" up

    # Critical section done — clear cleanup trap
    trap - EXIT
    created_mv=false
    created_br=false

    # Apply IP: DHCP or static
    # NOTE: macvlan on WiFi CANNOT get a DHCP lease because the AP only
    # accepts the authenticated MAC (the physical interface's MAC, not
    # the macvlan's). On WiFi we always fall back to a static IP.
    local is_wifi=false
    if [ -d "/sys/class/net/${slave_iface}/wireless" ]; then
        is_wifi=true
    fi
    if [ "$BRIDGE_DHCP" = "true" ] && [ "$is_wifi" = "false" ]; then
        if command -v dhclient &>/dev/null; then
            echo "  + requesting DHCP lease on ${br_name}..."
            dhclient -v "${br_name}" 2>&1 | tail -1
        elif command -v dhcpcd &>/dev/null; then
            echo "  + requesting DHCP lease on ${br_name} (via dhcpcd)..."
            dhcpcd -t 15 "${br_name}" 2>&1 | tail -5
        else
            echo "  ! no DHCP client found (dhclient or dhcpcd); bridge may have no IP"
        fi
    else
        local use_ip="${BRIDGE_STATIC_IP:-}"
        local use_gw="${BRIDGE_STATIC_GW:-}"
        local use_dns="${BRIDGE_STATIC_DNS:-}"
        if [ -z "${use_ip}" ]; then
            if [ "$is_wifi" = "true" ]; then
                echo "  ! macvlan on WiFi cannot use DHCP (AP only accepts wlan0's MAC)"
            fi
            # Auto-pick an unused IP in the same subnet as the physical iface
            local phys_ip phys_prefix
            phys_ip="$(ip -4 -o addr show dev "${slave_iface}" scope global 2>/dev/null | awk '{print $4}' | head -1)"
            if [ -n "${phys_ip}" ]; then
                phys_prefix="${phys_ip#*/}"           # e.g. 24
                local base last_octet gw dns max_host
                base="$(echo "${phys_ip}" | sed 's/\.[0-9]*\/[0-9]*$//')"   # 192.168.1
                last_octet="$(echo "${phys_ip}" | sed 's/.*\.\([0-9]*\).*/\1/')" # 171
                gw="${BRIDGE_STATIC_GW:-$(ip route 2>/dev/null | awk '/^default/ {print $3; exit}')}"
                dns="${BRIDGE_STATIC_DNS:-$(resolvectl dns "${slave_iface}" 2>/dev/null | sed -E 's/^[^:]*:[[:space:]]*//' | tr -s '[:space:]' ',' | sed 's/,$//')}"
                [ -z "${dns}" ] && dns="1.1.1.1,8.8.8.8"

                # For /24 subnets, .254 is the max valid host. For others, use
                # a generous cap — if the IP is outside this range fallback works.
                max_host=254
                [ "$phys_prefix" = "24" ] || max_host=254  # safe default

                local offset=1 tried=0 max_tries=50
                while [ $tried -lt $max_tries ]; do
                    local candidate_host=$((last_octet + offset))
                    # Wrap around if we exceed the subnet
                    if [ $candidate_host -gt $max_host ]; then
                        candidate_host=$((candidate_host - max_host + 1))
                    fi
                    # Skip the current IP itself
                    if [ $candidate_host -eq $last_octet ]; then
                        offset=$((offset + 1))
                        continue
                    fi
                    local candidate="${base}.${candidate_host}"
                    if ! ping -c1 -W1 "${candidate}" >/dev/null 2>&1; then
                        use_ip="${candidate}/${phys_prefix}"
                        use_gw="${gw}"
                        use_dns="${dns}"
                        echo "  + auto-selected IP ${use_ip} for bridge (unused host in ${slave_iface}'s subnet)"
                        break
                    fi
                    offset=$((offset + 1))
                    tried=$((tried + 1))
                done
                if [ -z "${use_ip}" ]; then
                    # Fallback: use +1 regardless of check
                    local fb_host=$((last_octet + 1))
                    [ $fb_host -gt $max_host ] && fb_host=2
                    use_ip="${base}.${fb_host}/${phys_prefix}"
                    use_gw="${gw}"
                    use_dns="${dns}"
                    echo "  + (fallback) IP ${use_ip} for bridge"
                fi
            fi
        fi
        if [ -n "${use_ip}" ]; then
            echo "  + assigning IP ${use_ip} to ${br_name}"
            sudo ip addr flush dev "${br_name}" scope global 2>/dev/null || true
            sudo ip addr add "${use_ip}" dev "${br_name}" 2>/dev/null || true
            if [ -n "${use_gw}" ]; then
                sudo ip route add default via "${use_gw}" dev "${br_name}" metric 200 2>/dev/null || true
            fi
        else
            echo "  ! could not determine IP for bridge; assign one manually"
        fi
    fi

    # Tell NetworkManager to leave mv-br0 and br0 alone (they are managed
    # by systemd-networkd). This is a no-op if NM is not running.
    if command -v nmcli &>/dev/null; then
        nmcli device set "${mv_name}" managed no 2>/dev/null || true
        nmcli device set "${br_name}" managed no 2>/dev/null || true
    fi
}

# --- runtime bridge creation (DIRECT mode — old behaviour) ------------------
ensure_linux_bridge() {
    local br_name="$1" slave_iface="$2"
    if [ -d "/sys/class/net/${br_name}/bridge" ]; then
        echo "  = Linux bridge ${br_name} already exists"
        return 0
    fi
    if [ ! -d "/sys/class/net/${slave_iface}" ]; then
        echo "  ! interface ${slave_iface} not found; skipping bridge creation"
        return 0
    fi
    if [ -d "/sys/class/net/${slave_iface}/bridge" ]; then
        echo "  ! ${slave_iface} is itself a bridge; skipping"
        return 0
    fi
    if [ -d "/sys/class/net/${slave_iface}/brport" ]; then
        echo "  ! ${slave_iface} is already a bridge port; skipping"
        return 0
    fi

    echo "  + creating Linux bridge ${br_name} (slave: ${slave_iface})"
    sudo ip link add name "${br_name}" type bridge

    # Move IPs from slave → bridge BEFORE attaching the slave
    move_addr() {
        local family="$1" slave="$2" br="$3"
        sudo ip "-${family}" -o addr show dev "${slave}" scope global 2>/dev/null \
            | while read -r line; do
                local cidr
                cidr="$(echo "${line}" | awk '{print $4}')"
                [ -z "${cidr}" ] && continue
                sudo ip addr add "${cidr}" dev "${br}" 2>/dev/null || true
                sudo ip addr del "${cidr}" dev "${slave}" 2>/dev/null || true
            done
    }
    move_addr 4 "${slave_iface}" "${br_name}"
    move_addr 6 "${slave_iface}" "${br_name}"

    sudo ip link set "${slave_iface}" master "${br_name}"
    sudo ip link set "${br_name}" up

    # Apply static IP/gateway right now
    if [ -n "${BRIDGE_STATIC_IP:-}" ]; then
        sudo ip addr flush dev "${br_name}" scope global 2>/dev/null || true
        sudo ip addr add "${BRIDGE_STATIC_IP}" dev "${br_name}" 2>/dev/null || true
        while ip route show default 2>/dev/null | grep -q .; do
            ip route del default 2>/dev/null || break
        done
        sudo ip route add default via "${BRIDGE_STATIC_GW}" dev "${br_name}" 2>/dev/null || true
    fi
}

# --- post-state verification ------------------------------------------------
verify_post_state_physical() {
    local iface="$1"
    echo "[verify] post-state checks (physical)"

    if ip -4 addr show dev "${iface}" 2>/dev/null | grep -q 'inet '; then
        local ip_addr
        ip_addr="$(ip -4 -o addr show dev "${iface}" scope global 2>/dev/null | awk '{print $4}')"
        echo "  = ${iface} has an IPv4 address (${ip_addr})"
        if [ -n "${BRIDGE_STATIC_IP:-}" ] && [ "${ip_addr}" != "${BRIDGE_STATIC_IP}" ]; then
            echo "  ! WARNING: ${iface} has ${ip_addr} but static IP is ${BRIDGE_STATIC_IP}"
        fi
    else
        echo "  ! WARNING: ${iface} has no IPv4 address"
    fi

    local def_route
    def_route="$(ip route show default 2>/dev/null | head -1)"
    if echo "${def_route}" | grep -q "dev ${iface}"; then
        echo "  = default route via ${iface} (correct)"
    else
        echo "  ! WARNING: default route is not via ${iface} (got: ${def_route})"
    fi
}

verify_post_state_bridge() {
    local br_name="$1" slave_iface="$2"
    echo "[verify] post-state checks (bridge)"

    if ip -4 addr show dev "${br_name}" 2>/dev/null | grep -q 'inet '; then
        local br_ip
        br_ip="$(ip -4 -o addr show dev "${br_name}" scope global 2>/dev/null | awk '{print $4}')"
        echo "  = ${br_name} has an IPv4 address (${br_ip})"
        if [ -n "${BRIDGE_STATIC_IP:-}" ] && [ "${br_ip}" != "${BRIDGE_STATIC_IP}" ]; then
            echo "  ! WARNING: ${br_name} has ${br_ip} but static IP is ${BRIDGE_STATIC_IP}"
        fi
    else
        echo "  ! WARNING: ${br_name} has no IPv4 address"
    fi

    local slave_ipv4
    slave_ipv4="$(ip -4 -o addr show dev "${slave_iface}" scope global 2>/dev/null | awk '{print $4}')"
    if [ -n "${slave_ipv4}" ]; then
        echo "  ! WARNING: ${slave_iface} has an IPv4 (${slave_ipv4}); removing it"
        sudo ip addr del "${slave_ipv4}" dev "${slave_iface}" 2>/dev/null || true
    else
        echo "  = ${slave_iface} has no global IPv4 (correct)"
    fi

    local def_route
    def_route="$(ip route show default 2>/dev/null | head -1)"
    if echo "${def_route}" | grep -q "dev ${br_name}"; then
        echo "  = default route via ${br_name} (correct)"
    elif echo "${def_route}" | grep -q "dev ${slave_iface}"; then
        echo "  ! WARNING: default route is via ${slave_iface}, not ${br_name}"
    fi
}

# --- post-state verification (macvlan bridge) --------------------------------
verify_post_state_macvlan() {
    local br_name="$1" slave_iface="$2"
    local mv_name="mv-${br_name}"
    echo "[verify] post-state checks (macvlan bridge)"

    # 1. The bridge exists and has an IP
    if ip -4 addr show dev "${br_name}" 2>/dev/null | grep -q 'inet '; then
        local br_ip
        br_ip="$(ip -4 -o addr show dev "${br_name}" scope global 2>/dev/null | awk '{print $4}')"
        echo "  = ${br_name} has an IPv4 address (${br_ip})"
        if [ "$BRIDGE_DHCP" = "false" ] && [ -n "${BRIDGE_STATIC_IP:-}" ] && [ "${br_ip}" != "${BRIDGE_STATIC_IP}" ]; then
            echo "  ! WARNING: ${br_name} has ${br_ip} but static IP is ${BRIDGE_STATIC_IP}"
        fi
    else
        echo "  ! WARNING: ${br_name} has no IPv4 address"
    fi

    # 2. The macvlan exists and is a bridge port
    if [ -d "/sys/class/net/${mv_name}" ]; then
        echo "  = macvlan ${mv_name} exists"
        if [ -d "/sys/class/net/${mv_name}/brport" ]; then
            echo "  = ${mv_name} is attached to a bridge (correct)"
        else
            echo "  ! WARNING: ${mv_name} is NOT a bridge port"
        fi
    else
        echo "  ! WARNING: macvlan ${mv_name} does not exist"
    fi

    # 3. The physical interface still has its original IP (or at least is up)
    if ip -4 addr show dev "${slave_iface}" scope global 2>/dev/null | grep -q 'inet '; then
        local phys_ip
        phys_ip="$(ip -4 -o addr show dev "${slave_iface}" scope global 2>/dev/null | awk '{print $4}')"
        echo "  = ${slave_iface} retains its IP (${phys_ip}) - correct"
    else
        echo "  = ${slave_iface} has no IP (expected if it was already a bridge port)"
    fi

    # 4. Default route still exists (should be via the physical iface)
    local def_route
    def_route="$(ip route show default 2>/dev/null | head -1)"
    if [ -n "${def_route}" ]; then
        echo "  = default route: ${def_route}"
    else
        echo "  ! WARNING: no default route"
    fi
}

# --- Physical bridge (vmbr0) — REQUIRED shared L2 -------------------------
# WebKVM requires a PHYSICAL Linux bridge (vmbr0/br0) attached to the host's
# physical NIC so KVM and Incus share the real LAN (Layer-2, IPs from the
# router via DHCP — Proxmox-style). NAT / virtual bridges are never used.
default_route_iface() {
    ip route show default 2>/dev/null | awk '{print $5; exit}'
}

# warn_dhcp_reservation prints the DHCP-reservation caveat: moving a DHCP
# IP onto a bridge promotes the lease, but the router still treats the IP
# as part of its pool and could hand it to another device on renewal.
warn_dhcp_reservation() {
    local br="$1" iface="$2" mac
    mac="$(cat "/sys/class/net/${iface}/address" 2>/dev/null)"
    [ -z "${mac}" ] && mac="<MAC of ${iface}>"
    cat <<EOF

  ⚠  DHCP RESERVATION REQUIRED: ${iface} (MAC ${mac}) is on DHCP. Moving its
     IP onto bridge ${br} promotes that lease, but your router still treats
     the IP as part of the DHCP pool. Add a DHCP reservation for ${mac} in
     the router (or exclude the IP from the range) BEFORE the lease renews,
     otherwise the router could hand the same IP to another device and break
     the bridge.
EOF
}

# apply_bridge_nmcli creates a bridge over a physical NIC via NetworkManager.
apply_bridge_nmcli() {
    local br="$1" iface="$2"
    echo "  + creating bridge ${br} on ${iface} via nmcli (IP moves to the bridge)"
    nmcli con add type bridge con-name "${br}" ifname "${br}" >/dev/null 2>&1 || return 1
    nmcli con add type ethernet con-name "${br}-${iface}" ifname "${iface}" master "${br}" >/dev/null 2>&1 || return 1
    # The enslaved port must not keep a DHCP client running on the bridge.
    nmcli con modify "${br}-${iface}" ipv4.method disabled >/dev/null 2>&1 || true
    if [ -n "${BRIDGE_STATIC_IP:-}" ]; then
        nmcli con modify "${br}" ipv4.method manual ipv4.addresses "${BRIDGE_STATIC_IP}" \
            ipv4.gateway "${BRIDGE_STATIC_GW:-}" ipv4.dns "${BRIDGE_STATIC_DNS:-}" >/dev/null 2>&1 || true
    else
        nmcli con modify "${br}" ipv4.method auto ipv6.method auto >/dev/null 2>&1 || true
    fi
    nmcli con up "${br}-${iface}" >/dev/null 2>&1 || true
    nmcli con up "${br}" >/dev/null 2>&1 || true
    [ -d "/sys/class/net/${br}/bridge" ]
}

# apply_bridge_netplan writes a netplan config and applies it (DHCP default,
# static when BRIDGE_STATIC_IP is set).
apply_bridge_netplan() {
    local br="$1" iface="$2" yaml="/etc/netplan/zz-webkvm-${br}.yaml"
    if [ -n "${BRIDGE_STATIC_IP:-}" ]; then
        sudo tee "${yaml}" >/dev/null <<EOF
network:
  version: 2
  ethernets:
    ${iface}:
      dhcp4: false
      dhcp6: false
  bridges:
    ${br}:
      interfaces: [${iface}]
      addresses: [${BRIDGE_STATIC_IP}]
      routes:
        - to: default
          via: ${BRIDGE_STATIC_GW}
      nameservers:
        addresses: [$(echo "${BRIDGE_STATIC_DNS}" | tr ',' ' ')]
EOF
    else
        sudo tee "${yaml}" >/dev/null <<EOF
network:
  version: 2
  ethernets:
    ${iface}:
      dhcp4: false
      dhcp6: false
  bridges:
    ${br}:
      interfaces: [${iface}]
      dhcp4: true
      dhcp6: true
EOF
    fi
    sudo netplan apply
    [ -d "/sys/class/net/${br}/bridge" ]
}

print_netplan_instructions() {
    local br="$1" iface="$2"
    if [ -n "${BRIDGE_STATIC_IP:-}" ]; then
        cat <<EOF

  ══════════════════════════════════════════════════════════════════════
  WebKVM requires a PHYSICAL Linux bridge (${br}) so KVM and Incus share
  your real LAN. La IP actual se fijará como ESTÁTICA (${BRIDGE_STATIC_IP}).
  Añade una reserva DHCP en el router para la MAC de ${iface}.

  Automatic creation was NOT applied (it would move the IP and could drop
  your SSH session). Create the bridge with Netplan:

    sudo tee /etc/netplan/zz-webkvm-${br}.yaml >/dev/null <<'YAML'
network:
  version: 2
  ethernets:
    ${iface}:
      dhcp4: false
      dhcp6: false
  bridges:
    ${br}:
      interfaces: [${iface}]
      addresses: [${BRIDGE_STATIC_IP}]
      routes:
        - to: default
          via: ${BRIDGE_STATIC_GW}
      nameservers:
        addresses: [$(echo "${BRIDGE_STATIC_DNS}" | tr ',' ' ')]
YAML
    sudo netplan apply
    ip -br addr show ${br}

  (Alternatively, with NetworkManager:)
    nmcli con add type bridge con-name ${br} ifname ${br} ipv4.method manual \\
      ipv4.addresses ${BRIDGE_STATIC_IP} ipv4.gateway ${BRIDGE_STATIC_GW} \\
      ipv4.dns $(echo "${BRIDGE_STATIC_DNS}" | tr ',' ' ')
    nmcli con add type ethernet con-name ${br}-${iface} ifname ${iface} master ${br}
    nmcli con modify ${br}-${iface} ipv4.method disabled
    nmcli con up ${br}-${iface} && nmcli con up ${br}
  ══════════════════════════════════════════════════════════════════════
EOF
    else
        cat <<EOF

  ══════════════════════════════════════════════════════════════════════
  WebKVM requires a PHYSICAL Linux bridge (${br}) so KVM and Incus share
  your real LAN (Layer-2, IPs from your router via DHCP).

  Automatic creation was NOT applied (it would move the IP and could drop
  your SSH session). Create the bridge with Netplan:

    sudo tee /etc/netplan/zz-webkvm-${br}.yaml >/dev/null <<'YAML'
network:
  version: 2
  ethernets:
    ${iface}:
      dhcp4: false
      dhcp6: false
  bridges:
    ${br}:
      interfaces: [${iface}]
      dhcp4: true
      dhcp6: true
YAML
    sudo netplan apply
    ip -br addr show ${br}

  (Alternatively, with NetworkManager:)
    nmcli con add type bridge con-name ${br} ifname ${br}
    nmcli con add type ethernet con-name ${br}-${iface} ifname ${iface} master ${br}
    nmcli con modify ${br} ipv4.method auto
    nmcli con up ${br}-${iface} && nmcli con up ${br}
  ══════════════════════════════════════════════════════════════════════
EOF
    fi
}

# apply_bridge_networkd creates vmbr0 over a physical NIC via systemd-networkd
# (Arch, Debian/CoreOS without NetworkManager). Static when BRIDGE_STATIC_IP
# is set (the pinned lease), else DHCP on the bridge.
apply_bridge_networkd() {
    local br="$1" iface="$2"
    backup_managed_file "/etc/systemd/network/${br}.netdev"
    backup_managed_file "/etc/systemd/network/${br}.network"
    backup_managed_file "/etc/systemd/network/${iface}.network"

    write_managed_file "/etc/systemd/network/${br}.netdev" \
"[NetDev]
Name=${br}
Kind=bridge
"

    if [ -n "${BRIDGE_STATIC_IP:-}" ]; then
        local dns_block=""
        local IFS=','
        for d in ${BRIDGE_STATIC_DNS}; do
            d="$(echo "${d}" | xargs)"
            [ -z "${d}" ] && continue
            dns_block="${dns_block}
DNS=${d}"
        done
        unset IFS
        write_managed_file "/etc/systemd/network/${br}.network" \
"[Match]
Name=${br}

[Network]
# Static IP — pinned from the previous DHCP lease at install time.
Address=${BRIDGE_STATIC_IP}
Gateway=${BRIDGE_STATIC_GW}${dns_block}
"
    else
        write_managed_file "/etc/systemd/network/${br}.network" \
"[Match]
Name=${br}

[Network]
DHCP=yes
"
    fi

    write_managed_file "/etc/systemd/network/${iface}.network" \
"[Match]
Name=${iface}

[Network]
Bridge=${br}
DHCP=no
IPv6AcceptRA=no
"
    sudo systemctl enable systemd-networkd >/dev/null 2>&1 || true
    sudo systemctl restart systemd-networkd >/dev/null 2>&1 || true
    [ -d "/sys/class/net/${br}/bridge" ]
}

# apply_bridge_ifupdown creates vmbr0 over a physical NIC via ifupdown
# (/etc/network/interfaces — Debian minimal, Proxmox-style: bridge-ports,
# stp off, fd 0).
apply_bridge_ifupdown() {
    local br="$1" iface="$2"
    local f="/etc/network/interfaces.d/50-webkvm-${br}"
    sudo mkdir -p /etc/network/interfaces.d
    if [ -n "${BRIDGE_STATIC_IP:-}" ]; then
        sudo tee "${f}" >/dev/null <<EOF
# Managed by webkvm
auto ${br}
iface ${br} inet static
    address ${BRIDGE_STATIC_IP}
    gateway ${BRIDGE_STATIC_GW}
    bridge-ports ${iface}
    bridge-stp off
    bridge-fd 0
EOF
    else
        sudo tee "${f}" >/dev/null <<EOF
# Managed by webkvm
auto ${br}
iface ${br} inet dhcp
    bridge-ports ${iface}
    bridge-stp off
    bridge-fd 0
EOF
    fi
    # Ensure /etc/network/interfaces sources interfaces.d (Debian default).
    if ! grep -qs "source.*interfaces.d" /etc/network/interfaces 2>/dev/null; then
        echo "source /etc/network/interfaces.d/*" | sudo tee -a /etc/network/interfaces >/dev/null
    fi
    if command -v ifreload >/dev/null 2>&1; then
        sudo ifreload -a >/dev/null 2>&1 || true
    elif command -v ifup >/dev/null 2>&1; then
        sudo ifup "${br}" >/dev/null 2>&1 || true
    fi
    [ -d "/sys/class/net/${br}/bridge" ]
}

# ensure_physical_bridge reuses an existing physical bridge, or creates
# vmbr0 (nmcli → netplan), or prints exact instructions and fails. Never
# falls back to NAT/macvlan.
ensure_physical_bridge() {
    local br="${BR_NAME:-vmbr0}"

    # 1. Reuse an existing physical bridge (vmbr0, br0, or BR_NAME).
    local cand existing=""
    for cand in vmbr0 br0 "${br}"; do
        if [ -d "/sys/class/net/${cand}/bridge" ]; then
            existing="${cand}"
            break
        fi
    done
    if [ -n "${existing}" ]; then
        echo "  = physical bridge '${existing}' already present"
        BR_NAME="${existing}"
        return 0
    fi

    # 2. Detect the physical interface (default route, then any NIC).
    local iface="${BRIDGE_SLAVE:-}"
    [ -z "${iface}" ] && iface="$(default_route_iface)"
    [ -z "${iface}" ] && iface="$(pick_physical_iface 2>/dev/null || true)"
    if [ -z "${iface}" ]; then
        echo "  ! cannot detect a physical interface to bind ${br} to" >&2
        return 1
    fi

    # Moving a DHCP IP onto the bridge promotes the lease; warn so the
    # admin adds a router reservation before the renewal breaks the bridge.
    if ip -4 addr show dev "${iface}" scope global 2>/dev/null | grep -q "dynamic"; then
        warn_dhcp_reservation "${br}" "${iface}"
    fi

    # 3. NetworkManager + nmcli (preferred when available).
    if command -v nmcli >/dev/null 2>&1 && systemctl is-active --quiet NetworkManager 2>/dev/null; then
        if [ "${BRIDGE_APPLY:-0}" = "1" ] && apply_bridge_nmcli "${br}" "${iface}"; then
            return 0
        fi
        echo "  = NetworkManager present but BRIDGE_APPLY!=1 — printing instructions instead of risking the SSH session"
    fi

    # 4. Netplan (modern Ubuntu/Debian) — apply only when explicitly asked.
    if [ -d /etc/netplan ] && command -v netplan >/dev/null 2>&1; then
        if [ "${BRIDGE_APPLY:-0}" = "1" ] && apply_bridge_netplan "${br}" "${iface}"; then
            return 0
        fi
        echo "  = Netplan present but BRIDGE_APPLY!=1 — printing instructions"
    fi

    # 5. systemd-networkd (no netplan: Arch, Debian without NetworkManager).
    if ! [ -d /etc/netplan ] && systemctl is-active --quiet systemd-networkd 2>/dev/null; then
        if [ "${BRIDGE_APPLY:-0}" = "1" ] && apply_bridge_networkd "${br}" "${iface}"; then
            return 0
        fi
        echo "  = systemd-networkd present but BRIDGE_APPLY!=1 — printing instructions"
    fi

    # 6. ifupdown (/etc/network/interfaces — Debian minimal, Proxmox-style).
    if ! [ -d /etc/netplan ] && ! systemctl is-active --quiet systemd-networkd 2>/dev/null \
        && [ -d /etc/network/interfaces.d ] && command -v ifup >/dev/null 2>&1; then
        if [ "${BRIDGE_APPLY:-0}" = "1" ] && apply_bridge_ifupdown "${br}" "${iface}"; then
            return 0
        fi
        echo "  = ifupdown present but BRIDGE_APPLY!=1 — printing instructions"
    fi

    # 7. Instructions (safe default: never risk dropping SSH automatically).
    print_netplan_instructions "${br}" "${iface}"
    return 1
}

# --- bridge hardening (sysctl + firewall FORWARD) ---------------------------
# Shared-L2 bridges need IP forwarding so VMs/containers can route when the
# host must forward, and must NOT be re-filtered by the host firewall when
# br_netfilter is loaded (otherwise DHCP/ARP on the bridge get dropped and
# the containers look isolated). Applied on every distro; idempotent.
apply_bridge_sysctl() {
    local f="/etc/sysctl.d/60-webkvm-bridge.conf"
    if [ -f "${f}" ] && ! grep -qF "$MANAGED_MARKER" "${f}"; then
        echo "  ! ${f} exists without our marker, leaving alone"
        return 0
    fi
    local tmp
    tmp="$(mktemp)"
    {
        echo "# $MANAGED_MARKER"
        echo "# WebKVM shared-L2 bridge: IP forwarding on, and if br_netfilter"
        echo "# is loaded the bridge L2 traffic must NOT be filtered by the host"
        echo "# firewall (netfilter), or container DHCP/ARP on the bridge breaks."
        echo "net.ipv4.ip_forward = 1"
        echo "net.ipv6.conf.all.forwarding = 1"
        if [ -d /proc/sys/net/bridge ]; then
            echo "net.bridge.bridge-nf-call-iptables = 0"
            echo "net.bridge.bridge-nf-call-ip6tables = 0"
        fi
    } > "$tmp"
    sudo install -m 0644 "$tmp" "${f}"
    rm -f "$tmp"
    sudo sysctl -p "${f}" >/dev/null 2>&1 || true
    if [ -d /proc/sys/net/bridge ]; then
        sudo sysctl -w net.bridge.bridge-nf-call-iptables=0 net.bridge.bridge-nf-call-ip6tables=0 >/dev/null 2>&1 || true
    fi
    echo "  + sysctl: ip_forward=1 + bridge L2 not filtered by host firewall (${f})"
}

# allow_bridge_forward opens the host FORWARD chain for bridge traffic on the
# distro's firewall. With br_netfilter loaded, bridged DHCP/ARP would traverse
# FORWARD and get dropped by restrictive defaults (firewalld/UFW drop it).
# Harmless no-op when no firewall is active (the common case).
allow_bridge_forward() {
    local br="$1"

    # firewalld: move the bridge into the trusted zone (accepts everything,
    # including FORWARD) — avoids per-rule whack-a-mole.
    if command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active --quiet firewalld 2>/dev/null; then
        if ! firewall-cmd --list-interfaces --zone=trusted 2>/dev/null | grep -qx "${br}"; then
            sudo firewall-cmd --permanent --zone=trusted --add-interface="${br}" >/dev/null 2>&1 || true
            sudo firewall-cmd --reload >/dev/null 2>&1 || true
        fi
        echo "  + firewalld: ${br} in trusted zone (bridge L2/DHCP allowed)"
        return 0
    fi

    # ufw: insert FORWARD accept into before.rules' filter section (first
    # COMMIT), because ufw's default forward policy drops everything.
    if command -v ufw >/dev/null 2>&1 && sudo ufw status 2>/dev/null | grep -q "Status: active"; then
        local f="/etc/ufw/before.rules"
        if ! sudo grep -qF "webkvm bridge ${br}" "${f}" 2>/dev/null; then
            sudo awk -v br="${br}" '
                /^COMMIT$/ && !done {
                    print "# webkvm bridge " br " FORWARD allow"
                    print "-A ufw-before-forward -i " br " -o " br " -j ACCEPT"
                    done = 1
                }
                { print }
            ' "${f}" > "${f}.tmp" && sudo mv "${f}.tmp" "${f}"
        fi
        echo "  + ufw: FORWARD ACCEPT for ${br} added to before.rules"
        return 0
    fi

    # raw iptables/nftables: FORWARD accept between bridge ports.
    if command -v iptables >/dev/null 2>&1; then
        sudo iptables -C FORWARD -i "${br}" -o "${br}" -j ACCEPT 2>/dev/null \
            || sudo iptables -I FORWARD -i "${br}" -o "${br}" -j ACCEPT 2>/dev/null || true
        echo "  + iptables: FORWARD ACCEPT -i ${br} -o ${br}"
        return 0
    fi

    echo "  = no active firewall detected; nothing to open for ${br}"
}

# configure_incus_default_profile points the Incus "default" profile NIC at
# the host's PHYSICAL Linux bridge (vmbr0/br0) so any `incus launch` — and
# containers WebKVM creates through that profile — land on the real LAN, never
# on the factory NAT lxdbr0. Handles the `incus` and `lxc` (LXD snap) CLIs.
configure_incus_default_profile() {
    local br="$1" cli=""
    if command -v incus >/dev/null 2>&1; then
        cli="incus"
    elif command -v lxc >/dev/null 2>&1; then
        cli="lxc"
    fi
    if [ -z "${cli}" ]; then
        echo "  = no Incus/LXD CLI found; skipping default-profile bridge wiring"
        return 0
    fi
    if [ ! -d "/sys/class/net/${br}/bridge" ]; then
        echo "  ! ${cli}: bridge ${br} not present; leaving default profile untouched"
        return 0
    fi
    # Modern LXD/Incus NICs use the "network" property (points at a managed
    # NAT network, e.g. lxdbr0). Unset it so parent= + nictype=bridged win;
    # a remove+add fallback covers stubborn states.
    sudo "${cli}" profile device unset default eth0 network 2>/dev/null || true
    if ! sudo "${cli}" profile device set default eth0 parent="${br}" nictype=bridged 2>/dev/null; then
        sudo "${cli}" profile device remove default eth0 2>/dev/null || true
        sudo "${cli}" profile device add default eth0 nic nictype=bridged parent="${br}" name=eth0 2>/dev/null || true
    fi
    if [ "$(sudo "${cli}" profile device get default eth0 parent 2>/dev/null)" = "${br}" ]; then
        echo "  + ${cli}: default profile eth0 → ${br} (nictype=bridged, shared L2 — no lxdbr0)"
    else
        echo "  ! ${cli}: could not point default profile eth0 at ${br} (is the daemon running?)"
    fi
}

# disable_libvirt_nat_default stops libvirt's factory NAT "default" network
# (virbr0) and disables its autostart. WebKVM requires shared L2 — VMs must
# attach to the physical bridge, never to the isolated NAT network.
disable_libvirt_nat_default() {
    if ! sudo virsh net-list --all --name 2>/dev/null | grep -qx default; then
        echo "  = libvirt 'default' NAT network not defined (nothing to disable)"
        return 0
    fi
    if sudo virsh net-list --name 2>/dev/null | grep -qx default; then
        sudo virsh net-destroy default >/dev/null 2>&1 || true
        echo "  - libvirt 'default' NAT network: stopped"
    fi
    if sudo virsh net-list --autostart --name 2>/dev/null | grep -qx default; then
        sudo virsh net-autostart --disable default >/dev/null 2>&1 || true
        echo "  - libvirt 'default' NAT network: autostart disabled"
    fi
}

# --- vmbr1 (NAT/aislada) — Proxmox-style ------------------------------------
# vmbr0 is the physical LAN bridge (real L2, DHCP from the router). In
# addition, Proxmox keeps isolated bridges (vmbr1, vmbr2, …) "up" by
# anchoring them to a kernel `dummy` interface. WebKVM mirrors that: vmbr1
# is a real Linux bridge on dummy0 with a static 100.0.0.1/24 and NAT
# (MASQUERADE) so its tenants reach the internet through the main uplink —
# exactly the /etc/network/interfaces pattern:
#
#   auto dummy0
#   iface dummy0 inet manual
#       pre-up ip link add dummy0 type dummy 2>/dev/null || true
#       up ip link set dummy0 up
#
#   auto vmbr1
#   iface vmbr1 inet static
#       address 100.0.0.1/24
#       bridge-ports dummy0
#       bridge-stp off
#       bridge-fd 0
VM1_NET="100.0.0.0/24"
VM1_IP="100.0.0.1/24"
VM1_BR="vmbr1"
VM1_DUMMY="dummy0"

exit_uplink() {
    ip route show default 2>/dev/null | awk '{print $5; exit}'
}

# ensure_dummy_dev creates the kernel dummy interface (keeps the isolated
# bridge "up" without a physical NIC, exactly like Proxmox).
ensure_dummy_dev() {
    local name="$1"
    if [ ! -d "/sys/class/net/${name}" ]; then
        echo "  + creating dummy interface ${name}"
        sudo ip link add "${name}" type dummy 2>/dev/null \
            || echo "  ! could not create ${name} (kernel dummy module?)"
    fi
    sudo ip link set "${name}" up 2>/dev/null || true
}

# apply_nat_vmbr1 sets up MASQUERADE for the isolated bridge on the host's
# firewall (firewalld → ufw → iptables/nftables), with the FORWARD accept
# the routed traffic needs.
apply_nat_vmbr1() {
    local uplink="${1:-}"
    [ -n "${uplink}" ] || uplink="$(exit_uplink)"
    [ -n "${uplink}" ] || { echo "  = no default uplink; skipping NAT for ${VM1_BR}"; return 0; }

    # firewalld: masquerade + FORWARD accept for the isolated zone.
    if command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active --quiet firewalld 2>/dev/null; then
        sudo firewall-cmd --permanent --zone=internal --add-interface="${VM1_BR}" >/dev/null 2>&1 || true
        sudo firewall-cmd --permanent --zone=internal --add-masquerade >/dev/null 2>&1 || true
        sudo firewall-cmd --reload >/dev/null 2>&1 || true
        echo "  + firewalld: ${VM1_BR} in internal zone with masquerade (NAT → ${uplink})"
        return 0
    fi

    # ufw: NAT masquerade in before.rules' *nat section + FORWARD accept.
    if command -v ufw >/dev/null 2>&1 && sudo ufw status 2>/dev/null | grep -q "Status: active"; then
        local f="/etc/ufw/before.rules"
        if ! sudo grep -qF "webkvm vmbr1 masquerade" "${f}" 2>/dev/null; then
            sudo awk -v net="${VM1_NET}" -v uplink="${uplink}" '
                /^COMMIT$/ && !done && !natdone && !seen_nat {
                    print "# webkvm vmbr1 masquerade (NAT to " uplink ")"
                    print "*nat"
                    print ":POSTROUTING ACCEPT [0:0]"
                    print "-A POSTROUTING -s " net " -o " uplink " -j MASQUERADE"
                    print "COMMIT"
                    seen_nat = 1
                }
                { print }
            ' "${f}" > "${f}.tmp" && sudo mv "${f}.tmp" "${f}"
        fi
        echo "  + ufw: MASQUERADE ${VM1_NET} → ${uplink} added to before.rules"
        return 0
    fi

    # raw iptables/nftables.
    if command -v iptables >/dev/null 2>&1; then
        sudo iptables -t nat -C POSTROUTING -s "${VM1_NET}" -o "${uplink}" -j MASQUERADE 2>/dev/null \
            || sudo iptables -t nat -A POSTROUTING -s "${VM1_NET}" -o "${uplink}" -j MASQUERADE 2>/dev/null || true
        sudo iptables -C FORWARD -i "${VM1_BR}" -j ACCEPT 2>/dev/null \
            || sudo iptables -I FORWARD -i "${VM1_BR}" -j ACCEPT 2>/dev/null || true
        sudo iptables -C FORWARD -o "${VM1_BR}" -j ACCEPT 2>/dev/null \
            || sudo iptables -I FORWARD -o "${VM1_BR}" -j ACCEPT 2>/dev/null || true
        echo "  + iptables: MASQUERADE ${VM1_NET} → ${uplink} + FORWARD accept on ${VM1_BR}"
        return 0
    fi
    echo "  = no firewall detected; NAT for ${VM1_BR} not configured"
}

# ensure_vmbr1_nat creates the isolated bridge vmbr1 on dummy0 (100.0.0.1/24)
# with NAT, with persistence through netplan / systemd-networkd / nmcli.
ensure_vmbr1_nat() {
    ensure_dummy_dev "${VM1_DUMMY}"

    if [ ! -d "/sys/class/net/${VM1_BR}/bridge" ]; then
        echo "  + creating Linux bridge ${VM1_BR} (slave: ${VM1_DUMMY})"
        sudo ip link add name "${VM1_BR}" type bridge
        sudo ip link set "${VM1_DUMMY}" master "${VM1_BR}"
        sudo ip link set "${VM1_BR}" up
    else
        echo "  = Linux bridge ${VM1_BR} already exists"
    fi

    if ! ip -4 -o addr show dev "${VM1_BR}" scope global 2>/dev/null | grep -q "${VM1_IP%/*}"; then
        sudo ip addr flush dev "${VM1_BR}" scope global 2>/dev/null || true
        sudo ip addr add "${VM1_IP}" dev "${VM1_BR}" 2>/dev/null || true
        echo "  + ${VM1_BR}: IP ${VM1_IP} assigned"
    fi

    apply_nat_vmbr1

    # DHCP for the isolated bridge: Proxmox serves its NAT vmbr networks
    # with dnsmasq. The host dnsmasq (bind-interfaces on vmbr1 only) hands
    # out 100.0.0.100-200 with the bridge as gateway — containers/cloud-init
    # get an IP and the MASQUERADE rule carries them out to the internet.
    if command -v dnsmasq >/dev/null 2>&1 && [ -d /etc/dnsmasq.d ]; then
        write_managed_file "/etc/dnsmasq.d/webkvm-${VM1_BR}.conf" \
"# webkvm ${VM1_BR} DHCP (Proxmox-style isolated NAT bridge)
interface=${VM1_BR}
bind-interfaces
dhcp-range=100.0.0.100,100.0.0.200,255.255.255.0,12h
dhcp-option=option:router,100.0.0.1
dhcp-option=option:dns-server,1.1.1.1
"
        if systemctl list-unit-files dnsmasq.service >/dev/null 2>&1; then
            sudo systemctl enable dnsmasq >/dev/null 2>&1 || true
            sudo systemctl restart dnsmasq >/dev/null 2>&1 || true
        elif command -v dnsmasq >/dev/null 2>&1; then
            # Only dnsmasq-base is present (no dnsmasq.service, e.g. Ubuntu):
            # run the binary directly through a dedicated unit.
            local svc="webkvm-${VM1_BR}-dnsmasq.service"
            write_managed_file "/etc/systemd/system/${svc}" \
"[Unit]
Description=webkvm dnsmasq for ${VM1_BR} (isolated NAT bridge DHCP)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/sbin/dnsmasq --keep-in-foreground --conf-file=/etc/dnsmasq.d/webkvm-${VM1_BR}.conf
Restart=on-failure

[Install]
WantedBy=multi-user.target
"
            systemctl daemon-reload >/dev/null 2>&1 || true
            sudo systemctl enable "${svc}" >/dev/null 2>&1 || true
            sudo systemctl restart "${svc}" >/dev/null 2>&1 || true
        fi
        echo "  + dnsmasq: ${VM1_BR} DHCP (100.0.0.100-200) configured"
    fi

    # --- persistence ---
    if [ -d /etc/netplan ] && command -v netplan >/dev/null 2>&1; then
        # netplan cannot create dummy devices; a boot-time oneshot does.
        local svc="webkvm-dummy0.service"
        write_managed_file "/etc/systemd/system/${svc}" \
"[Unit]
Description=webkvm ${VM1_DUMMY} (anchor for isolated bridge ${VM1_BR})
Before=network-pre.target
Wants=network-pre.target
[Service]
Type=oneshot
ExecStart=/bin/sh -c 'ip link add ${VM1_DUMMY} type dummy 2>/dev/null || true; ip link set ${VM1_DUMMY} up'
RemainAfterExit=yes
[Install]
WantedBy=multi-user.target
"
        systemctl daemon-reload 2>/dev/null || true
        systemctl enable "${svc}" 2>/dev/null || true
        write_managed_file "/etc/netplan/zz-webkvm-${VM1_BR}.yaml" \
"network:
  version: 2
  bridges:
    ${VM1_BR}:
      interfaces: [${VM1_DUMMY}]
      addresses: [${VM1_IP}]
      parameters:
        stp: false
"
        if [ "${BRIDGE_APPLY:-0}" = "1" ]; then
            sudo netplan apply 2>/dev/null || true
        fi
        echo "  + netplan: ${VM1_BR} (${VM1_IP}) + dummy0 oneshot written"
    elif command -v nmcli >/dev/null 2>&1 && systemctl is-active --quiet NetworkManager 2>/dev/null; then
        if ! nmcli con show "${VM1_BR}" >/dev/null 2>&1; then
            nmcli con add type bridge con-name "${VM1_BR}" ifname "${VM1_BR}" ipv4.addresses "${VM1_IP}" ipv4.method manual >/dev/null 2>&1 || true
            nmcli con add type ethernet con-name "${VM1_BR}-${VM1_DUMMY}" ifname "${VM1_DUMMY}" master "${VM1_BR}" >/dev/null 2>&1 || true
        fi
        echo "  + nmcli: ${VM1_BR} (${VM1_IP}) bridge connection written"
    elif systemctl is-active --quiet systemd-networkd 2>/dev/null; then
        write_managed_file "/etc/systemd/network/${VM1_DUMMY}.netdev" \
"[NetDev]
Name=${VM1_DUMMY}
Kind=dummy
"
        write_managed_file "/etc/systemd/network/${VM1_BR}.netdev" \
"[NetDev]
Name=${VM1_BR}
Kind=bridge
"
        write_managed_file "/etc/systemd/network/${VM1_DUMMY}.network" \
"[Match]
Name=${VM1_DUMMY}
[Network]
Bridge=${VM1_BR}
"
        write_managed_file "/etc/systemd/network/${VM1_BR}.network" \
"[Match]
Name=${VM1_BR}
[Network]
Address=${VM1_IP}
"
        echo "  + systemd-networkd: ${VM1_DUMMY} + ${VM1_BR} (${VM1_IP}) written"
    else
        echo "  ! ${VM1_BR} created live only — configure persistence manually (netplan/systemd-networkd)"
    fi
}

# --- main -------------------------------------------------------------------
echo "=== webkvm network setup (mode: ${MODE}) ==="

# Pre-flight
if ! command -v systemctl >/dev/null; then
    echo "  ! no systemctl found; this script needs systemd"
    exit 1
fi
if ! systemctl is-active --quiet libvirtd; then
    echo "  ! libvirtd is not running; start it with: systemctl start libvirtd"
    exit 1
fi

# 1. Disable conflicting DHCP clients
echo "[1/5] disabling conflicting DHCP clients"
disable_conflicting_dhcp_clients

# 2. libvirt default NAT network (always for NAT and Both modes)
if [[ "${MODE}" != "bridge" ]]; then
    echo "[2/5] libvirt default NAT network"
    ensure_default_network
else
    echo "[2/5] libvirt default NAT network (skipped in bridge-only mode)"
fi

# 3. Pick the candidate interface
echo "[3/5] selecting host interface"
IFACE=""
EXISTING_BRIDGE=""
if [ -n "${BRIDGE_SLAVE:-}" ]; then
    if [ -d "/sys/class/net/${BRIDGE_SLAVE}" ]; then
        IFACE="${BRIDGE_SLAVE}"
        echo "  + selected ${IFACE} (BRIDGE_SLAVE override)"
    else
        echo "  ! BRIDGE_SLAVE=${BRIDGE_SLAVE} but that iface doesn't exist" >&2
        exit 1
    fi
else
    IFACE="$(pick_physical_iface || true)"
    if [ -z "${IFACE}" ]; then
        echo "  ! no candidate physical interface found"
    else
        echo "  + selected ${IFACE}"
    fi
fi

# If the selected interface is already a bridge port, check for existing bridge
if [ -n "${IFACE}" ] && [ -d "/sys/class/net/${IFACE}/brport" ]; then
    # Find the bridge this interface belongs to
    BRIDGE_MASTER="$(basename "$(readlink -f /sys/class/net/${IFACE}/master)" 2>/dev/null || true)"
    if [ -n "${BRIDGE_MASTER}" ] && [ -d "/sys/class/net/${BRIDGE_MASTER}/bridge" ]; then
        echo "  + ${IFACE} is already a port of bridge ${BRIDGE_MASTER}, reusing existing bridge"
        EXISTING_BRIDGE="${BRIDGE_MASTER}"
        BR_NAME="${EXISTING_BRIDGE}"
    fi
fi

# 4. Resolve static IP/GW/DNS (env vars > auto-detect from the current
# config). Default in bridge mode: pin the current address (DHCP lease →
# static) so vmbr0 keeps the same IP across reboots. --dhcp keeps DHCP.
NEEDS_STATIC_IP=false

# Re-run safety: if a physical bridge already exists, it already carries the
# host IP — reuse it and skip static resolution (nothing to pin).
EXISTING_PHYS_BR=""
if [ -n "${BRIDGE_NAME:-}" ] && [ -d "/sys/class/net/${BRIDGE_NAME}/bridge" ]; then
    EXISTING_PHYS_BR="${BRIDGE_NAME}"
fi
if [ -z "${EXISTING_PHYS_BR}" ] && [ -d /sys/class/net/vmbr0/bridge ]; then
    EXISTING_PHYS_BR="vmbr0"
fi
if [ -z "${EXISTING_PHYS_BR}" ] && [ -d /sys/class/net/br0/bridge ]; then
    EXISTING_PHYS_BR="br0"
fi
if [ -n "${EXISTING_PHYS_BR}" ]; then
    echo "  = bridge físico '${EXISTING_PHYS_BR}' ya presente: se reutiliza con su IP actual"
    BRIDGE_DHCP=false
elif [ "$DIRECT_BRIDGE" = "true" ]; then
    NEEDS_STATIC_IP=true
elif [[ "${MODE}" == "bridge" || "${MODE}" == "both" ]]; then
    # Default: pin the current address (DHCP lease → static). --dhcp keeps
    # the bridge on DHCP; an explicit BRIDGE_STATIC_IP always wins.
    if [ "$BRIDGE_DHCP" = "true" ] && [ -z "${BRIDGE_STATIC_IP:-}" ]; then
        NEEDS_STATIC_IP=false
    else
        NEEDS_STATIC_IP=true
    fi
elif [[ "${MODE}" == "nat" || "${MODE}" == "both" ]] && [ -n "${BRIDGE_STATIC_IP:-}" ]; then
    NEEDS_STATIC_IP=true
fi

# Early y/n: if the interface is on DHCP and we are about to pin it as
# static, ask the operator to accept it before touching anything.
if [ "$NEEDS_STATIC_IP" = "true" ] && [ -n "${IFACE:-}" ] && is_iface_dhcp "${IFACE}"; then
    cur_ip="$(ip -4 -o addr show dev "${IFACE}" scope global 2>/dev/null | awk '{print $4}' | head -1)"
    if ! confirm_pin_static "${IFACE}" "${cur_ip:-<sin IP>}"; then
        echo "  ! Operador: la IP actual NO se fija — el bridge se dejará en DHCP." >&2
        BRIDGE_DHCP=true
        NEEDS_STATIC_IP=false
    fi
fi

if [ -n "${IFACE}" ]; then
    if [ "$NEEDS_STATIC_IP" = "true" ]; then
        # Must have a static IP — resolve from env or auto-detect
        if [ -z "${BRIDGE_STATIC_IP:-}" ] || [ -z "${BRIDGE_STATIC_GW:-}" ] || [ -z "${BRIDGE_STATIC_DNS:-}" ]; then
            echo "  > auto-detecting IP/gateway/DNS from ${IFACE}..."
            DETECTED="$(detect_static_for_iface "${IFACE}" || true)"
            if [ -z "${DETECTED}" ]; then
                echo "ERROR: couldn't auto-detect static IP on ${IFACE}." >&2
                echo "       Re-run with explicit env vars:" >&2
                echo "         sudo BRIDGE_STATIC_IP=192.168.1.100/24 \\" >&2
                echo "              BRIDGE_STATIC_GW=192.168.1.1 \\" >&2
                echo "              BRIDGE_STATIC_DNS=1.1.1.1,8.8.8.8 \\" >&2
                echo "              $0" >&2
                exit 1
            fi
            eval "${DETECTED}"
            export BRIDGE_STATIC_IP BRIDGE_STATIC_GW BRIDGE_STATIC_DNS
            echo "    IP:  ${BRIDGE_STATIC_IP}"
            echo "    GW:  ${BRIDGE_STATIC_GW}"
            echo "    DNS: ${BRIDGE_STATIC_DNS}"

            if is_iface_dhcp "${IFACE}"; then
                echo "  + DHCP detectado: la concesión se fija como estática (añade la reserva en el router)"
            fi
        else
            echo "  > using env vars: IP=${BRIDGE_STATIC_IP} GW=${BRIDGE_STATIC_GW} DNS=${BRIDGE_STATIC_DNS}"
            if is_iface_dhcp "${IFACE}"; then
                echo "  + DHCP detectado: la concesión se fija como estática (añade la reserva en el router)"
            fi
        fi
    else
        # --dhcp (opt-in): the bridge keeps requesting its own DHCP lease.
        echo "  > (--dhcp) el bridge pedirá su propia IP por DHCP; no se fija nada."
        detected_info="$(detect_static_for_iface "${IFACE}" 2>/dev/null || true)"
        if [ -n "${detected_info}" ]; then
            # Use a subshell to eval without polluting our env vars
            (
                eval "${detected_info}" 2>/dev/null
                echo "    current IP:  ${BRIDGE_STATIC_IP:-<none>}"
                echo "    current GW:  ${BRIDGE_STATIC_GW:-<none>}"
                echo "    current DNS: ${BRIDGE_STATIC_DNS:-<none>}"
            )
            echo "  + bridge will obtain its own DHCP lease — physical iface stays untouched"
        else
            echo "    (no IP detected — bridge will get one via DHCP)"
        fi
    fi
fi

# 5. Apply configuration based on mode
BR_NAME="${BRIDGE_NAME:-vmbr0}"

if [[ "${MODE}" == "bridge" || "${MODE}" == "both" ]]; then
    # WebKVM requires a PHYSICAL Linux bridge (vmbr0/br0) so KVM and Incus
    # share the real LAN (shared Layer-2, Proxmox-style). Reuse an existing
    # one or create it (nmcli → netplan); NEVER fall back to NAT/macvlan.
    if ! ensure_physical_bridge; then
        echo "  FATAL: no physical bridge available — WebKVM requires shared Layer-2." >&2
        echo "        See the instructions above; isolated NAT/macvlan are not used." >&2
        exit 1
    fi
    apply_bridge_sysctl
    allow_bridge_forward "${BR_NAME}"
    configure_incus_default_profile "${BR_NAME}"
    disable_libvirt_nat_default
    ensure_vmbr1_nat
    echo "[4/5] wiring KVM/Incus to physical bridge ${BR_NAME} (no libvirt virtual networks)"
    if [ -n "${IFACE:-}" ]; then
        verify_post_state_bridge "${BR_NAME}" "${IFACE}"
    fi
fi

if [[ "${MODE}" == "nat" || "${MODE}" == "both" ]]; then
    apply_bridge_sysctl
    if [ -n "${IFACE}" ]; then
        # Apply static IP to the physical interface only in:
        #   - direct-bridge mode (old behaviour: enslave & move IP)
        #   - NAT-only mode with an explicit static IP
        # In macvlan 'both' mode the static IP is for the BRIDGE, not phys.
        SHOULD_CONFIGURE_PHYSICAL=false
        if [ "$DIRECT_BRIDGE" = "true" ]; then
            SHOULD_CONFIGURE_PHYSICAL=true
        elif [ -n "${BRIDGE_STATIC_IP:-}" ] && [[ "${MODE}" == "nat" ]]; then
            SHOULD_CONFIGURE_PHYSICAL=true
        fi

        if [ "$SHOULD_CONFIGURE_PHYSICAL" = "true" ]; then
            echo "[5/5] writing static IP configuration on ${IFACE}"
            ensure_networkd_config_physical "${IFACE}"

            sudo ip addr flush dev "${IFACE}" scope global 2>/dev/null || true
            sudo ip addr add "${BRIDGE_STATIC_IP}" dev "${IFACE}" 2>/dev/null || true
            while ip route show default 2>/dev/null | grep -q .; do
                ip route del default 2>/dev/null || break
            done
            sudo ip route add default via "${BRIDGE_STATIC_GW}" dev "${IFACE}" 2>/dev/null || true

            verify_post_state_physical "${IFACE}"

            cat <<EOF

  ${bold:-}config written.${reset:-} To make it persistent across reboots, run:
    sudo systemctl restart systemd-networkd
    ip -br addr | grep ${IFACE}
    ping -c 1 ${BRIDGE_STATIC_GW}

  If the host doesn't come back, restore the backup:
    sudo cp /etc/systemd/network/${IFACE}.network.bak /etc/systemd/network/${IFACE}.network
    sudo systemctl restart systemd-networkd
EOF
        else
            echo "[5/5] NAT mode: leaving physical interface ${IFACE} untouched"
            echo "  + VMs route through libvirt 'default' NAT network (192.168.122.x)"
        fi
    fi
fi

echo ""
echo "=== network setup complete ==="
if [[ "${MODE}" == "nat" ]]; then
    phys_ip="$(ip -4 -o addr show dev "${IFACE:-}" scope global 2>/dev/null | awk '{print $4}' | head -1)"
    unset bridge_ip ip_type
    cat <<EOF
  Mode: NAT only
  - Interface ${IFACE:-<none>} IP: ${phys_ip:-<unchanged>}
  - VMs use libvirt 'default' NAT network (192.168.122.x)
EOF
elif [[ "${MODE}" == "bridge" ]]; then
    if [ "$DIRECT_BRIDGE" = "true" ]; then
        unset bridge_ip phys_ip ip_type
        cat <<EOF
  Mode: Bridge only (direct)
  - Linux bridge ${BR_NAME} created with slave ${IFACE}
  - ${BR_NAME} has static IP ${BRIDGE_STATIC_IP}
  - VMs/containers attach DIRECTLY to the physical bridge (visible on LAN, no libvirt networks)
EOF
    else
        bridge_ip="$(ip -4 -o addr show dev "${BR_NAME}" scope global 2>/dev/null | awk '{print $4}' | head -1)"
        phys_ip="$(ip -4 -o addr show dev "${IFACE:-}" scope global 2>/dev/null | awk '{print $4}' | head -1)"
        if [ -n "${bridge_ip}" ]; then ip_type=""; else ip_type=" (<acquiring>)"; fi
        cat <<EOF
  Mode: Bridge only (macvlan)
  - ${BR_NAME} IP: ${bridge_ip:-<acquiring>}${ip_type}
  - ${IFACE} IP untouched: ${phys_ip:-<none>}
  - macvlan mv-${BR_NAME} bridges ${IFACE} → ${BR_NAME}
  - VMs/containers attach DIRECTLY to the physical bridge (visible on LAN)
EOF
    fi
else
    # both
    if [ "$DIRECT_BRIDGE" = "true" ]; then
        unset bridge_ip phys_ip ip_type
        cat <<EOF
  Mode: Both (NAT + Bridge direct)
  - Host interface ${IFACE} has static IP ${BRIDGE_STATIC_IP} (for NAT)
  - Linux bridge ${BR_NAME} created with slave ${IFACE} (for Bridge)
  - ${BR_NAME} has static IP ${BRIDGE_STATIC_IP}
  - VMs/containers attach to real bridges: vmbr0 (LAN, DHCP del router) or vmbr1 (100.0.0.0/24, NAT)
EOF
    else
        bridge_ip="$(ip -4 -o addr show dev "${BR_NAME}" scope global 2>/dev/null | awk '{print $4}' | head -1)"
        phys_ip="$(ip -4 -o addr show dev "${IFACE:-}" scope global 2>/dev/null | awk '{print $4}' | head -1)"
        if [ -n "${bridge_ip}" ]; then ip_type=""; else ip_type=" (<acquiring>)"; fi
        cat <<EOF
  Mode: Both (NAT + Bridge macvlan)
  - ${IFACE} IP untouched: ${phys_ip:-<none>}
  - ${BR_NAME} IP: ${bridge_ip:-<acquiring>}${ip_type}
  - macvlan mv-${BR_NAME} bridges ${IFACE} → ${BR_NAME}
  - VMs/containers: vmbr0 (LAN) or vmbr1 (100.0.0.0/24, NAT)
EOF
    fi
fi