package libvirt

import (
	"fmt"
	"net"
	"strings"

	"webkvm/internal/models"

	"github.com/libvirt/libvirt-go"
)

// validateDNS checks a slice of DNS forwarder IPs and returns an error if any
// are invalid. An empty slice is valid (means "use host DNS").
func validateDNS(dns []string) error {
	for _, d := range dns {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if net.ParseIP(d) == nil {
			return fmt.Errorf("invalid DNS server: %q", d)
		}
	}
	return nil
}

// buildDNSXML produces a <dns>...</dns> element for libvirt. When the slice
// is empty it returns an empty string so no <dns> element is emitted and
// dnsmasq falls back to the host's resolv.conf.
func buildDNSXML(dns []string) string {
	cleaned := make([]string, 0, len(dns))
	for _, d := range dns {
		d = strings.TrimSpace(d)
		if d != "" {
			cleaned = append(cleaned, d)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	out := "<dns>"
	for _, d := range cleaned {
		out += fmt.Sprintf("<forwarder addr='%s'/>", xmlEscape(d))
	}
	out += "</dns>\n  "
	return out
}

func (c *Connector) ListNetworks() ([]models.Network, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}

	nets, err := c.conn.ListAllNetworks(libvirt.CONNECT_LIST_NETWORKS_ACTIVE | libvirt.CONNECT_LIST_NETWORKS_INACTIVE)
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}

	result := make([]models.Network, 0, len(nets))
	for i := range nets {
		n, err := networkToModel(&nets[i])
		nets[i].Free()
		if err != nil {
			continue
		}
		result = append(result, n)
	}
	return result, nil
}

func (c *Connector) CreateNetwork(req models.CreateNetworkRequest) (models.Network, error) {
	if err := c.ensureConnected(); err != nil {
		return models.Network{}, err
	}

	if err := validateDNS(req.DNS); err != nil {
		return models.Network{}, err
	}

	// For forward=bridge, libvirt expects the named interface to
	// already be a Linux bridge on the host.
	if req.Forward == "bridge" {
		bridgeName := req.Bridge
		if bridgeName == "" {
			bridgeName = bridgeNameFor(req.Name)
		}
		if !isLinuxBridge(bridgeName) {
			available := listLinuxBridges()
			hint := ""
			if len(available) == 0 {
				hint = " (the host has no Linux bridges yet — create one first)"
			} else {
				hint = " (available: " + strings.Join(available, ", ") + ")"
			}
			return models.Network{}, fmt.Errorf("'%s' is not a Linux bridge on the host%s. Create a bridge first (e.g. via the Networks page or POST /api/host/bridges) and try again", bridgeName, hint)
		}
	}

	forward := req.Forward
	if forward == "" {
		forward = "nat"
	}
	bridge := req.Bridge
	if bridge == "" {
		bridge = bridgeNameFor(req.Name)
	}

	var forwardXML string
	switch forward {
	case "nat":
		forwardXML = `<forward mode='nat'/>
  <bridge name='` + xmlEscape(bridge) + `' stp='on' delay='0'/>`
	case "bridge":
		forwardXML = `<forward mode='bridge'/>
  <bridge name='` + xmlEscape(bridge) + `'/>`
	case "isolated":
		forwardXML = `<forward mode='none'/>
  <bridge name='` + xmlEscape(bridge) + `' stp='on' delay='0'/>`
	default:
		forwardXML = `<forward mode='` + xmlEscape(forward) + `'/>
  <bridge name='` + xmlEscape(bridge) + `' stp='on' delay='0'/>`
	}

	var ipXML string
	// forward=bridge networks MUST NOT have an <ip>
	// element — the IP belongs to the underlying bridge
	// (managed externally by the host, not by libvirt), and libvirt
	// rejects any <forward mode='bridge'/> network that also specifies
	// an <ip> with "Unsupported <ip> element in network <name> with
	// forward mode='bridge'".
	if forward == "bridge" {
		// Note: req.CIDR / req.DHCP / req.DHCPStart / req.DHCPEnd
		// are intentionally ignored for bridge-mode networks.
	} else if req.CIDR != "" {
		parsed, err := parseCIDR(req.CIDR)
		if err != nil {
			return models.Network{}, fmt.Errorf("invalid CIDR %q: %w", req.CIDR, err)
		}

		dhcpEnabled := true
		if req.DHCP != nil {
			dhcpEnabled = *req.DHCP
		}

		start := req.DHCPStart
		end := req.DHCPEnd
		if start == "" {
			start = parsed.DHCPStart
		}
		if end == "" {
			end = parsed.DHCPEnd
		}

		if dhcpEnabled && start != "" && end != "" {
			ipXML = fmt.Sprintf(`<ip address='%s' netmask='%s'>
      <dhcp>
        <range start='%s' end='%s'/>
      </dhcp>
    </ip>`, xmlEscape(parsed.Gateway), xmlEscape(parsed.Netmask), xmlEscape(start), xmlEscape(end))
		} else {
			ipXML = fmt.Sprintf(`<ip address='%s' netmask='%s'/>`, xmlEscape(parsed.Gateway), xmlEscape(parsed.Netmask))
		}
	}

	xmlStr := fmt.Sprintf(`<network>
  <name>%s</name>
  %s
  %s%s
</network>`, xmlEscape(req.Name), forwardXML, buildDNSXML(req.DNS), ipXML)

	net, err := c.conn.NetworkDefineXML(xmlStr)
	if err != nil {
		return models.Network{}, fmt.Errorf("define network: %w", err)
	}
	defer net.Free()

	autostart := true
	if req.Autostart != nil {
		autostart = *req.Autostart
	}
	if err := net.SetAutostart(autostart); err != nil {
		net.Undefine()
		return models.Network{}, fmt.Errorf("set autostart: %w", err)
	}

	if err := net.Create(); err != nil {
		net.Undefine()
		return models.Network{}, fmt.Errorf("create network: %w", err)
	}

	return networkToModel(net)
}

