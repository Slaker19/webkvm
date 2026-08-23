#!/usr/bin/env bash
# Wire up libvirt networks on a fresh webkvm install: the default
# NAT network (so VMs can reach the Internet through the host) and a
# bridge-mode network on the first non-loopback, non-virtual host
# interface (so VMs can be reachable on the LAN).
#
# Idempotent: safe to re-run on an existing install. Skips any piece
# that's already in place and reports what it did.
#
# Skipped automatically when:
#   - we're inside a container (no real NICs to bridge)
#   - no candidate interface exists (e.g. all-NIC box that's already
#     fully virtualized)
#   - the bridge network is already wired to a different interface
#     (in which case we leave it alone — the operator's choice wins)
#
# Side effect: stops and disables dhcpcd and NetworkManager if they
# are running, because they would race with systemd-networkd over
# the slave interface and re-introduce dual-homing on reboot.
#
# Static IP: the bridge is configured with a STATIC address (not DHCP).
# If env vars BRIDGE_STATIC_IP / BRIDGE_STATIC_GW / BRIDGE_STATIC_DNS are
# set, those win. If not, the script auto-detects from the running
# config (ip / resolvectl). If detection fails (no IP, no gateway), the
# script FAILS with a clear error pointing the operator at the env
# vars — it does NOT prompt, because setup-bridge.sh is meant to be
# scriptable / re-runnable. The interactive prompt lives in setup.sh
# (the onboarding wrapper) only.
#
# WEBKVM_DETECT_ONLY=1 makes the script exit after detecting and
# printing the would-be values, without touching the system. Used by
# setup.sh to auto-fill the prompt and by CI for dry-runs.

set -euo pipefail

# --- minimal pre-detection helpers (must be defined BEFORE the
# WEBKVM_DETECT_ONLY check, which is the first executable after
# `set -euo pipefail`) -----------------------------------------------
# These forward-declarations exist solely so the detect-only path
# below can call them. The full versions of these functions live
# later in the file (where they're used by the main flow); the second
# definition wins in bash, so behavior is identical.

pick_bridge_iface() {
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

# See the full version later in the file for the rationale behind the
# resolvectl-first / grep-fallback DNS detection.
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

# Return the slave of an existing br0 (or the first of br0/br1/br2
# we find). The detect-only path needs this because pick_bridge_iface
# only returns FREE interfaces (it skips anything that's already a
# bridge port), but on a host with an existing br0 we want to detect
# the IP on the EXISTING slave, not on a free NIC.
existing_bridge_slave() {
    for br in br0 br1 br2; do
        if [ -d "/sys/class/net/${br}/bridge" ]; then
            local slave
            slave=$(ls "/sys/class/net/${br}/brif/" 2>/dev/null | head -1 || true)
            [ -n "${slave}" ] && { echo "${slave}"; return 0; }
        fi
    done
    return 1
}

# --- 0. WEBKVM_DETECT_ONLY fast path (MUST be the first executable) ------
# Without this being first, "detect without touching the system" is a
# lie: disable_conflicting_dhcp_clients (which stops services) runs
# later in main, before we'd get a chance to check the flag.
if [ "${WEBKVM_DETECT_ONLY:-0}" = "1" ]; then
    # When the bridge already exists, the IP/GW/DNS are on the BRIDGE
    # (the slave is configured DHCP=no by design). When the bridge
    # doesn't exist yet (first install), detect on the candidate slave
    # where the IP currently lives.
    if [ -d "/sys/class/net/br0/bridge" ]; then
        detect_iface="br0"
    else
        detect_iface="$(existing_bridge_slave 2>/dev/null || pick_bridge_iface 2>/dev/null || true)"
    fi
    if [ -n "${detect_iface}" ] && detect_static_for_iface "${detect_iface}" >/dev/null 2>&1; then
        detect_static_for_iface "${detect_iface}"
    fi
    exit 0
fi

# --- container detection -------------------------------------------------
in_container() {
    [ -f /run/.containerenv ] && return 0
    grep -qE 'docker|lxc|containerd' /proc/1/cgroup 2>/dev/null && return 0
    return 1
}
if in_container; then
    echo "=== webkvm bridge setup: skipped (running in a container)"
    exit 0
fi

# --- managed-file helper -----------------------------------------------
# A "managed" file is one we wrote. Re-runs of the script can update
# files we own, but never clobber an operator-customized config.
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
        echo "# DO NOT EDIT: re-run scripts/setup-bridge.sh to regenerate."
        echo
        echo "$content"
    } > "$tmp"
    sudo install -m 0644 "$tmp" "$path"
    rm -f "$tmp"
    echo "  + ${path}: written"
}

