#!/usr/bin/env bash
# Wire up networking on a fresh webkvm install.
#
# Modes:
#   --nat          : create libvirt default NAT network only.
#                    If BRIDGE_STATIC_IP is set, writes it to the physical
#                    interface; otherwise leaves the host's network untouched.
#   --bridge       : create a macvlan virtual slave on the physical interface,
#                    attach it to a Linux bridge br0, and create libvirt bridge
#                    network br0-bridge. VMs on br0-bridge are visible on the
#                    LAN. The physical interface is NEVER touched — its IP stays
#                    intact. Works on both WiFi and ethernet.
#   --both         : do both NAT and Bridge (DEFAULT when no flags given)
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
MODE="${NET_MODE:-both}"
DIRECT_BRIDGE=false
BRIDGE_DHCP=true   # default: bridge gets IP via DHCP
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
is_iface_dhcp() {
    local iface="$1"
    ip route 2>/dev/null | grep -qE "^default .* dev ${iface} .* proto dhcp"
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
        echo "  - libvirt default network is not defined (the backend will create it on startup)"
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

# --- libvirt bridge network (br0-bridge) ------------------------------------
ensure_bridge_network() {
    local br_name="$1"
    local net_name="br0-bridge"
    if sudo virsh net-list --all --name 2>/dev/null | grep -qx "${net_name}"; then
        local current
        current="$(sudo virsh net-dumpxml "${net_name}" 2>/dev/null \
            | grep -oE "bridge name='[^']+'" | head -1 | sed -E "s/.*'([^']+)'.*/\1/")"
        if [ "${current}" = "${br_name}" ]; then
            echo "  = libvirt network '${net_name}' already wired to ${br_name}"
            sudo virsh net-list --name 2>/dev/null | grep -qx "${net_name}" \
                || sudo virsh net-start "${net_name}" >/dev/null 2>&1 || true
            sudo virsh net-list --autostart --name 2>/dev/null | grep -qx "${net_name}" \
                || sudo virsh net-autostart "${net_name}" >/dev/null 2>&1 || true
            return 0
        fi
        echo "  ! libvirt network '${net_name}' is wired to '${current}', not ${br_name}; leaving it alone"
        echo "    (re-point it manually with: virsh net-destroy ${net_name} && virsh net-edit ${net_name})"
        return 0
    fi
    local xml
    xml=$(cat <<EOF
<network>
  <name>${net_name}</name>
  <forward mode='bridge'/>
  <bridge name='${br_name}'/>
</network>
EOF
)
    echo "${xml}" | sudo virsh net-define /dev/stdin >/dev/null
    sudo virsh net-autostart "${net_name}" >/dev/null
    sudo virsh net-start "${net_name}" >/dev/null
    echo "  + libvirt network '${net_name}' created on bridge ${br_name}"
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

# 4. Resolve static IP/GW/DNS (env vars > auto-detect > [conditional error])
# In macvlan bridge mode with DHCP, static IP is optional (bridge gets its own
# DHCP lease). In all other modes (direct-bridge, static bridge, NAT+static),
# we need the static IP.
NEEDS_STATIC_IP=false
if [ "$DIRECT_BRIDGE" = "true" ]; then
    NEEDS_STATIC_IP=true
elif [[ "${MODE}" == "bridge" || "${MODE}" == "both" ]] && [ "$BRIDGE_DHCP" = "false" ]; then
    NEEDS_STATIC_IP=true
elif [[ "${MODE}" == "nat" || "${MODE}" == "both" ]] && [ -n "${BRIDGE_STATIC_IP:-}" ]; then
    NEEDS_STATIC_IP=true
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
                show_dhcp_to_static_warning "${IFACE}" "${BRIDGE_STATIC_IP}" "${BRIDGE_STATIC_GW}" "${BRIDGE_STATIC_DNS}"
            fi
        else
            echo "  > using env vars: IP=${BRIDGE_STATIC_IP} GW=${BRIDGE_STATIC_GW} DNS=${BRIDGE_STATIC_DNS}"
            if is_iface_dhcp "${IFACE}"; then
                show_dhcp_to_static_warning "${IFACE}" "${BRIDGE_STATIC_IP}" "${BRIDGE_STATIC_GW}" "${BRIDGE_STATIC_DNS}"
            fi
        fi
    else
        # DHCP bridge mode — auto-detect is informational only
        echo "  > (informational) auto-detecting current IP/gateway/DNS from ${IFACE}..."
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
BR_NAME="br0"

if [[ "${MODE}" == "bridge" || "${MODE}" == "both" ]]; then
    if [ -n "${IFACE}" ]; then
        if [ "$DIRECT_BRIDGE" = "true" ]; then
            # === DIRECT bridge mode (old behaviour: enslave physical iface) ===
            if [ -n "${EXISTING_BRIDGE:-}" ]; then
                echo "[4/5] reusing existing Linux bridge ${BR_NAME} + libvirt network br0-bridge"
                ensure_bridge_network "${BR_NAME}"
                verify_post_state_bridge "${BR_NAME}" "${IFACE}"
            else
                echo "[4/5] configuring Linux bridge ${BR_NAME} + libvirt network br0-bridge (direct)"
                ensure_networkd_config_bridge "${BR_NAME}" "${IFACE}"
                ensure_linux_bridge "${BR_NAME}" "${IFACE}"
                ensure_bridge_network "${BR_NAME}"
                verify_post_state_bridge "${BR_NAME}" "${IFACE}"
            fi

            cat <<EOF

  ${bold:-}configs written.${reset:-} Before restarting systemd-networkd, verify:
    - /etc/systemd/network/${BR_NAME}.network has Address=${BRIDGE_STATIC_IP}
    - /etc/systemd/network/${IFACE}.network has Bridge=${BR_NAME} and DHCP=no
    - you have physical/console access (restart WILL drop IP briefly)

  When ready, from console:
    sudo systemctl restart systemd-networkd
    ip -br addr | grep ${BR_NAME}
    ping -c 1 ${BRIDGE_STATIC_GW}

  If the host doesn't come back, restore the backup:
    sudo cp /etc/systemd/network/${BR_NAME}.network.bak /etc/systemd/network/${BR_NAME}.network
    sudo cp /etc/systemd/network/${IFACE}.network.bak /etc/systemd/network/${IFACE}.network
    sudo systemctl restart systemd-networkd
EOF
        else
            # === MACVLAN bridge mode (default) ===
            echo "[4/5] configuring macvlan bridge ${BR_NAME} + libvirt network br0-bridge"
            ensure_networkd_config_macvlan "${BR_NAME}" "${IFACE}"
            ensure_sysctl_arp_flux
            ensure_macvlan_bridge "${BR_NAME}" "${IFACE}"
            ensure_bridge_network "${BR_NAME}"
            verify_post_state_macvlan "${BR_NAME}" "${IFACE}"

            # Get the actual bridge IP for the summary
            actual_bridge_ip="$(ip -4 -o addr show dev "${BR_NAME}" scope global 2>/dev/null | awk '{print $4}' | head -1)"
            if [ -n "${actual_bridge_ip}" ]; then
                ip_info="${actual_bridge_ip}"
            elif [ "$BRIDGE_DHCP" = "false" ] && [ -n "${BRIDGE_STATIC_IP:-}" ]; then
                ip_info="${BRIDGE_STATIC_IP} (pending)"
            else
                ip_info="acquiring via DHCP"
            fi

            cat <<EOF

  ${bold:-}configs written.${reset:-} To apply systemd-networkd configs persistently
  (they will survive reboot), restart systemd-networkd:

    sudo systemctl restart systemd-networkd

  The bridge is already running with IP (${ip_info}). If the physical
  interface gets a new DHCP lease, the macvlan+bridge will be recreated
  automatically by systemd-networkd.

  Current state:
    ip -br addr | grep -E '(${BR_NAME}|${IFACE}|mv-${BR_NAME})'
EOF
        fi
    fi
fi

if [[ "${MODE}" == "nat" || "${MODE}" == "both" ]]; then
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
  - VMs use libvirt 'br0-bridge' network (visible on LAN)
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
  - VMs use libvirt 'br0-bridge' (visible on LAN)
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
  - VMs can use 'default' (NAT, 192.168.122.x) OR 'br0-bridge' (LAN)
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
  - VMs: 'default' (NAT, 192.168.122.x) OR 'br0-bridge' (LAN)
EOF
    fi
fi