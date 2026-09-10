package libvirt

import (
	"errors"
	"os"
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
// one webkvm.s setup-bridge.sh auto-creates. The API refuses to delete
// it (and the UI greys out the delete button) so a stray click can't
// silently remove the bridge that holds the host's LAN IP — the host
// is reachable on the LAN through br0, and tearing it down without
// a replacement is the same as yanking the network cable.
//
// Today the auto-created name is hardcoded as "br0" (see
// ensure_linux_bridge in scripts/setup-bridge.sh). If setup-bridge
// ever grows a config flag for the name, this function is the single
// place to update.
func IsManagedBridge(name string) bool {
	return name == "br0"
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
// bridge. Used by CreateNetwork to validate a forward=direct
// (macvtap) network's target interface — unlike forward=bridge, this
// deliberately does NOT require (or accept) a Linux bridge device.
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
