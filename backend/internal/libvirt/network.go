package libvirt

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"

	"webkvm/internal/models"
	"webkvm/internal/netstore"
)

// WebKVM uses ONE network model: real OS-level Linux bridges (vmbr0,
// vmbr1, … — Proxmox-style). libvirt virtual networks (virbr0, etc.)
// are never created, listed or used. KVM attaches with
// <interface type='bridge'> and Incus with nictype=bridged
// parent=<bridge>, both against these host bridges.
//
// Every bridge is one of three kinds (Network.Kind /
// CreateNetworkRequest.Kind):
//   - "direct": a real physical/wireless NIC enslaved into a bridge
//     with a user-chosen name — VMs/containers on it share the host's
//     real LAN, exactly like the host's own vmbr0.
//   - "isolated": a bare bridge (no physical NIC) with an optional
//     static IP/DHCP subnet, no internet access.
//   - "nat": the same bare-bridge shape as "isolated", plus a
//     masquerade rule (added by the firewall package) so its subnet
//     reaches the internet through the host.
//
// netStore persists which kind + extra state (enslaved interface, NAT
// CIDR) each WebKVM-created bridge has — kernel state alone can't
// distinguish "isolated" from "nat with the rule removed by hand", or
// tell a deliberately-enslaved uplink NIC from a VM/container tap.

// netStore is set once at startup (SetNetStore) by cmd/server/main.go.
// A nil store degrades gracefully: everything still works, but every
// bridge falls back to best-effort Kind inference (see ListNetworks).
var netStoreVar *netstore.Store

// SetNetStore wires the persisted network-kind store. Called once at
// startup.
func SetNetStore(s *netstore.Store) { netStoreVar = s }

// natChecker reports whether a bridge/CIDR currently has a masquerade
// rule applied — used only as a fallback to infer Kind for a "nat"
// bridge that has no netStore record (created before this feature, or
// by the installer). Wired by cmd/server/main.go to avoid libvirt
// importing the firewall package.
var natChecker func(bridge string) bool

// SetNATChecker wires the best-effort "does this bridge have a NAT
// rule" check used for Kind inference. Called once at startup.
func SetNATChecker(fn func(bridge string) bool) { natChecker = fn }

// IsManagedNetwork reports whether a network id is WebKVM-managed in
// the sense of "protected from deletion" — the host's primary bridge,
// or (defensively) any bridge WebKVM did not create itself.
func IsManagedNetwork(id string) bool {
	return IsManagedBridge(id)
}

// ListNetworks returns the host's REAL Linux bridges. Virtual/NAT
// bridges (virbr*, lxdbr*, lxcbr*, docker, br-*) are excluded — they
// are plumbing, never a place to attach a VM/container.
func (c *Connector) ListNetworks() ([]models.Network, error) {
	bridges := listLinuxBridges()
	result := make([]models.Network, 0, len(bridges))
	for _, b := range bridges {
		result = append(result, networkView(b))
	}
	return result, nil
}

// networkView builds the API-facing Network for one existing bridge,
// using its netStore record when WebKVM created it, or best-effort
// inference from kernel state otherwise.
func networkView(name string) models.Network {
	slaves := readBridgeSlaves(name)
	dummy := dummyNameFor(name)
	var realSlaves []string
	for _, s := range slaves {
		if s != dummy {
			realSlaves = append(realSlaves, s)
		}
	}

	kind, iface := "isolated", ""
	if netStoreVar != nil {
		if rec, ok := netStoreVar.Get(name); ok {
			kind, iface = rec.Kind, rec.Interface
		} else {
			kind, iface = inferKind(name, realSlaves)
		}
	} else {
		kind, iface = inferKind(name, realSlaves)
	}

	vlanAware := false
	if data, err := os.ReadFile("/sys/class/net/" + name + "/bridge/vlan_filtering"); err == nil {
		vlanAware = strings.TrimSpace(string(data)) == "1"
	}

	return models.Network{
		Name:      name,
		Kind:      kind,
		Forward:   kind, // deprecated alias, kept for old clients
		Bridge:    name,
		Interface: iface,
		CIDR:      bridgeIPv4(name),
		DHCP:      dnsmasqUnitExists(name),
		VLanAware: vlanAware,
		Slaves:    realSlaves,
		Active:    true,
		Autostart: true,
		Protected: IsManagedBridge(name),
	}
}