// IsManagedNetwork reports whether the given network name is one that
// webkvm.s setup-bridge.sh (legacy) auto-created. The API refuses to
// delete these (and the UI greys out the delete button) so a stray
// click can't silently remove the bridge that holds the host's LAN
// IP — doing so would also delete the bridged network's only path
// to the outside world, breaking every VM attached to it.
//
// The legacy auto-created name is "br0-bridge" (see
// ensure_bridge_network in scripts/setup-bridge.sh). This function
// exists to protect that network on upgraded installations. New
// installs use NAT and don't create this network.
func IsManagedNetwork(name string) bool {
	return name == "br0-bridge"
}

func (c *Connector) DeleteNetwork(id string) error {
	if IsManagedNetwork(id) {
		return fmt.Errorf("network %q is managed by webkvm and cannot be deleted via the API; remove the underlying Linux bridge manually (or re-run setup-bridge.sh) if you really want it gone", id)
	}
	net, err := c.lookupNetwork(id)
	if err != nil {
		return err
	}
	defer net.Free()

	if active, _ := net.IsActive(); active {
		if err := net.Destroy(); err != nil {
			return err
		}
	}

	return net.Undefine()
}

// UpdateNetwork applies changes to a network. The network is briefly destroyed
// and re-created so that the new DHCP range and other settings take effect
// immediately. This requires no VMs to be running on the network.
func (c *Connector) UpdateNetwork(name string, req models.UpdateNetworkRequest) (models.Network, error) {
	if err := c.ensureConnected(); err != nil {
		return models.Network{}, err
	}

	if err := validateDNS(req.DNS); err != nil {
		return models.Network{}, err
	}

	net, err := c.lookupNetwork(name)
	if err != nil {
		return models.Network{}, err
	}
	defer net.Free()

	xmlDesc, err := net.GetXMLDesc(0)
	if err != nil {
		return models.Network{}, fmt.Errorf("get xml: %w", err)
	}

	forward := extractNetworkForward(xmlDesc)

	// Bridge-mode networks don't (and can't) have an <ip> block in
	// their XML — the IP belongs to the underlying Linux bridge.
	// For those, the only thing the user can update via the UI is
	// autostart and DNS, both of which are applied below without
	// touching the IP block.
	var parsed cidrInfo
	cidr, _ := extractNetworkCIDR(xmlDesc)
	if cidr != "" {
		p, err := parseCIDR(cidr)
		if err != nil {
			return models.Network{}, fmt.Errorf("parse current CIDR %q: %w", cidr, err)
		}
		parsed = p
	}

	dhcpEnabled := req.DHCP != nil && *req.DHCP
	if req.DHCP == nil {
		_, curEnd := extractNetworkDHCP(xmlDesc)
		dhcpEnabled = curEnd != ""
	}
	start := req.DHCPStart
	end := req.DHCPEnd
	if start == "" && parsed.Gateway != "" {
		start = parsed.DHCPStart
	}
	if end == "" && parsed.Gateway != "" {
		end = parsed.DHCPEnd
	}

	// DNS: nil means "leave as is", []string{} means "clear"
	var dnsXML string
	if req.DNS != nil {
		dnsXML = buildDNSXML(req.DNS)
	} else {
		// Preserve the current DNS block
		dnsXML = extractNetworkDNSBlock(xmlDesc)
	}

	bridge := name
	if b, _ := net.GetBridgeName(); b != "" {
		bridge = b
	}

	var forwardXML string
	switch forward {
	case "nat":
		forwardXML = `<forward mode='nat'/>
  <bridge name='` + xmlEscape(bridge) + `' stp='on' delay='0'/>`
	case "bridge":
		forwardXML = `<forward mode='bridge'/>
  <bridge name='` + xmlEscape(bridge) + `'/>`
	case "isolated":
		forwardXML = `<forward mode='none'/>
  <bridge name='` + xmlEscape(bridge) + `' stp='on' delay='0'/>`
	default:
		forwardXML = `<forward mode='` + xmlEscape(forward) + `'/>
  <bridge name='` + xmlEscape(bridge) + `' stp='on' delay='0'/>`
	}

	// Same forward=bridge restriction as CreateNetwork: no <ip>
	// block on a network that points at an external bridge.
	// For NAT/isolated/default networks the <ip> block is required, so
	// refuse if we don't have one.
	var ipXML string
	if forward == "bridge" {
		// ipXML stays empty — bridge networks have no <ip>.
	} else if parsed.Gateway == "" {
		return models.Network{}, fmt.Errorf("network has no IP configuration")
	} else if dhcpEnabled && start != "" && end != "" {
		ipXML = fmt.Sprintf(`<ip address='%s' netmask='%s'>
      <dhcp>
        <range start='%s' end='%s'/>
      </dhcp>
    </ip>`, xmlEscape(parsed.Gateway), xmlEscape(parsed.Netmask), xmlEscape(start), xmlEscape(end))
	} else {
		ipXML = fmt.Sprintf(`<ip address='%s' netmask='%s'/>`, xmlEscape(parsed.Gateway), xmlEscape(parsed.Netmask))
	}

	xmlStr := fmt.Sprintf(`<network>
  <name>%s</name>
  %s
  %s%s
</network>`, xmlEscape(name), forwardXML, dnsXML, ipXML)

	wasActive, _ := net.IsActive()
	autostart, _ := net.GetAutostart()
	if req.Autostart != nil {
		autostart = *req.Autostart
	}

	if wasActive {
		if err := net.Destroy(); err != nil {
			return models.Network{}, fmt.Errorf("stop network: %w", err)
		}
	}

	if err := net.Undefine(); err != nil {
		return models.Network{}, fmt.Errorf("undefine network: %w", err)
	}

	newNet, err := c.conn.NetworkDefineXML(xmlStr)
	if err != nil {
		return models.Network{}, fmt.Errorf("redefine network: %w", err)
	}
	defer newNet.Free()

	if err := newNet.SetAutostart(autostart); err != nil {
		return models.Network{}, fmt.Errorf("set autostart: %w", err)
	}

	if wasActive {
		if err := newNet.Create(); err != nil {
			return models.Network{}, fmt.Errorf("start network: %w", err)
		}
	}

	return networkToModel(newNet)
}

