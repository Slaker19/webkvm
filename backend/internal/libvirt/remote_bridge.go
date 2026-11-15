package libvirt

import (
	"encoding/xml"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	libvirt "libvirt.org/go/libvirt"

	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// isRemote reports whether this connector talks to another host. Its checks
// of host-level state (bridges...) must then go through libvirt instead of
// the local /sys, which would describe the wrong machine.
func (c *Connector) isRemote() bool {
	u, err := url.Parse(c.uri)
	if err != nil || u.Hostname() == "" {
		return false
	}
	return !nodes.IsLocalHost(u.Hostname())
}

// bridgeExists reports whether name is a Linux bridge on the host the
// connector manages: /sys for the local host, libvirt's interface API for a
// remote one.
func (c *Connector) bridgeExists(name string) bool {
	if !c.isRemote() {
		return isLinuxBridge(name)
	}
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return false
	}
	conn := c.Get()
	if conn == nil {
		return false
	}
	iface, err := conn.LookupInterfaceByName(name)
	if err != nil {
		if isNoSupportErr(err) {
			// Modern libvirt (Debian 13, Ubuntu 24.04+) ships without the
			// interface driver, so host bridges cannot be enumerated over
			// the connection. Accept a plausible bridge name (qemu rejects
			// a missing bridge when the VM starts) or the bridge of one of
			// the node's libvirt networks.
			return ValidRemoteBridgeName(name) || c.remoteNetworkBridges()[name]
		}
		return false
	}
	defer iface.Free()
	xmlDesc, err := iface.GetXMLDesc(0)
	return err == nil && interfaceXMLIsBridge(xmlDesc)
}

var remoteBridgeNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)

// ValidRemoteBridgeName reports whether name is a plausible Linux interface
// name to attach a NIC to on a remote node that cannot list its bridges
// (libvirt without the interface driver): 1-15 characters of [A-Za-z0-9._-]
// (IFNAMSIZ), never "." / ".." / "lo". The name is only ever placed in a
// <source bridge='...'/> attribute; qemu rejects a bridge that does not exist
// when the VM starts.
func ValidRemoteBridgeName(name string) bool {
	return remoteBridgeNameRE.MatchString(name) && name != "." && name != ".." && name != "lo" && !strings.Contains(name, "..")
}

var interfaceBridgeRE = regexp.MustCompile(`<interface\b[^>]*\btype=['"]bridge['"]`)

// interfaceXMLIsBridge reports whether a libvirt <interface> description is a
// bridge.
func interfaceXMLIsBridge(xmlDesc string) bool { return interfaceBridgeRE.MatchString(xmlDesc) }

func libvirtNoSupportErr() error { return libvirt.Error{Code: libvirt.ERR_NO_SUPPORT} }

// isNoSupportErr reports whether err is libvirt's "this driver does not
// implement the call" (VIR_ERR_NO_SUPPORT).
func isNoSupportErr(err error) bool {
	var le libvirt.Error
	return errors.As(err, &le) && le.Code == libvirt.ERR_NO_SUPPORT
}

// ErrDomainNotFound is returned (wrapped) when a domain lookup finds nothing.
var ErrDomainNotFound = errors.New("domain not found")

func isNoDomainErr(err error) bool {
	var le libvirt.Error
	return errors.As(err, &le) && le.Code == libvirt.ERR_NO_DOMAIN
}

// IsDomainNotFound reports whether err means "no such domain": the typed
// error, libvirt's VIR_ERR_NO_DOMAIN or its "Domain not found" text (which is
// all some remote paths keep).
func IsDomainNotFound(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrDomainNotFound) || isNoDomainErr(err) ||
		strings.Contains(strings.ToLower(err.Error()), "domain not found")
}

// HasDomain reports whether the domain (UUID or name) exists on the
// connector's host. It answers false with a nil error ONLY for libvirt's
// typed VIR_ERR_NO_DOMAIN; any other failure (no connection, broken link) is
// returned as an error so the caller proceeds and reports the real problem.
// It never reconnects: a connector without a live connection fails fast.
func (c *Connector) HasDomain(id string) (bool, error) {
	conn := c.Get()
	if conn == nil {
		return false, ErrNodeUnreachable
	}
	dom, err := conn.LookupDomainByUUIDString(id)
	if err != nil {
		dom, err = conn.LookupDomainByName(id)
	}
	return domainLookupResult(dom, err)
}

