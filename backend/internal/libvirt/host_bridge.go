package libvirt

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// isLinuxBridge reports whether the given interface name exists on
// the host AND is itself a Linux bridge (i.e. /sys/class/net/<name>/bridge
// is a directory). It is used by CreateNetwork to validate that a
// forward=bridge network is actually wired to a bridge, not to a
// raw ethernet or wireless device.
func isLinuxBridge(name string) bool {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		return false
	}
	_, err := os.Stat("/sys/class/net/" + name + "/bridge") // lgtm[go/path-injection] - name validated above
	return err == nil
}

// virtualBridgePrefixes are the prefixes of bridges WebKVM treats as
// virtual/NAT plumbing rather than PHYSICAL shared bridges: libvirt NAT
// bridges (virbr0), container-daemon bridges (lxdbr0/lxcbr0) and Docker
// bridges (docker0/br-*). A physical bridge (vmbr0/br0) is required so
// KVM and Incus share the host's real LAN (Proxmox-style).
var virtualBridgePrefixes = []string{"virbr", "lxdbr", "lxcbr", "incusbr", "docker", "br-"}

// errNoPhysicalBridge is the fatal error when the host has no physical
// Linux bridge. Mirrors compute.ErrNoPhysicalBridge (libvirt cannot import
// compute without a cycle).
var errNoPhysicalBridge = errors.New("no physical bridge found on host; please configure a Linux bridge (vmbr0 or br0) attached to your physical NIC")

// isPhysicalBridge reports whether name is a Linux bridge AND is not one
// of the virtual/NAT bridges above — i.e. a real, shared L2 bridge a
// user wires to a physical NIC (vmbr0, br0, …).
func isPhysicalBridge(name string) bool {
	if !isLinuxBridge(name) {
		return false
	}
	for _, p := range virtualBridgePrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// IsManagedBridge reports whether the given Linux bridge name is the
// host's primary/protected bridge. The API refuses to delete it (and
// the UI greys out the delete button) so a stray click can't silently
// remove the bridge that holds the host's LAN IP — tearing it down
// without a replacement is the same as yanking the network cable.
//
// Two independent checks, either of which protects a bridge:
//  1. Its name matches one of the well-known primary-bridge names
//     scripts/setup-network.sh creates by default ("vmbr0") or the
//     older "br0" convention.
//  2. It currently carries the host's default route — a dynamic
//     safety net so a renamed/custom primary bridge is still protected
//     even if its name doesn't match #1 (this also catches the case
//     that motivated this check: setup-network.sh's real default is
//     "vmbr0", but this function used to only recognize "br0").
func IsManagedBridge(name string) bool {
	if name == "vmbr0" || name == "br0" {
		return true
	}
	return carriesDefaultRoute(name)
}

// carriesDefaultRoute reports whether the given interface is the one
// the host's default route goes through (i.e. removing it would cut
// the host off its own gateway).
func carriesDefaultRoute(name string) bool {
	out, err := exec.Command("ip", "-4", "route", "show", "default").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "dev" && i+1 < len(fields) && fields[i+1] == name {
				return true
			}
		}
	}
	return false
}