// inferKind best-effort classifies a bridge WebKVM has no netStore
// record for (pre-existing installer bridges like vmbr0/vmbr1, or one
// made by hand): a real physical NIC enslaved means "direct"; a NAT
// rule for it means "nat"; otherwise "isolated".
func inferKind(name string, realSlaves []string) (kind, iface string) {
	for _, s := range realSlaves {
		if isPhysicalInterface(s) {
			return "direct", s
		}
	}
	if natChecker != nil && natChecker(name) {
		return "nat", ""
	}
	return "isolated", ""
}

// dnsmasqUnitExists reports whether the per-bridge dnsmasq DHCP unit
// (configureBridgeDHCP) is present for this bridge.
func dnsmasqUnitExists(br string) bool {
	_, err := os.Stat("/etc/webkvm/" + br + "-dnsmasq.conf")
	return err == nil
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

// resolveKind maps a request's Kind (or, for older callers, Forward)
// to one of "nat"/"isolated"/"direct". Empty/"bridge" means "isolated"
// (the pre-v2.5 behavior of forward="bridge"/"").
func resolveKind(req models.CreateNetworkRequest) string {
	k := strings.TrimSpace(req.Kind)
	if k != "" {
		return k
	}
	switch strings.TrimSpace(req.Forward) {
	case "", "bridge", "isolated":
		return "isolated"
	case "nat":
		return "nat"
	case "direct":
		return "direct"
	}
	return strings.TrimSpace(req.Forward)
}

// validIfaceName mirrors validBridgeName's charset check for the
// physical interface a "direct" network enslaves — interface names
// share the same kernel constraints as bridge names (IFNAMSIZ-1, no
// slashes/whitespace).
func validIfaceName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/ \t\n\r\\") && len(name) <= 15
}

// CreateNetwork creates a REAL Linux bridge at the OS level (Proxmox-
// style). It never creates a libvirt virtual network. The bridge is
// created live; for persistence across reboots use setup-network.sh.
func (c *Connector) CreateNetwork(req models.CreateNetworkRequest) (models.Network, error) {
	kind := resolveKind(req)
	name := strings.TrimSpace(req.Name)
	if !validBridgeName(name) {
		return models.Network{}, fmt.Errorf("invalid bridge name %q — WebKVM only manages real Linux bridges, and virtual/NAT bridge names are not allowed", name)
	}
	if isLinuxBridge(name) {
		return models.Network{}, fmt.Errorf("bridge %q already exists", name)
	}

	switch kind {
	case "direct":
		return c.createDirectNetwork(name, req)
	case "nat", "isolated":
		return c.createIsolatedOrNATNetwork(name, kind, req)
	default:
		return models.Network{}, fmt.Errorf("kind %q is not supported (must be nat, isolated or direct)", kind)
	}
}

// createDirectNetwork enslaves req.Interface into a new bridge named
// `name` (kind=="direct") and persists the record needed to release
// the NIC (and restore its IP) symmetrically on delete.
func (c *Connector) createDirectNetwork(name string, req models.CreateNetworkRequest) (models.Network, error) {
	iface := strings.TrimSpace(req.Interface)
	if iface == "" {
		return models.Network{}, fmt.Errorf("kind=direct requires interface (the physical/wireless NIC to enslave)")
	}
	if !validIfaceName(iface) {
		return models.Network{}, fmt.Errorf("invalid interface name %q", iface)
	}
	if _, err := os.Stat("/sys/class/net/" + iface); err != nil {
		return models.Network{}, fmt.Errorf("interface %q not found", iface)
	}
	if isLinuxBridge(iface) {
		return models.Network{}, fmt.Errorf("%q is itself a bridge; you can only enslave a non-bridge interface", iface)
	}
	if _, err := os.Stat("/sys/class/net/" + iface + "/brport"); err == nil {
		return models.Network{}, fmt.Errorf("%q is already a port of another bridge; remove it from there first", iface)
	}

	moved, err := createDirectBridge(name, iface, req.VLanAware)
	if err != nil {
		return models.Network{}, err
	}
	if netStoreVar != nil {
		_ = netStoreVar.Save(netstore.Record{Name: name, Kind: "direct", Interface: iface, MovedIPv4: moved})
	}
	return networkView(name), nil
}

