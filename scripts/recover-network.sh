#!/usr/bin/env bash
# webkvm network recovery — run from physical console / IPMI / KVM as root.
# Use this if the host lost its LAN IP after a failed setup-network.sh run.
# Also restores Linux bridge (br0) if bridge configs exist.

set -e
echo "=== webkvm network recovery ==="
echo "Recovering a host that lost its LAN IP."

echo
echo "--- 1. current state ---"
ip -br addr 2>&1 | grep -v lo || true
ip route 2>&1 || true
echo

echo "--- 2. interfaces ---"
ls /sys/class/net/ 2>&1 | grep -v lo || true
echo

# Check if bridge configs exist
HAS_BRIDGE_CONFIG=false
if [ -f "/etc/systemd/network/br0.netdev" ] || [ -f "/etc/systemd/network/br0.network" ]; then
    HAS_BRIDGE_CONFIG=true
fi

echo "--- 3. bring up all interfaces ---"
for iface in $(ls /sys/class/net/ 2>/dev/null | grep -v lo); do
    ip link set "${iface}" up 2>/dev/null || true
done
echo "  all interfaces brought UP"
echo

echo "--- 4. request DHCP on first physical interface ---"
# Find the first non-virtual interface to use for DHCP recovery
PHYS_IFACE=""
for path in /sys/class/net/*; do
    name="$(basename "${path}")"
    case "${name}" in
        lo|virbr*|vnet*|docker*|br-*|tun*|tap*|veth*) continue ;;
    esac
    [ -d "${path}/bridge" ] && continue
    [ -d "${path}/brport" ] && continue
    [ -d "${path}/device" ] || continue
    PHYS_IFACE="${name}"
    break
done

if [ -z "${PHYS_IFACE}" ]; then
    echo "  no physical interface found; trying ens18 as fallback"
    PHYS_IFACE="ens18"
fi

echo "  using ${PHYS_IFACE} for DHCP recovery"
if command -v dhclient >/dev/null; then
    echo "  using dhclient..."
    dhclient -v "${PHYS_IFACE}" || true
elif command -v dhcpcd >/dev/null; then
    echo "  using dhcpcd..."
    dhcpcd "${PHYS_IFACE}" || true
else
    echo "  no DHCP client found"
fi
echo

echo "--- 5. state after DHCP ---"
ip -br addr 2>&1 | grep -v lo
ip route 2>&1
echo

echo "--- 6. wait a moment, then check IP ---"
sleep 3
NEWIP=$(ip -4 -o addr show dev "${PHYS_IFACE}" scope global 2>/dev/null | awk '{print $4}' | head -1)
if [ -n "${NEWIP}" ]; then
    echo "  ${PHYS_IFACE} has ${NEWIP}"
else
    echo "  ${PHYS_IFACE} still has no IPv4. The router didn't give us a lease."
    echo "  Try: ip link set ${PHYS_IFACE} down && ip link set ${PHYS_IFACE} up"
    echo "  then: dhclient -v ${PHYS_IFACE}"
fi
echo

# Restore bridge if bridge configs exist
if [ "${HAS_BRIDGE_CONFIG}" = "true" ]; then
    echo "--- 7. restoring Linux bridge br0 (configs found) ---"
    if [ ! -d /sys/class/net/br0/bridge ]; then
        # Check whether it's macvlan (mv-br0 .netdev) or direct (phys iface enslaved)
        if [ -f "/etc/systemd/network/mv-br0.netdev" ]; then
            echo "  macvlan config detected — recreating macvlan + bridge"
            # Use the systemd oneshot service if it exists
            local mv_svc="webkvm-mv-br0@${PHYS_IFACE}.service"
            if systemctl cat "${mv_svc}" &>/dev/null; then
                systemctl start "${mv_svc}" 2>/dev/null || true
                echo "  started ${mv_svc}"
            else
                ip link add mv-br0 link "${PHYS_IFACE}" type macvlan mode bridge 2>/dev/null || true
                ip link add name br0 type bridge 2>/dev/null || true
                ip link set mv-br0 master br0 2>/dev/null || true
                ip link set mv-br0 up 2>/dev/null || true
                echo "  mv-br0 → br0 created (${PHYS_IFACE} is untouched)"
            fi
            ip link set br0 up 2>/dev/null || true
        else
            echo "  creating br0 with ${PHYS_IFACE} as slave..."
            ip link add name br0 type bridge
            ip link set "${PHYS_IFACE}" master br0
            ip link set br0 up
            echo "  br0 created"
        fi
    else
        echo "  br0 already exists"
    fi

    # Request DHCP on br0 if the bridge config uses DHCP
    if [ -f "/etc/systemd/network/br0.network" ] && grep -q 'DHCP=yes' /etc/systemd/network/br0.network 2>/dev/null; then
        echo "  br0 uses DHCP — requesting lease..."
        if command -v dhclient &>/dev/null; then
            dhclient -v br0 2>&1 | tail -1 || true
        elif command -v dhcpcd &>/dev/null; then
            dhcpcd br0 2>&1 | tail -1 || true
        fi
    fi
    echo
fi

echo "--- 8. show persistent configs (Managed by webkvm) ---"
for f in /etc/systemd/network/*.network /etc/systemd/network/*.netdev; do
    if [ -f "$f" ]; then
        echo "=== $f ==="
        cat "$f"
        echo
    fi
done

echo "--- 9. enable + restart systemd-networkd ---"
systemctl enable --now systemd-networkd 2>/dev/null || true
systemctl restart systemd-networkd 2>/dev/null || true
sleep 3

echo "--- 10. final state ---"
ip -br addr 2>&1 | grep -v lo
ip route 2>&1
echo "--- macvlan / bridge state ---"
ip -br link show type macvlan 2>/dev/null || echo "  (no macvlan interfaces)"
for br in /sys/class/net/*/bridge; do
    br_name="$(basename "$(dirname "$br")")"
    echo "  bridge ${br_name}: $(brctl show "${br_name}" 2>/dev/null | tail -n+2 || echo 'N/A')"
done 2>/dev/null || true

echo
echo "=== done. host should be reachable at the DHCP-assigned IP. ==="
if [ -n "${NEWIP}" ]; then
    echo "If you want a STATIC IP, re-run with:"
    echo "  sudo BRIDGE_STATIC_IP=$(echo ${NEWIP:-X.X.X.X})/24 \\"
    echo "       BRIDGE_STATIC_GW=192.168.1.1 \\"
    echo "       BRIDGE_STATIC_DNS=1.1.1.1,8.8.8.8 \\"
    echo "       /tmp/setup-network.sh"
    echo
    echo "AND add a DHCP reservation for ${PHYS_IFACE}'s MAC on the router"
    echo "(${PHYS_IFACE} MAC: $(cat /sys/class/net/${PHYS_IFACE}/address 2>/dev/null || echo 'unknown'))"
fi

# Show bridge-specific instructions if bridge was restored
if [ "${HAS_BRIDGE_CONFIG}" = "true" ]; then
    echo
    echo "=== Bridge (br0) restored ==="
    echo "VMs on 'br0-bridge' network should now be reachable on the LAN."
    echo "To re-configure bridge static IP, run setup-network.sh --bridge"
fi