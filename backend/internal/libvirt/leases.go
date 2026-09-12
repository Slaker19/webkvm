package libvirt

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"webkvm/internal/models"
)

// leaseFilePath returns the per-bridge dnsmasq leasefile path
// configureBridgeDHCP writes (see its dhcp-leasefile= directive). Every
// bridge gets its own file — without this, multiple per-bridge dnsmasq
// instances would all fall back to dnsmasq's compiled-in default path
// and stomp on each other's lease records.
func leaseFilePath(br string) string {
	return "/etc/webkvm/" + br + "-dnsmasq.leases"
}

// readLeases parses a dnsmasq leasefile: one lease per line,
// "<expiry-epoch> <mac> <ip> <hostname-or-*> <client-id-or-*>". A
// missing file (DHCP never enabled on this bridge, or no lease issued
// yet) is not an error — it returns an empty, non-nil slice.
func readLeases(br string) ([]models.DHCPLease, error) {
	f, err := os.Open(leaseFilePath(br))
	if os.IsNotExist(err) {
		return []models.DHCPLease{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	leases := []models.DHCPLease{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue // malformed/short line — skip, don't fail the whole read
		}
		epoch, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		mac, ip, host := fields[1], fields[2], fields[3]
		if host == "*" {
			host = ""
		}
		leases = append(leases, models.DHCPLease{
			MAC:      mac,
			IP:       ip,
			Hostname: host,
			Expiry:   time.Unix(epoch, 0),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return leases, nil
}

// NetworkLeases returns the active DHCP leases for a bridge. Returns an
// empty slice, never an error, for a "direct" network, one with DHCP
// off, or one with no leasefile yet — those all look the same from
// here (no dnsmasq unit for this bridge).
func (c *Connector) NetworkLeases(name string) ([]models.DHCPLease, error) {
	if !isLinuxBridge(name) {
		return nil, fmt.Errorf("bridge %q not found", name)
	}
	if !dnsmasqUnitExists(name) {
		return []models.DHCPLease{}, nil
	}
	return readLeases(name)
}

// ErrLeaseNotFound is returned by ReleaseNetworkLease when the given
// (ip, mac) pair isn't in the bridge's current lease table. dhcp_release
// itself can't tell us this — it just fires a DHCPRELEASE packet and
// exits 0 whether or not that address was ever leased — so this package
// checks the leasefile first rather than reporting a silent no-op as a
// success.
var ErrLeaseNotFound = errors.New("lease not found")

// ReleaseNetworkLease asks dnsmasq to release one active lease via the
// dhcp_release companion binary (a real DHCPRELEASE) so the address is
// immediately free for reassignment. dnsmasq's in-memory lease table is
// authoritative — editing the leasefile directly and signaling dnsmasq
// is not a correctness-equivalent fallback (a periodic leasefile flush
// can overwrite the edit), so this deliberately does not attempt one:
// it returns a clear, actionable error when dhcp_release isn't
// installed instead.
func (c *Connector) ReleaseNetworkLease(br, ip, mac string) error {
	if !isLinuxBridge(br) {
		return fmt.Errorf("bridge %q not found", br)
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP %q", ip)
	}
	if _, err := net.ParseMAC(mac); err != nil {
		return fmt.Errorf("invalid MAC %q", mac)
	}
	leases, err := readLeases(br)
	if err != nil {
		return fmt.Errorf("reading leases for %q: %w", br, err)
	}
	found := false
	for _, l := range leases {
		if l.IP == ip && strings.EqualFold(l.MAC, mac) {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("no active lease %s (%s) on %q: %w", ip, mac, br, ErrLeaseNotFound)
	}
	path, err := exec.LookPath("dhcp_release")
	if err != nil {
		return fmt.Errorf("dhcp_release not installed — cannot release lease %s (%s) on %q; install your distro's dnsmasq companion tools package (e.g. dnsmasq-utils)", ip, mac, br)
	}
	out, err := exec.Command(path, br, ip, mac).CombinedOutput()
	if err != nil {
		return fmt.Errorf("dhcp_release %s %s %s: %v (%s)", br, ip, mac, err, strings.TrimSpace(string(out)))
	}
	return nil
}
