package libvirt

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"webkvm/internal/models"
)

// WebKVM v2.4 uses ONE network model: real OS-level Linux bridges
// (vmbr0, vmbr1, … — Proxmox-style). libvirt virtual networks (NAT
// virbr0, forward='bridge' libvirt nets, macvtap/direct) are no longer
// created, listed or used. KVM attaches with <interface type='bridge'>
// and Incus with nictype=bridged parent=<bridge>, both against these
// host bridges. Networks are configured at the OS level
// (setup-network.sh); this package only lists and validates them.

// IsManagedNetwork reports whether a network id is WebKVM-managed. In the
// agnostic-L2 model every network is an OS-level host bridge, so every id
// is "managed" and the API refuses to delete any of them (clean 403).
func IsManagedNetwork(id string) bool {
	return true
}

// ListNetworks returns the host's REAL Linux bridges as the only network
// resources. Virtual/NAT bridges (virbr*, lxdbr*, lxcbr*, docker, br-*)
// are excluded — they are plumbing, never a place to attach a VM.
func (c *Connector) ListNetworks() ([]models.Network, error) {
	bridges := listLinuxBridges()
	result := make([]models.Network, 0, len(bridges))
	for _, b := range bridges {
		result = append(result, models.Network{
			Name:      b,
			Forward:   "bridge",
			Bridge:    b,
			CIDR:      bridgeIPv4(b),
			Active:    true,
			Autostart: true,
			// Host bridges are OS-level resources (setup-network.sh);
			// the UI greys out delete and the API refuses it.
			Protected: true,
		})
	}
	return result, nil
}

// validBridgeName reports whether name is a safe identifier for a NEW host
// Linux bridge: no slashes/whitespace, no virtual/NAT prefixes, and no
// collision with an existing interface that is not itself a bridge.
func validBridgeName(name string) bool {
	if name == "" || strings.ContainsAny(name, "/ \t\n\r\\") || name == "lo" {
		return false
	}
	for _, p := range []string{"virbr", "lxdbr", "lxcbr", "incusbr", "docker", "br-"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	// Legacy libvirt virtual-network names are abandoned: they must never
	// be created as (or confused with) real bridges.
	switch name {
	case "default", "webkvm-bridge", "br0-bridge":
		return false
	}
	_, err := os.Stat("/sys/class/net/" + name) // lgtm[go/path-injection] - name validated above
	if err == nil {
		// An existing interface must already be a Linux bridge.
		return isLinuxBridge(name)
	}
	return true
}

// CreateNetwork creates a REAL Linux bridge at the OS level (Proxmox-style,
// e.g. vmbr1, vmbr2). It never creates a libvirt virtual network. The bridge
// is created live; for persistence across reboots use setup-network.sh.
func (c *Connector) CreateNetwork(req models.CreateNetworkRequest) (models.Network, error) {
	if f := strings.TrimSpace(req.Forward); f != "" && f != "bridge" {
		return models.Network{}, fmt.Errorf("forward=%q is not supported — WebKVM v2.4 only creates real Linux bridges (forward=bridge)", f)
	}
	name := strings.TrimSpace(req.Name)
	if !validBridgeName(name) {
		return models.Network{}, fmt.Errorf("invalid bridge name %q — WebKVM only manages real Linux bridges (vmbr0, vmbr1, br0, …), and virtual/NAT bridge names are not allowed", name)
	}
	if isLinuxBridge(name) {
		// Already a host bridge: return it as-is.
		return models.Network{
			Name:      name,
			Forward:   "bridge",
			Bridge:    name,
			CIDR:      bridgeIPv4(name),
			Active:    true,
			Autostart: true,
			Protected: true,
		}, nil
	}
	if out, err := exec.Command("ip", "link", "add", name, "type", "bridge").CombinedOutput(); err != nil {
		return models.Network{}, fmt.Errorf("create Linux bridge %q: %v (%s)", name, err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("ip", "link", "set", name, "up").Run()
	return models.Network{
		Name:      name,
		Forward:   "bridge",
		Bridge:    name,
		Active:    true,
		Autostart: true,
		Protected: true,
	}, nil
}

// UpdateNetwork, DeleteNetwork, StartNetwork and StopNetwork are refused:
// host Linux bridges are configured at the OS level (setup-network.sh) and
// are always active. WebKVM no longer manages libvirt virtual networks.
func (c *Connector) UpdateNetwork(name string, req models.UpdateNetworkRequest) (models.Network, error) {
	return models.Network{}, errors.New("host Linux bridges are configured at the OS level (setup-network.sh); WebKVM does not edit network definitions")
}

func (c *Connector) DeleteNetwork(id string) error {
	return errors.New("host Linux bridges are configured at the OS level (setup-network.sh) and cannot be deleted via the API")
}

func (c *Connector) StartNetwork(name string) (models.Network, error) {
	return models.Network{}, errors.New("host Linux bridges are always active; WebKVM does not start/stop OS-level bridges")
}

func (c *Connector) StopNetwork(name string) (models.Network, error) {
	return models.Network{}, errors.New("host Linux bridges are always active; WebKVM does not start/stop OS-level bridges")
}

// bridgeIPv4 returns the bridge's IPv4 CIDR (e.g. "192.168.1.30/24"), or ""
// when the bridge has no IPv4 address.
func bridgeIPv4(br string) string {
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "dev", br, "scope", "global").CombinedOutput()
	if err != nil {
		return ""
	}
	for _, f := range strings.Fields(string(out)) {
		if strings.Contains(f, "/") {
			return f
		}
	}
	return ""
}