func domainLookupResult(dom *libvirt.Domain, err error) (bool, error) {
	if err == nil {
		if dom != nil {
			_ = dom.Free()
		}
		return true, nil
	}
	if isNoDomainErr(err) {
		return false, nil
	}
	return false, err
}

type libvirtNetXML struct {
	Name    string `xml:"name"`
	Forward struct {
		Mode string `xml:"mode,attr"`
	} `xml:"forward"`
	Bridge struct {
		Name string `xml:"name,attr"`
	} `xml:"bridge"`
	IP struct {
		Address string `xml:"address,attr"`
		Netmask string `xml:"netmask,attr"`
		Prefix  int    `xml:"prefix,attr"`
		DHCP    *struct {
			Range struct {
				Start string `xml:"start,attr"`
				End   string `xml:"end,attr"`
			} `xml:"range"`
		} `xml:"dhcp"`
	} `xml:"ip"`
}

// networkFromLibvirtXML maps a libvirt <network> definition to the API view.
// Like local bridges, a libvirt network is addressed by its bridge name.
func networkFromLibvirtXML(xmlDesc string, active, autostart bool) (models.Network, bool) {
	var x libvirtNetXML
	if err := xml.Unmarshal([]byte(xmlDesc), &x); err != nil || x.Bridge.Name == "" || !validNetLabel(x.Bridge.Name) {
		return models.Network{}, false
	}
	kind := "isolated"
	if x.Forward.Mode == "nat" || x.Forward.Mode == "route" {
		kind = "nat"
	}
	cidr := ""
	if x.IP.Address != "" {
		ones := x.IP.Prefix
		if ones == 0 && x.IP.Netmask != "" {
			if m := net.ParseIP(x.IP.Netmask); m != nil && m.To4() != nil {
				ones, _ = net.IPMask(m.To4()).Size()
			}
		}
		if ones > 0 {
			cidr = x.IP.Address + "/" + itoa(ones)
		}
	}
	n := models.Network{
		Name: x.Bridge.Name, Kind: kind, Forward: kind, Bridge: x.Bridge.Name,
		CIDR: cidr, Gateway: x.IP.Address, DHCP: x.IP.DHCP != nil,
		Active: active, Autostart: autostart, Protected: true,
	}
	if x.IP.DHCP != nil {
		n.DHCPStart, n.DHCPEnd = x.IP.DHCP.Range.Start, x.IP.DHCP.Range.End
	}
	return n, true
}

func validNetLabel(s string) bool {
	return s != "" && !strings.ContainsAny(s, "/\\ \t\n\r") && !strings.Contains(s, "..")
}

func itoa(i int) string { return strconv.Itoa(i) }

// remoteNetworks lists what a remote node's libvirt itself can tell: its
// libvirt networks, plus bridges from the interface driver when it exists.
func (c *Connector) remoteNetworks() []models.Network {
	conn := c.Get()
	if conn == nil {
		return nil
	}
	var out []models.Network
	seen := map[string]bool{}
	if nets, err := conn.ListAllNetworks(0); err == nil {
		for i := range nets {
			nw := nets[i]
			xmlDesc, xerr := nw.GetXMLDesc(0)
			active, _ := nw.IsActive()
			auto, _ := nw.GetAutostart()
			_ = nw.Free()
			if xerr != nil {
				continue
			}
			if n, ok := networkFromLibvirtXML(xmlDesc, active, auto); ok && !seen[n.Name] {
				seen[n.Name] = true
				out = append(out, n)
			}
		}
	}
	if ifaces, err := conn.ListAllInterfaces(0); err == nil {
		for i := range ifaces {
			ifc := ifaces[i]
			name, _ := ifc.GetName()
			xmlDesc, _ := ifc.GetXMLDesc(0)
			_ = ifc.Free()
			if name != "" && !seen[name] && interfaceXMLIsBridge(xmlDesc) {
				seen[name] = true
				out = append(out, models.Network{Name: name, Kind: "direct", Forward: "direct", Bridge: name, Active: true, Autostart: true})
			}
		}
	}
	return out
}

func (c *Connector) remoteNetworkBridges() map[string]bool {
	m := map[string]bool{}
	for _, n := range c.remoteNetworks() {
		m[n.Bridge] = true
	}
	return m
}