// createIsolatedOrNATNetwork creates a bare bridge (no physical NIC),
// optionally with a static IP and DHCP, for kind "isolated" or "nat".
// For "nat" it also asks the firewall package to add a masquerade
// rule for the bridge's CIDR, rolling the whole bridge back if that
// fails (the check-then-apply in firewall.applyRuleset is atomic, so a
// failure here never leaves a half-applied ruleset — only the just-
// created bridge needs undoing).
func (c *Connector) createIsolatedOrNATNetwork(name, kind string, req models.CreateNetworkRequest) (models.Network, error) {
	if req.CIDR == "" {
		return models.Network{}, fmt.Errorf("kind=%s requires cidr (e.g. 10.20.0.0/24)", kind)
	}
	if ip, _, perr := net.ParseCIDR(req.CIDR); perr != nil || ip == nil {
		return models.Network{}, fmt.Errorf("invalid CIDR %q", req.CIDR)
	}

	if out, err := exec.Command("ip", "link", "add", name, "type", "bridge").CombinedOutput(); err != nil {
		return models.Network{}, fmt.Errorf("create Linux bridge %q: %v (%s)", name, err, strings.TrimSpace(string(out)))
	}
	// Proxmox-style: give the bridge a dummy port for carrier so it's
	// UP and dnsmasq can bind (an empty bridge stays NO-CARRIER/DOWN).
	dummy := dummyNameFor(name)
	_ = exec.Command("ip", "link", "add", dummy, "type", "dummy").Run()
	_ = exec.Command("ip", "link", "set", dummy, "master", name).Run()
	_ = exec.Command("ip", "link", "set", dummy, "up").Run()
	_ = exec.Command("ip", "link", "set", name, "up").Run()

	teardown := func() {
		exec.Command("ip", "link", "set", name, "down").Run()
		exec.Command("ip", "link", "del", name).Run()
	}

	if out, err := exec.Command("ip", "addr", "add", req.CIDR, "dev", name).CombinedOutput(); err != nil {
		teardown()
		return models.Network{}, fmt.Errorf("assign %s to %q: %v (%s)", req.CIDR, name, err, strings.TrimSpace(string(out)))
	}

	dhcp := req.DHCP != nil && *req.DHCP
	if dhcp {
		start, end, err := resolveDHCPRange(req.CIDR, req.DHCPStart, req.DHCPEnd)
		if err != nil {
			teardown()
			return models.Network{}, err
		}
		if err := configureBridgeDHCP(name, req.CIDR, start, end); err != nil {
			teardown()
			return models.Network{}, err
		}
	}

	if netStoreVar != nil {
		_ = netStoreVar.Save(netstore.Record{Name: name, Kind: kind, CIDR: req.CIDR})
	}

	if kind == "nat" {
		if firewallReapply != nil {
			if err := firewallReapply(); err != nil {
				removeDnsmasqUnit(name)
				if netStoreVar != nil {
					_ = netStoreVar.Delete(name)
				}
				teardown()
				return models.Network{}, fmt.Errorf("apply NAT rule for %q: %w", name, err)
			}
		}
		_ = exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").Run()
	}

	return networkView(name), nil
}

// firewallReapply re-renders and applies the whole nftables ruleset,
// picking up any NAT-kind bridge just added/removed from netStoreVar.
// Wired by cmd/server/main.go to avoid libvirt importing firewall.
var firewallReapply func() error

// SetFirewallReapply wires the firewall re-apply callback. Called once
// at startup.
func SetFirewallReapply(fn func() error) { firewallReapply = fn }

// removeDnsmasqUnit stops and removes the per-bridge dnsmasq DHCP unit
// (if configureBridgeDHCP ever created one for this bridge).
func removeDnsmasqUnit(br string) {
	svcPath := "/etc/systemd/system/webkvm-" + br + "-dnsmasq.service"
	if _, err := os.Stat(svcPath); err != nil {
		return
	}
	exec.Command("systemctl", "stop", "webkvm-"+br+"-dnsmasq.service").Run()
	exec.Command("systemctl", "disable", "webkvm-"+br+"-dnsmasq.service").Run()
	os.Remove(svcPath)
	os.Remove("/etc/webkvm/" + br + "-dnsmasq.conf")
	exec.Command("systemctl", "daemon-reload").Run()
}

