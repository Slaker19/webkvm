package libvirt

import (
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
		// lxdbr* / lxcbr* — LXD/LXC bridges.
		if strings.HasPrefix(name, "lxdbr") || strings.HasPrefix(name, "lxcbr") {
			continue
		}
		if _, err := os.Stat("/sys/class/net/" + name + "/bridge"); err == nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