// StartNetwork starts (activates) a previously defined but inactive network.
func (c *Connector) StartNetwork(name string) (models.Network, error) {
	if err := c.ensureConnected(); err != nil {
		return models.Network{}, err
	}
	net, err := c.lookupNetwork(name)
	if err != nil {
		return models.Network{}, err
	}
	defer net.Free()

	active, _ := net.IsActive()
	if active {
		return networkToModel(net)
	}
	if err := net.Create(); err != nil {
		return models.Network{}, fmt.Errorf("start network: %w", err)
	}
	return networkToModel(net)
}

// StopNetwork stops (deactivates) a network. The network definition is kept.
func (c *Connector) StopNetwork(name string) (models.Network, error) {
	if err := c.ensureConnected(); err != nil {
		return models.Network{}, err
	}
	net, err := c.lookupNetwork(name)
	if err != nil {
		return models.Network{}, err
	}
	defer net.Free()

	active, _ := net.IsActive()
	if !active {
		return networkToModel(net)
	}
	if err := net.Destroy(); err != nil {
		return models.Network{}, fmt.Errorf("stop network: %w", err)
	}
	return networkToModel(net)
}

func (c *Connector) lookupNetwork(id string) (*libvirt.Network, error) {
	net, err := c.conn.LookupNetworkByName(id)
	if err != nil {
		return nil, fmt.Errorf("network not found: %w", err)
	}
	return net, nil
}