// defaultUplink returns the interface name the host's default route
// goes through (e.g. "eth0"), or "" if there is none. Used to scope a
// NAT bridge's masquerade rule to the real internet-facing interface.
func defaultUplink() string {
	out, err := exec.Command("ip", "-4", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// mainBridge returns the host's primary Linux bridge: "vmbr0" (Proxmox
// convention), then "br0", then the first Linux bridge found. Empty when
// the host has no Linux bridges. This is the bridge WebKVM wires default
// networks to so VMs and containers share the same physical L2 (Proxmox
// philosophy) instead of isolated NAT.
func mainBridge() string {
	for _, preferred := range []string{"vmbr0", "br0"} {
		if isLinuxBridge(preferred) {
			return preferred
		}
	}
	all := listLinuxBridges()
	if len(all) > 0 {
		return all[0]
	}
	return ""
}

// listLinuxBridges returns the names of every Linux bridge present
// on the host, sorted alphabetically. libvirt-managed default bridges
// (virbr*), Docker-managed bridges (docker0, br-*, br-webkvm, etc.)
// and L2-only plumbing (lxdbr*, br-*) are excluded so the list is a
// clean set of "real" bridges a user might want to wire VMs to. The
// result is intended for error messages, so a small list is fine;
// callers that need the full set (slaves, IPs, etc.) should use
// api.ListHostBridges instead.
func listLinuxBridges() []string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "virbr") {
			continue
		}
		// docker* — Docker's default bridge (docker0) and
		// user-defined Docker bridges (br-<name>).
		if strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") {
			continue
		}
		// lxdbr* / lxcbr* / incusbr* — LXD/Incus bridges.
		if strings.HasPrefix(name, "lxdbr") || strings.HasPrefix(name, "lxcbr") || strings.HasPrefix(name, "incusbr") {
			continue
		}
		if _, err := os.Stat("/sys/class/net/" + name + "/bridge"); err == nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// isPhysicalInterface reports whether name is a real physical (or
// wireless) network interface on the host: it has a backing device
// (/sys/class/net/<name>/device exists) and is not itself a Linux
// bridge. Used by CreateNetwork to validate a kind=="direct" network's
// target interface, and by DeleteNetwork to tell a deliberately-
// enslaved physical NIC (safe to release) apart from a VM/container's
// virtual tap (must block deletion).
func isPhysicalInterface(name string) bool {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		return false
	}
	base := "/sys/class/net/" + name // lgtm[go/path-injection] - name validated above
	if _, err := os.Stat(base + "/bridge"); err == nil {
		return false
	}
	_, err := os.Stat(base + "/device")
	return err == nil
}