// resolveDHCPRange returns the DHCP range to use: the caller-chosen
// start/end when both are given (validated to fall inside cidr, with
// start <= end), or today's auto-derived range when both are empty.
// Exactly one of start/end being set is rejected as ambiguous.
func resolveDHCPRange(cidr, start, end string) (string, string, error) {
	start, end = strings.TrimSpace(start), strings.TrimSpace(end)
	if start == "" && end == "" {
		s, e := dhcpRangeFor(cidr)
		if s == "" || e == "" {
			return "", "", fmt.Errorf("cannot derive a DHCP range from CIDR %q", cidr)
		}
		return s, e, nil
	}
	if start == "" || end == "" {
		return "", "", fmt.Errorf("dhcp_start and dhcp_end must both be set, or both left empty for an automatic range")
	}
	sIP := net.ParseIP(start).To4()
	eIP := net.ParseIP(end).To4()
	if sIP == nil || eIP == nil {
		return "", "", fmt.Errorf("dhcp_start/dhcp_end must be valid IPv4 addresses")
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || !ipnet.Contains(sIP) || !ipnet.Contains(eIP) {
		return "", "", fmt.Errorf("dhcp range must fall inside %s", cidr)
	}
	for i := 0; i < 4; i++ {
		if sIP[i] != eIP[i] {
			if sIP[i] > eIP[i] {
				return "", "", fmt.Errorf("dhcp_start must be <= dhcp_end")
			}
			break
		}
	}
	return start, end, nil
}

// configureBridgeDHCP writes a self-contained dnsmasq config + systemd unit
// for a shared bridge (the same pattern setup-network.sh uses for vmbr1),
// using the given (already-resolved) DHCP range.
func configureBridgeDHCP(br, cidr, start, end string) error {
	if start == "" || end == "" {
		return fmt.Errorf("cannot derive a DHCP range from CIDR %q", cidr)
	}
	confDir := "/etc/webkvm"
	_ = os.MkdirAll(confDir, 0755)
	confPath := fmt.Sprintf("%s/%s-dnsmasq.conf", confDir, br)
	conf := fmt.Sprintf(`# webkvm %s DHCP (shared KVM+Incus bridge)
interface=%s
bind-interfaces
except-interface=lo
listen-address=%s
dhcp-range=%s,%s,255.255.255.0,12h
dhcp-option=option:router,%s
dhcp-option=option:dns-server,1.1.1.1
`, br, br, strings.SplitN(cidr, "/", 2)[0], start, end, strings.SplitN(cidr, "/", 2)[0])
	if err := os.WriteFile(confPath, []byte(conf), 0644); err != nil {
		return fmt.Errorf("write dnsmasq config for %q: %w", br, err)
	}
	svcPath := "/etc/systemd/system/webkvm-" + br + "-dnsmasq.service"
	unit := fmt.Sprintf(`[Unit]
Description=webkvm dnsmasq for %s (shared bridge DHCP)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/sbin/dnsmasq --keep-in-foreground --conf-file=%s
Restart=on-failure

[Install]
WantedBy=multi-user.target
`, br, confPath)
	if err := os.WriteFile(svcPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write dnsmasq unit for %q: %w", br, err)
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("systemctl", "enable", "webkvm-"+br+"-dnsmasq.service").Run()
	if out, err := exec.Command("systemctl", "restart", "webkvm-"+br+"-dnsmasq.service").CombinedOutput(); err != nil {
		return fmt.Errorf("start dnsmasq for %q: %v (%s)", br, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// dhcpRangeFor derives a DHCP .100-.200 range from a /24-style CIDR.
func dhcpRangeFor(cidr string) (start, end string) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", ""
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return "", ""
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "", ""
	}
	base := ip4[3]
	s := base + 100
	e := base + 200
	if ones <= 24 {
		if s > 254 {
			s = 254
		}
		if e > 254 {
			e = 254
		}
		return fmt.Sprintf("%d.%d.%d.%d", ip4[0], ip4[1], ip4[2], s),
			fmt.Sprintf("%d.%d.%d.%d", ip4[0], ip4[1], ip4[2], e)
	}
	// /25 or smaller: keep the range inside the subnet (host+1..host+10).
	if s > 1 {
		return fmt.Sprintf("%d.%d.%d.%d", ip4[0], ip4[1], ip4[2], s),
			fmt.Sprintf("%d.%d.%d.%d", ip4[0], ip4[1], ip4[2], s+10)
	}
	return "", ""
}

// DeleteNetwork removes a Linux bridge from the host, releasing any
// physical NIC it enslaved (kind=="direct") and removing its NAT rule
// (kind=="nat"), for any of the 3 kinds through one code path.
func (c *Connector) DeleteNetwork(id string) error {
	if !isLinuxBridge(id) {
		return fmt.Errorf("bridge %q not found", id)
	}
	if IsManagedBridge(id) {
		return fmt.Errorf("bridge %q is the host's primary bridge and cannot be deleted via the API; tear it down on the host (ip link del %s) if you really want it gone", id, id)
	}

	var rec netstore.Record
	if netStoreVar != nil {
		rec, _ = netStoreVar.Get(id)
	}

	dummy := dummyNameFor(id)
	slaves := readBridgeSlaves(id)
	var physical []string
	for _, s := range slaves {
		if s == dummy {
			continue
		}
		if isPhysicalInterface(s) {
			physical = append(physical, s)
			continue
		}
		// A real VM/container tap (vnet*, an Incus veth, …): never
		// auto-release it, or a running guest silently loses its NIC.
		return fmt.Errorf("bridge %q has a VM/container interface (%s) attached; detach it first", id, s)
	}

	removeDnsmasqUnit(id)

	if netStoreVar != nil {
		_ = netStoreVar.Delete(id)
	}
	if rec.Kind == "nat" && firewallReapply != nil {
		if err := firewallReapply(); err != nil {
			slog.Default().Warn("network_delete_nat_reapply_failed", "bridge", id, "err", err)
		}
	}

	exec.Command("ip", "link", "set", id, "down").Run()
	for _, s := range physical {
		exec.Command("ip", "link", "set", s, "nomaster").Run()
		exec.Command("ip", "link", "set", s, "up").Run()
		if s == rec.Interface && rec.MovedIPv4 != "" {
			if err := restoreIPv4FromBridge(id, s, rec.MovedIPv4); err != nil {
				slog.Default().Warn("network_delete_restore_ip_failed", "bridge", id, "interface", s, "err", err)
			}
		}
	}
	if _, err := os.Stat("/sys/class/net/" + dummy); err == nil {
		exec.Command("ip", "link", "set", dummy, "nomaster").Run()
		exec.Command("ip", "link", "del", dummy).Run()
	}
	if out, err := exec.Command("ip", "link", "del", id).CombinedOutput(); err != nil {
		return fmt.Errorf("ip link del %s: %v: %s", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// UpdateNetwork updates a bridge's DHCP settings (isolated/nat) or
// VLAN-awareness (direct, or any bridge). Kind/CIDR/Interface are
// immutable after creation — change them by deleting and recreating.
func (c *Connector) UpdateNetwork(name string, req models.UpdateNetworkRequest) (models.Network, error) {
	if !isLinuxBridge(name) {
		return models.Network{}, fmt.Errorf("bridge %q not found", name)
	}
	if req.VLanAware != nil {
		val := "0"
		if *req.VLanAware {
			val = "1"
		}
		if out, err := exec.Command("ip", "link", "set", name, "type", "bridge", "vlan_filtering", val).CombinedOutput(); err != nil {
			return models.Network{}, fmt.Errorf("set vlan_filtering: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	if req.DHCP != nil {
		cidr := bridgeIPv4(name)
		if *req.DHCP {
			if cidr == "" {
				return models.Network{}, fmt.Errorf("bridge %q has no IP; assign one before enabling DHCP", name)
			}
			start, end, err := resolveDHCPRange(cidr, req.DHCPStart, req.DHCPEnd)
			if err != nil {
				return models.Network{}, err
			}
			if err := configureBridgeDHCP(name, cidr, start, end); err != nil {
				return models.Network{}, err
			}
		} else {
			removeDnsmasqUnit(name)
		}
	}
	return networkView(name), nil
}

// StartNetwork/StopNetwork bring a bridge up/down at the OS level.
func (c *Connector) StartNetwork(name string) (models.Network, error) {
	if !isLinuxBridge(name) {
		return models.Network{}, fmt.Errorf("bridge %q not found", name)
	}
	if out, err := exec.Command("ip", "link", "set", name, "up").CombinedOutput(); err != nil {
		return models.Network{}, fmt.Errorf("ip link set %s up: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return networkView(name), nil
}

func (c *Connector) StopNetwork(name string) (models.Network, error) {
	if !isLinuxBridge(name) {
		return models.Network{}, fmt.Errorf("bridge %q not found", name)
	}
	if IsManagedBridge(name) {
		return models.Network{}, fmt.Errorf("bridge %q is the host's primary bridge and cannot be stopped", name)
	}
	if out, err := exec.Command("ip", "link", "set", name, "down").CombinedOutput(); err != nil {
		return models.Network{}, fmt.Errorf("ip link set %s down: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return networkView(name), nil
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