# Save a copy of a file we own (or skip if we don't). Called BEFORE
# write_managed_file so the operator can roll back from a console if
# the new config drops the IP. The backup goes to <path>.bak.
backup_managed_file() {
    local path="$1"
    [ -f "$path" ] || return 0
    grep -qF "$MANAGED_MARKER" "$path" || return 0
    sudo cp "${path}" "${path}.bak" 2>/dev/null || true
    echo "  ! ${path}.bak: backup created (use it to roll back from console)"
}

# --- pick the first physical interface ---------------------------------
pick_bridge_iface() {
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

# --- detect_static_for_iface: read the host's current IP/gateway/DNS ---
# Prints "BRIDGE_STATIC_IP=... BRIDGE_STATIC_GW=... BRIDGE_STATIC_DNS=..."
# in a format that's safe to `eval` in the calling shell. Exits non-zero
# if IP or gateway can't be determined (DNS can fall back to a default).
#
# IP:    first global IPv4 on the iface.
# GW:    first default route (regardless of which iface it points at;
#        if you're on a single-homed host that's your gateway; if
#        you're multi-homed and the default goes somewhere else,
#        override with BRIDGE_STATIC_GW).
# DNS:   prefer resolvectl dns <iface> (the per-interface DNS actually
#        learned via DHCP, not the 127.0.0.53 stub in /etc/resolv.conf
#        on hosts with systemd-resolved). Fall back to grepping
#        /etc/resolv.conf but FILTERING OUT 127.0.0.53, because
#        that's the local stub resolver — not a real upstream DNS.
detect_static_for_iface() {
    local iface="$1"
    local ip gw dns

    ip=$(ip -4 -o addr show dev "${iface}" scope global 2>/dev/null | awk '{print $4}' | head -1)
    gw=$(ip route 2>/dev/null | awk '/^default/ {print $3; exit}')

    # DNS: resolvectl preferred (handles systemd-resolved correctly).
    dns=$(resolvectl dns "${iface}" 2>/dev/null \
        | sed -E 's/^[^:]*:[[:space:]]*//' \
        | tr -s '[:space:]' ',' | sed 's/,$//')

    # Fallback: /etc/resolv.conf, but skip 127.0.0.53 (systemd-resolved
    # stub resolver — NOT a real upstream DNS).
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

# --- is_iface_dhcp: is this iface currently in DHCP mode? -------------
# Based on the ACTUAL current state of the iface (no marker file).
# If the iface is in DHCP, the next step is a destructive operation
# (writing static config and restarting networkd), and the operator
# needs to know about the DHCP reservation on the router.
is_iface_dhcp() {
    local iface="$1"
    # `ip route` shows `proto dhcp` for the default route if it was
    # learned via DHCP. This is the most reliable indicator that the
    # iface (or rather, the routing stack that goes through it) is on
    # DHCP right now.
    ip route 2>/dev/null | grep -qE "^default .* dev ${iface} .* proto dhcp"
}

# --- show_dhcp_to_static_warning: pre-conversion nag about the router --
# TTY-aware: blocks with a read prompt if stdin is a TTY, just logs
# to stderr if not (so curl-piped installers and `ssh host command`
# don't hang). Show this ONLY when the script is about to convert a
# DHCP lease to a static address — that's the one operation where the
# router-side reservation matters.
show_dhcp_to_static_warning() {
    local iface="$1" ip="$2" gw="$3" dns="$4"
    local mac
    mac=$(cat "/sys/class/net/${iface}/address" 2>/dev/null || echo "?")

    cat >&2 <<EOF
================================================================
⚠️  Converting DHCP IP to static on ${iface}
================================================================
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
================================================================
EOF

    if [ -t 0 ]; then
        # Interactive: let the operator abort if they realize they
        # don't have a reservation in place.
        local _
        read -r -p "Press Enter to continue, Ctrl-C to abort. " _ </dev/tty || true
    fi
    # If not a TTY (CI, automation, `ssh host command`), we just
    # log to stderr and continue. The operator is expected to have
    # reviewed the audit log.
}

# --- conflicting DHCP clients ------------------------------------------
# systemd-networkd assigns the address to the bridge and the slave
# must NOT have an IP of its own. dhcpcd and NetworkManager would
# both happily DHCP-request on ens18 anyway, giving it a stale IP
# and breaking the bridge (asymmetric routing, lost SSH, etc.).
disable_conflicting_dhcp_clients() {
    local changed=0
    if systemctl is-active --quiet dhcpcd 2>/dev/null; then
        sudo systemctl disable --now dhcpcd >/dev/null 2>&1 || true
        echo "  - dhcpcd: stopped and disabled"
        changed=1
    fi
    if systemctl is-active --quiet NetworkManager 2>/dev/null; then
        sudo systemctl disable --now NetworkManager >/dev/null 2>&1 || true
        echo "  - NetworkManager: stopped and disabled"
        changed=1
    fi
    if [ $changed -eq 0 ]; then
        echo "  = no conflicting DHCP clients running"
    fi
    # Belt-and-suspenders: if dhcpcd ever gets re-enabled (e.g. by
    # a package upgrade), make sure its config still denies ens18.
    if [ -f /etc/dhcpcd.conf ] && command -v dhcpcd >/dev/null; then
        if ! sudo grep -q '^denyinterfaces ens18' /etc/dhcpcd.conf 2>/dev/null; then
            echo "denyinterfaces ens18" | sudo tee -a /etc/dhcpcd.conf >/dev/null
            echo "  + /etc/dhcpcd.conf: added 'denyinterfaces ens18'"
        fi
    fi
}

# --- libvirt default NAT network ---------------------------------------
ensure_default_network() {
    if ! sudo virsh net-list --all --name 2>/dev/null | grep -qx default; then
        echo "  - libvirt default network is not defined (libvirt will seed it on first use)"
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

# --- bridge-mode libvirt network ---------------------------------------
ensure_bridge_network() {
    local net_name="$1" bridge_name="$2"
    if sudo virsh net-list --all --name 2>/dev/null | grep -qx "${net_name}"; then
        local current
        current="$(sudo virsh net-dumpxml "${net_name}" 2>/dev/null \
            | grep -oE "bridge name='[^']+'" | head -1 | sed -E "s/.*'([^']+)'.*/\1/")"
        if [ "${current}" = "${bridge_name}" ]; then
            echo "  = libvirt network '${net_name}' already wired to ${bridge_name}"
            sudo virsh net-list --name 2>/dev/null | grep -qx "${net_name}" \
                || sudo virsh net-start "${net_name}" >/dev/null 2>&1 || true
            sudo virsh net-list --autostart --name 2>/dev/null | grep -qx "${net_name}" \
                || sudo virsh net-autostart "${net_name}" >/dev/null 2>&1 || true
            return 0
        fi
        echo "  ! libvirt network '${net_name}' is wired to '${current}', not ${bridge_name}; leaving it alone"
        echo "    (re-point it manually with: virsh net-destroy ${net_name} && virsh net-edit ${net_name})"
        return 0
    fi
    local xml
    xml=$(cat <<EOF
<network>
  <name>${net_name}</name>
  <forward mode='bridge'/>
  <bridge name='${bridge_name}'/>
</network>
EOF
)
    echo "${xml}" | sudo virsh net-define /dev/stdin >/dev/null
    sudo virsh net-autostart "${net_name}" >/dev/null
    sudo virsh net-start "${net_name}" >/dev/null
    echo "  + libvirt network '${net_name}' created on bridge ${bridge_name}"
}

# --- systemd-networkd configs (the persistence piece) -----------------
ensure_networkd_configs() {
    local br_name="$1" slave_iface="$2"

    # Take a backup of the current configs (if we own them via the
    # marker) before overwriting. The operator can roll back from a
    # console session if the new config breaks the network.
    backup_managed_file "/etc/systemd/network/${br_name}.netdev"
    backup_managed_file "/etc/systemd/network/${br_name}.network"
    backup_managed_file "/etc/systemd/network/${slave_iface}.network"

    write_managed_file "/etc/systemd/network/${br_name}.netdev" \
"[NetDev]
Name=${br_name}
Kind=bridge
"

    # br0.network: STATIC. The Address=/Gateway=/DNS= block below is
    # what the operator is committing to. The webUI's "New bridge"
    # form (which defaults to move_ip=true) will, for any future
    # Linux bridge the operator creates, move a slave's IPv4 onto
    # the new bridge — and that IPv4 should be stable across reboots,
    # which is exactly what static config gives you.
    #
    # Format: each DNS server on its own DNS= line (systemd-networkd
    # syntax, not comma-separated). Build it from BRIDGE_STATIC_DNS.
    local dns_block=""
    local IFS=','
    for d in ${BRIDGE_STATIC_DNS}; do
        d="$(echo "${d}" | xargs)"  # trim whitespace
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

    # The slave MUST say DHCP=no. Without this, kernel DHCP or
    # networkd's own DHCPv4 client can sneak an address onto the
    # slave, producing the dual-homing we keep seeing on reboot.
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

# --- runtime bridge creation ------------------------------------------
# Used both for the initial bring-up and as a no-op (idempotent) when
# the bridge already exists from a prior run.
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

    # Move IPs from slave → bridge BEFORE attaching the slave, so
    # the host stays reachable on the LAN throughout the move.
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

    # Apply the static IP/gateway/DNS right now too, so the host
    # is reachable at the configured address even before networkd
    # picks up the config we just wrote.
    if [ -n "${BRIDGE_STATIC_IP:-}" ]; then
        # Remove any address we might have just moved, in case
        # move_addr left a duplicate; then add the canonical one.
        sudo ip addr flush dev "${br_name}" scope global 2>/dev/null || true
        sudo ip addr add "${BRIDGE_STATIC_IP}" dev "${br_name}" 2>/dev/null || true
        # Replace any existing default route with one via the
        # configured gateway on the bridge.
        while ip route show default 2>/dev/null | grep -q .; do
            ip route del default 2>/dev/null || break
        done
        sudo ip route add default via "${BRIDGE_STATIC_GW}" dev "${br_name}" 2>/dev/null || true
    fi
}

# --- post-state verification ------------------------------------------
verify_post_state() {
    local br_name="$1" slave_iface="$2"
    echo "[verify] post-state checks"

    # 1. The bridge has an IPv4 (and ideally the configured static one).
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

    # 2. The slave has NO IP of its own (would cause dual-homing).
    local slave_ipv4
    slave_ipv4="$(ip -4 -o addr show dev "${slave_iface}" scope global 2>/dev/null | awk '{print $4}')"
    if [ -n "${slave_ipv4}" ]; then
        echo "  ! WARNING: ${slave_iface} has an IPv4 (${slave_ipv4}); removing it"
        sudo ip addr del "${slave_ipv4}" dev "${slave_iface}" 2>/dev/null || true
    else
        echo "  = ${slave_iface} has no global IPv4 (correct)"
    fi

    # 3. Default route via the bridge, not the slave.
    local def_route
    def_route="$(ip route show default 2>/dev/null | head -1)"
    if echo "${def_route}" | grep -q "dev ${br_name}"; then
        echo "  = default route via ${br_name} (correct)"
    elif echo "${def_route}" | grep -q "dev ${slave_iface}"; then
        echo "  ! WARNING: default route is via ${slave_iface}, not ${br_name}"
    fi
}

# --- main --------------------------------------------------------------
echo "=== webkvm bridge setup ==="

# Pre-flight: must be a real host with systemd + libvirtd.
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

# 2. libvirt default NAT network
echo "[2/5] libvirt default NAT network"
ensure_default_network

# 3. Pick the candidate interface
echo "[3/5] selecting host interface for bridged network"
BR_NAME="br0"
IFACE=""
if [ -n "${BRIDGE_SLAVE:-}" ]; then
    # Operator override: pin the slave to a specific iface (useful on
    # multi-NIC hosts where alphabetical order isn't the right pick).
    if [ -d "/sys/class/net/${BRIDGE_SLAVE}" ]; then
        IFACE="${BRIDGE_SLAVE}"
        echo "  + selected ${IFACE} (BRIDGE_SLAVE override)"
    else
        echo "  ! BRIDGE_SLAVE=${BRIDGE_SLAVE} but that iface doesn't exist" >&2
        exit 1
    fi
elif [ -d "/sys/class/net/${BR_NAME}/bridge" ]; then
    EXISTING_SLAVE="$(ls /sys/class/net/${BR_NAME}/brif/ 2>/dev/null | head -1 || true)"
    echo "  = ${BR_NAME} already exists${EXISTING_SLAVE:+ (slave: ${EXISTING_SLAVE})}"
    IFACE="${EXISTING_SLAVE}"
else
    IFACE="$(pick_bridge_iface || true)"
    if [ -z "${IFACE}" ]; then
        echo "  ! no candidate physical interface found"
    else
        echo "  + selected ${IFACE}"
    fi
fi

# 4. Resolve the static IP/GW/DNS (env vars > auto-detect > FAIL).
# This must happen BEFORE we write the networkd configs, but AFTER
# we know which interface to use.
if [ -n "${IFACE}" ]; then
    if [ -z "${BRIDGE_STATIC_IP:-}" ] || [ -z "${BRIDGE_STATIC_GW:-}" ] || [ -z "${BRIDGE_STATIC_DNS:-}" ]; then
        # Detect on the bridge, not the slave — the slave is
        # configured DHCP=no and intentionally has no IP. The
        # bridge is what actually holds the address.
        DETECT_IFACE="br0"
        [ -d "/sys/class/net/br0/bridge" ] || DETECT_IFACE="${IFACE}"
        echo "  > auto-detecting IP/gateway/DNS from ${DETECT_IFACE}..."
        DETECTED="$(detect_static_for_iface "${DETECT_IFACE}" || true)"
        if [ -z "${DETECTED}" ]; then
            echo "ERROR: couldn't auto-detect static IP on ${DETECT_IFACE}." >&2
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

        # If we auto-detected from a DHCP lease, the operator MUST
        # know — see comment in show_dhcp_to_static_warning. Show
        # the warning based on the ACTUAL current state (no marker
        # file, no "have I shown this before" — the state IS the
        # signal).
        if is_iface_dhcp "${DETECT_IFACE}"; then
            show_dhcp_to_static_warning "${DETECT_IFACE}" "${BRIDGE_STATIC_IP}" "${BRIDGE_STATIC_GW}" "${BRIDGE_STATIC_DNS}"
        fi
    else
        echo "  > using env vars: IP=${BRIDGE_STATIC_IP} GW=${BRIDGE_STATIC_GW} DNS=${BRIDGE_STATIC_DNS}"
        # Env var override path: also warn if the iface is on DHCP,
        # because the operator explicitly chose to make it static.
        DETECT_IFACE="br0"
        [ -d "/sys/class/net/br0/bridge" ] || DETECT_IFACE="${IFACE}"
        if is_iface_dhcp "${DETECT_IFACE}"; then
            show_dhcp_to_static_warning "${DETECT_IFACE}" "${BRIDGE_STATIC_IP}" "${BRIDGE_STATIC_GW}" "${BRIDGE_STATIC_DNS}"
        fi
    fi
fi

# 5. Create the bridge + libvirt network + persist via systemd-networkd.
# The systemd-networkd restart is INTENTIONALLY NOT done here. systemd-
# networkd's "restart" tears down every interface it manages and re-
# applies the configs; if the new config is wrong (e.g. netplan still
# has DHCP=yes on ens18 and overrode our Bridge=br0), the restart
# can drop the host's IP and lock out SSH. The script writes the
# configs and then prints the exact command the operator should run
# WHEN READY (typically from a console session, not over SSH).
if [ -n "${IFACE}" ]; then
    echo "[4/5] Linux bridge + libvirt network br0-bridge"
    ensure_networkd_configs "${BR_NAME}" "${IFACE}"
    ensure_linux_bridge "${BR_NAME}" "${IFACE}"
    ensure_bridge_network "br0-bridge" "${BR_NAME}"

    # 6. Verify current state
    echo "[5/5] verifying (pre-restart)"
    verify_post_state "${BR_NAME}" "${IFACE}"

    # Print the next step, but don't do it. The operator should
    # run this from a console session (or be ready to roll back)
    # because the restart WILL briefly drop the host's IP if the
    # configs aren't exactly right.
    cat <<EOF

  ${bold:-}configs written.${reset:-} Before restarting systemd-networkd, verify:
    - the new /etc/systemd/network/${BR_NAME}.network has
      Address=${BRIDGE_STATIC_IP} (NOT DHCP=ipv4)
    - /etc/systemd/network/${IFACE}.network has
      Bridge=${BR_NAME} and DHCP=no
    - on Ubuntu/Debian: netplan is disabled or its config doesn't
      conflict (netplan's "DHCP=yes" on ens18 will override our
      DHCP=no and bring the slave up with DHCP instead of as a
      bridge port — see scripts/disable-netplan.sh)
    - you have physical/console access (the restart WILL drop the
      IP briefly and SSH will hang)

  When ready, from console:
    sudo systemctl restart systemd-networkd
    # then verify the host is back at the new IP
    ip -br addr | grep ${BR_NAME}
    ping -c 1 ${BRIDGE_STATIC_GW}

  If the host doesn't come back, restore the backup:
    sudo cp /etc/systemd/network/${BR_NAME}.network.bak \\
            /etc/systemd/network/${BR_NAME}.network
    sudo cp /etc/systemd/network/${IFACE}.network.bak \\
            /etc/systemd/network/${IFACE}.network
    sudo systemctl restart systemd-networkd
EOF
fi

echo ""
echo "=== bridge setup complete ==="
if [ -n "${IFACE}" ]; then
    cat <<EOF
  The bridged network 'br0-bridge' is wired to ${BR_NAME} (slave: ${IFACE}).
  VMs attached to it get their IP from the LAN's DHCP server (typically
  the router) and are visible to other machines on the LAN.

  To attach a VM:
    - webUI: VM details → Interfaces → Add → network: br0-bridge
    - CLI:   virsh attach-interface <vm> network br0-bridge --model virtio --live
EOF
fi