// listPhysicalInterfaces returns the names of the host's physical/
// wireless NICs, sorted alphabetically, for use in error-message
// hints. Mirrors the filtering in api.ListHostInterfaces; callers
// that need richer per-interface detail (type, state, MAC, DHCP
// status) should use that endpoint instead.
func listPhysicalInterfaces() []string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range entries {
		name := e.Name()
		if name == "lo" || strings.HasPrefix(name, "vnet") || strings.HasPrefix(name, "virbr") {
			continue
		}
		if isPhysicalInterface(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// readBridgeSlaves lists the port names attached to a Linux bridge
// (the entries in /sys/class/net/<bridge>/brif/).
func readBridgeSlaves(name string) []string {
	entries, err := os.ReadDir("/sys/class/net/" + name + "/brif")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// firstIPv4 returns the first non-loopback, non-link-local IPv4 CIDR
// assigned to the interface, or "" if none.
func firstIPv4(name string) string {
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "dev", name, "scope", "global").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "inet" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}

// dummyNameFor returns the synthetic carrier-dummy name a bridge is
// created with (see CreateNetwork), truncated to fit IFNAMSIZ.
func dummyNameFor(bridge string) string {
	dummy := bridge + "-d"
	if len(dummy) > 15 {
		dummy = bridge[:13] + "-d"
	}
	return dummy
}

// createDirectBridge enslaves a real physical/wireless interface into
// a NEW Linux bridge (kind=="direct"): the equivalent of the host's
// own vmbr0, but under whatever name the caller picks. It shells out
// to `ip` because iproute2 is the canonical way to manage bridges on
// Linux.
//
// Returns the IPv4 CIDR that was moved off iface (so the caller can
// persist it and restore it on delete), or "" if iface had none.
func createDirectBridge(name, iface string, vlanAware bool) (movedCIDR string, err error) {
	if out, cerr := exec.Command("ip", "link", "add", "name", name, "type", "bridge").CombinedOutput(); cerr != nil {
		return "", fmt.Errorf("ip link add %s: %v: %s", name, cerr, strings.TrimSpace(string(out)))
	}

	// Move the IP from iface to the new bridge BEFORE attaching iface
	// as a slave. Doing it in this order keeps the host reachable on
	// the LAN throughout: the address is held in the bridge namespace
	// first, and iface only loses it once the bridge is already
	// holding it — never a window with no IP anywhere.
	moved, merr := moveIPv4ToBridge(iface, name)
	if merr != nil {
		exec.Command("ip", "link", "del", name).Run()
		return "", fmt.Errorf("move IP from %s to %s: %v", iface, name, merr)
	}

	if out, aerr := exec.Command("ip", "link", "set", iface, "master", name).CombinedOutput(); aerr != nil {
		exec.Command("ip", "link", "set", iface, "nomaster").Run()
		exec.Command("ip", "link", "del", name).Run()
		return "", fmt.Errorf("ip link set %s master %s: %v: %s", iface, name, aerr, strings.TrimSpace(string(out)))
	}

	// Bug fix: bring the slave itself up. Re-parenting a DOWN interface
	// into a bridge does not implicitly bring it up on every kernel/
	// driver — without this the bridge is left in NO-CARRIER until an
	// operator manually runs `ip link set <iface> up`.
	if out, uerr := exec.Command("ip", "link", "set", iface, "up").CombinedOutput(); uerr != nil {
		exec.Command("ip", "link", "set", iface, "nomaster").Run()
		exec.Command("ip", "link", "del", name).Run()
		return "", fmt.Errorf("ip link set %s up: %v: %s", iface, uerr, strings.TrimSpace(string(out)))
	}

	if out, berr := exec.Command("ip", "link", "set", name, "up").CombinedOutput(); berr != nil {
		exec.Command("ip", "link", "set", iface, "nomaster").Run()
		exec.Command("ip", "link", "del", name).Run()
		return "", fmt.Errorf("ip link set %s up: %v: %s", name, berr, strings.TrimSpace(string(out)))
	}

	if vlanAware {
		exec.Command("ip", "link", "set", name, "type", "bridge", "vlan_filtering", "1").Run()
	}
	return moved, nil
}

// moveIPv4ToBridge transfers the first global IPv4 address (and its
// /prefix) from `from` to `to`, returning the moved CIDR (or "" if
// `from` had no global IPv4 — fine for setups where the bridge will be
// DHCP'd). The destination interface must already exist.
func moveIPv4ToBridge(from, to string) (string, error) {
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "dev", from, "scope", "global").Output()
	if err != nil {
		return "", fmt.Errorf("read %s IPs: %v", from, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !strings.Contains(line, " inet ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		cidr := fields[3]
		ip, ipnet, perr := net.ParseCIDR(cidr)
		if perr != nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		prefix, _ := ipnet.Mask.Size()
		dest := fmt.Sprintf("%s/%d", ip.String(), prefix)
		// Add to the bridge first, then remove from the slave, to
		// keep connectivity throughout.
		if out, aerr := exec.Command("ip", "addr", "add", dest, "dev", to).CombinedOutput(); aerr != nil {
			return "", fmt.Errorf("ip addr add to %s: %v: %s", to, aerr, strings.TrimSpace(string(out)))
		}
		if out, derr := exec.Command("ip", "addr", "del", cidr, "dev", from).CombinedOutput(); derr != nil {
			exec.Command("ip", "addr", "del", dest, "dev", to).Run()
			return "", fmt.Errorf("ip addr del from %s: %v: %s", from, derr, strings.TrimSpace(string(out)))
		}
		return dest, nil // only one address can be primary anyway
	}
	return "", nil
}

// restoreIPv4FromBridge is the mirror of moveIPv4ToBridge, run when a
// "direct" network is deleted: it hands movedCIDR back to iface before
// releasing it from the bridge, so the physical NIC isn't left
// address-less. Best-effort — if the bridge's address was reassigned
// independently by the operator in the meantime, this is skipped by
// the caller (see DeleteNetwork).
func restoreIPv4FromBridge(bridge, iface, movedCIDR string) error {
	if movedCIDR == "" {
		return nil
	}
	if out, err := exec.Command("ip", "addr", "add", movedCIDR, "dev", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("ip addr add %s to %s: %v: %s", movedCIDR, iface, err, strings.TrimSpace(string(out)))
	}
	exec.Command("ip", "addr", "del", movedCIDR, "dev", bridge).Run()
	return nil
}
