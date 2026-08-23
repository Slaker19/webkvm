#!/bin/bash
# Disable netplan on Ubuntu 24.04+ / Debian 12+ systems.
#
# Netplan runs THREE different services on these distros:
#   1. netplan.service (top-level apply)
#   2. netplan-wait-online.service (waits for interfaces)
#   3. netplan-configure.service (the one that ACTUALLY GENERATES
#      /run/systemd/network/10-netplan-*.network on every boot,
#      BEFORE systemd-networkd runs — so the generated files
#      win the alphabetical race against webkvm's configs
#      and override them)
#
# Previous versions of this script masked only netplan.service and
# netplan-wait-online.service, which left netplan-configure.service
# free to regenerate the override on every reboot. THIS script
# masks all three, AND empties the netplan YAML so even if a
# future update re-enables one of the services, it has nothing
# to apply.
#
# The netplan YAML is the SOURCE of the generated configs; emptying
# it means netplan has nothing to write, even if it runs.

set -e
echo "=== Disabling netplan (lets webkvm's systemd-networkd configs win) ==="

if ! command -v netplan >/dev/null 2>&1; then
    echo "  netplan not installed, nothing to do"
    exit 0
fi

# 1. Mask ALL three netplan services. --now is implicit; mask also
#    disables on boot. We mask the generator one (netplan-configure)
#    first because it's the one that ACTUALLY generates the override
#    files on every boot.
echo "  masking netplan services..."
systemctl mask netplan-configure.service 2>&1 | grep -v "^$" || true
systemctl mask netplan.service 2>&1 | grep -v "^$" || true
systemctl mask netplan-wait-online.service 2>&1 | grep -v "^$" || true
systemctl stop netplan-configure.service 2>&1 | grep -v "^$" || true
echo "    netplan-configure.service: masked and stopped (this is the one that regenerates 10-netplan-*.network on boot)"
echo "    netplan.service:          masked and stopped"
echo "    netplan-wait-online.service: masked and stopped"

# 2. Empty the netplan YAML. The file's existence (matched by
#    /etc/netplan/*.yaml glob) is what causes netplan-configure to
#    act; the contents just say what to write. With an empty network
#    stanza, even a re-enabled service would generate nothing.
if [ -d /etc/netplan ]; then
    echo
    echo "  emptying netplan YAML configs..."
    for f in /etc/netplan/*.yaml; do
        [ -f "$f" ] || continue
        # Back up the original so the operator can restore it
        # if they ever need the default Ubuntu config back.
        cp "${f}" "${f}.bak-webkvm" 2>/dev/null || true
        # Replace with an empty renderer. 'renderer: networkd' is
        # the default; with no interfaces defined, nothing is
        # generated. We keep the file (just empty) so future
        # 'netplan try' or 'netplan apply' runs don't error out.
        cat > "${f}" <<EOF
# webkvm manages networking directly via systemd-networkd. The
# netplan config is intentionally empty so it doesn't override
# webkvm's /etc/systemd/network/*.network files.
network:
  version: 2
  renderer: networkd
EOF
        echo "    ${f}: emptied (backup at ${f}.bak-webkvm)"
    done
fi

# 3. Remove the currently-generated netplan files
removed=0
for f in /run/systemd/network/10-netplan-*.network /run/systemd/network/10-netplan-*.link; do
    if [ -f "$f" ]; then
        rm -f "$f"
        echo "    removed: $f"
        removed=$((removed+1))
    fi
done
[ "$removed" -eq 0 ] && echo "    (no netplan-generated files found)"

# 4. Restart systemd-networkd to apply the change. This WILL drop
#    the IP briefly (typically 2-5s while DHCP re-acquires, or
#    immediately if static config is in place). The operator should
#    run this from a console session, not over SSH, OR be ready
#    to reconnect to whatever the new IP ends up being.
echo
echo "  about to restart systemd-networkd — IP will drop for 2-5s"
echo "  (DHCP re-acquire, or instant if static config is in place)"
echo
systemctl restart systemd-networkd
sleep 3

echo
echo "=== state after restart ==="
ip -br addr | grep -v lo
echo
ip route | head -3
echo
echo "  expected: ens18 has no IP (bridge port), br0 has the IP"
echo "  (static if webkvm's configs are in place, else DHCP)"