func networkToModel(net *libvirt.Network) (models.Network, error) {
	name, _ := net.GetName()
	bridgeName, _ := net.GetBridgeName()

	active, _ := net.IsActive()
	autostart, _ := net.GetAutostart()

	var forward string
	xmlDesc, _ := net.GetXMLDesc(0)
	forward = extractNetworkForward(xmlDesc)

	cidr, gateway := extractNetworkCIDR(xmlDesc)
	dhcpStart, dhcpEnd := extractNetworkDHCP(xmlDesc)
	dns := extractNetworkDNS(xmlDesc)

	return models.Network{
		Name:      name,
		Forward:   forward,
		Bridge:    bridgeName,
		CIDR:      cidr,
		Gateway:   gateway,
		DHCP:      dhcpStart != "" || dhcpEnd != "",
		DHCPStart: dhcpStart,
		DHCPEnd:   dhcpEnd,
		DNS:       dns,
		Active:    active,
		Autostart: autostart,
		Protected: IsManagedNetwork(name),
	}, nil
}

// extractNetworkDNS parses all <forwarder addr='...'/> inside a <dns> block.
func extractNetworkDNS(xml string) []string {
	s := strings.Index(xml, "<dns>")
	if s < 0 {
		s = strings.Index(xml, "<dns ")
		if s < 0 {
			return nil
		}
	}
	e := strings.Index(xml[s:], "</dns>")
	if e < 0 {
		return nil
	}
	block := xml[s : s+e]

	out := []string{}
	for {
		tag := "<forwarder addr='"
		i := strings.Index(block, tag)
		if i < 0 {
			tag = "<forwarder addr=\""
			i = strings.Index(block, tag)
		}
		if i < 0 {
			break
		}
		i += len(tag)
		end := strings.Index(block[i:], "'")
		if end < 0 {
			end = strings.Index(block[i:], "\"")
		}
		if end < 0 {
			break
		}
		out = append(out, block[i:i+end])
		block = block[i+end:]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// extractNetworkDNSBlock returns the raw "<dns>...</dns>\n  " block as it
// should appear in a network XML, or empty string if there is no DNS.
func extractNetworkDNSBlock(xml string) string {
	s := strings.Index(xml, "<dns>")
	if s < 0 {
		s = strings.Index(xml, "<dns ")
		if s < 0 {
			return ""
		}
	}
	e := strings.Index(xml[s:], "</dns>")
	if e < 0 {
		return ""
	}
	return xml[s:s+e+len("</dns>")] + "\n  "
}

func extractNetworkForward(xml string) string {
	fwd := strings.Index(xml, "<forward")
	if fwd < 0 {
		return "nat"
	}
	end := strings.IndexByte(xml[fwd:], '>')
	if end < 0 {
		return "nat"
	}
	tag := xml[fwd : fwd+end+1]
	// Extract mode from attributes in any order
	modeKey := `mode='`
	m := strings.Index(tag, modeKey)
	if m < 0 {
		modeKey = `mode="`
		m = strings.Index(tag, modeKey)
	}
	if m < 0 {
		return "nat"
	}
	m += len(modeKey)
	e := strings.IndexByte(tag[m:], '\'')
	if e < 0 {
		e = strings.IndexByte(tag[m:], '"')
	}
	if e < 0 {
		return "nat"
	}
	return tag[m : m+e]
}

// extractNetworkCIDR returns the network CIDR (e.g. "192.168.100.0/24") and
// the gateway IP (e.g. "192.168.100.1"). libvirt only stores the gateway and
// the netmask in <ip>, so we compute the network address as gateway & netmask.
func extractNetworkCIDR(xml string) (string, string) {
	addr, netmask := extractIPAndNetmask(xml)
	if addr == "" {
		return "", ""
	}

	if netmask == "" {
		return fmt.Sprintf("%s/32", addr), addr
	}

	prefix := netmaskToPrefix(netmask)
	if prefix <= 0 {
		return fmt.Sprintf("%s/32", addr), addr
	}

	gatewayIP := net.ParseIP(addr).To4()
	maskIP := net.ParseIP(netmask).To4()
	if gatewayIP == nil || maskIP == nil {
		return addr, addr
	}

	networkIP := make(net.IP, 4)
	for i := range networkIP {
		networkIP[i] = gatewayIP[i] & maskIP[i]
	}

	return fmt.Sprintf("%s/%d", networkIP.String(), prefix), addr
}

// extractIPAndNetmask returns the raw <ip address='...'> and netmask values
// from a libvirt network XML.
func extractIPAndNetmask(xml string) (string, string) {
	start := `<ip address='`
	s := strings.Index(xml, start)
	if s < 0 {
		start = `<ip address="`
		s = strings.Index(xml, start)
		if s < 0 {
			return "", ""
		}
	}
	s += len(start)
	e := strings.Index(xml[s:], "'")
	if e < 0 {
		e = strings.Index(xml[s:], "\"")
		if e < 0 {
			return "", ""
		}
	}
	addr := xml[s : s+e]

	netmask := ""
	nmStart := `netmask='`
	nmS := strings.Index(xml, nmStart)
	if nmS >= 0 {
		nmS += len(nmStart)
		nmE := strings.Index(xml[nmS:], "'")
		if nmE >= 0 {
			netmask = xml[nmS : nmS+nmE]
		}
	}
	if netmask == "" {
		nmStart = `netmask="`
		nmS := strings.Index(xml, nmStart)
		if nmS >= 0 {
			nmS += len(nmStart)
			nmE := strings.Index(xml[nmS:], "\"")
			if nmE >= 0 {
				netmask = xml[nmS : nmS+nmE]
			}
		}
	}

	return addr, netmask
}

// extractNetworkDHCP looks for <range> inside the <dhcp> block only,
// so it doesn't match unrelated 'start=' or 'end=' attributes.
func extractNetworkDHCP(xml string) (string, string) {
	dhcpStart := strings.Index(xml, "<dhcp>")
	if dhcpStart < 0 {
		dhcpStart = strings.Index(xml, "<dhcp ")
		if dhcpStart < 0 {
			return "", ""
		}
	}
	dhcpEnd := strings.Index(xml[dhcpStart:], "</dhcp>")
	if dhcpEnd < 0 {
		return "", ""
	}
	dhcpBlock := xml[dhcpStart : dhcpStart+dhcpEnd]

	var start, end string

	sStart := `<range start='`
	s := strings.Index(dhcpBlock, sStart)
	if s < 0 {
		sStart = `<range start="`
		s = strings.Index(dhcpBlock, sStart)
	}
	if s >= 0 {
		s += len(sStart)
		e := strings.Index(dhcpBlock[s:], "'")
		if e < 0 {
			e = strings.Index(dhcpBlock[s:], "\"")
		}
		if e >= 0 {
			start = dhcpBlock[s : s+e]
		}
	}

	eStart := `end='`
	s2 := strings.Index(dhcpBlock, eStart)
	if s2 < 0 {
		eStart = `end="`
		s2 = strings.Index(dhcpBlock, eStart)
	}
	if s2 >= 0 {
		s2 += len(eStart)
		e := strings.Index(dhcpBlock[s2:], "'")
		if e < 0 {
			e = strings.Index(dhcpBlock[s2:], "\"")
		}
		if e >= 0 {
			end = dhcpBlock[s2 : s2+e]
		}
	}

	return start, end
}

func netmaskToPrefix(netmask string) int {
	ip := net.ParseIP(netmask)
	if ip == nil {
		return 0
	}
	ip = ip.To4()
	if ip == nil {
		return 0
	}
	ones, _ := net.IPv4Mask(ip[0], ip[1], ip[2], ip[3]).Size()
	if ones < 0 {
		return 0
	}
	return ones
}

// bridgeNameFor returns a Linux interface name for a network. Linux limits
// network interface names to 15 characters (IFNAMSIZ=16, including null
// terminator). The "virbr-" prefix consumes 6, leaving 9 characters for the
// network name. If the name is too long, it is truncated to 9 chars to fit.
func bridgeNameFor(name string) string {
	const prefix = "virbr-"
	const maxIface = 15
	maxName := maxIface - len(prefix)
	if len(name) > maxName {
		name = name[:maxName]
	}
	return prefix + name
}

// cidrInfo holds derived values from a CIDR notation.
type cidrInfo struct {
	Gateway   string
	Netmask   string
	Prefix    int
	DHCPStart string
	DHCPEnd   string
}

// parseCIDR parses a CIDR like "192.168.100.0/24" and returns the first usable
// IP (gateway), netmask, prefix length, and the default DHCP range
// (second usable IP to last usable IP).
func parseCIDR(cidr string) (cidrInfo, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return cidrInfo{}, err
	}
	ip = ip.To4()
	if ip == nil {
		return cidrInfo{}, fmt.Errorf("not an IPv4 CIDR")
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return cidrInfo{}, fmt.Errorf("not an IPv4 CIDR")
	}
	mask := ipnet.Mask

	// First usable IP = network address + 1
	first := make(net.IP, 4)
	copy(first, ipnet.IP.To4())
	first[3]++

	// Last usable IP = broadcast - 1
	broadcast := make(net.IP, 4)
	for i := range broadcast {
		broadcast[i] = ipnet.IP.To4()[i] | ^mask[i]
	}
	last := make(net.IP, 4)
	copy(last, broadcast)
	last[3]--

	// For /31 and /32, DHCP range is unusual; default to nothing.
	dhcpStart := ""
	dhcpEnd := ""
	if ones <= 30 {
		dhcpStartIP := make(net.IP, 4)
		copy(dhcpStartIP, first)
		dhcpStartIP[3]++
		if dhcpStartIP.To4()[3] < last.To4()[3] {
			dhcpStart = dhcpStartIP.String()
			dhcpEnd = last.String()
		}
	}

	netmask := fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])

	return cidrInfo{
		Gateway:   first.String(),
		Netmask:   netmask,
		Prefix:    ones,
		DHCPStart: dhcpStart,
		DHCPEnd:   dhcpEnd,
	}, nil
}
