package libvirt

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// Errors of the remote console path. Unlike the local helper, a remote
// console must never guess a port: a wrong guess would open ANOTHER guest's
// screen on that node.
var (
	ErrGraphicsNotRunning = errors.New("the VM has no graphics port yet (not running?)")
	ErrGraphicsNotTunnel  = errors.New("the VM graphics listen address cannot be reached through the SSH tunnel")
)

var (
	graphicsBlockRE = regexp.MustCompile(`(?s)<graphics\b[^>]*?(?:/>|>.*?</graphics>)`)
	graphicsOpenRE  = regexp.MustCompile(`<graphics\b[^>]*>`)
	listenChildRE   = regexp.MustCompile(`<listen\b[^>]*>`)
)

func xmlAttr(tag, name string) string {
	re := regexp.MustCompile(`\b` + name + `=(?:'([^']*)'|"([^"]*)")`)
	m := re.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

// ParseGraphicsXML extracts the live graphics endpoint of the given type
// ("vnc" or "spice") from a domain XML as libvirt reports it for a RUNNING
// domain (autoport already resolved). It fails closed: no such device, no
// resolved port, or a listen address that is not loopback / wildcard (the
// tunnel can only reach 127.0.0.1 of the node) are errors; for the latter the
// returned info is still populated (Host = the listen address).
func ParseGraphicsXML(xmlDesc, wantType string) (GraphicsInfo, error) {
	for _, blk := range graphicsBlockRE.FindAllString(xmlDesc, -1) {
		open := graphicsOpenRE.FindString(blk)
		if xmlAttr(open, "type") != wantType {
			continue
		}
		port, err := strconv.Atoi(xmlAttr(open, "port"))
		if err != nil || port <= 0 {
			return GraphicsInfo{}, ErrGraphicsNotRunning
		}
		listen := xmlAttr(open, "listen")
		if listen == "" {
			for _, l := range listenChildRE.FindAllString(blk, -1) {
				if a := xmlAttr(l, "address"); a != "" {
					listen = a
					break
				}
				if xmlAttr(l, "type") == "socket" {
					return GraphicsInfo{}, ErrGraphicsNotTunnel
				}
			}
		}
		if listen == "" || listen == "localhost" {
			listen = "127.0.0.1"
		}
		info := GraphicsInfo{Type: wantType, Port: port, Host: listen}
		switch listen {
		case "127.0.0.1", "0.0.0.0":
		default:
			// The info is still valid (a direct connection to listen:port
			// works); only the SSH tunnel cannot reach it.
			return info, fmt.Errorf("%w (%s)", ErrGraphicsNotTunnel, listen)
		}
		return info, nil
	}
	return GraphicsInfo{}, fmt.Errorf("graphics device of type %s not configured on VM", wantType)
}

// RemoteGraphicsInfo reads the live XML of the domain through THIS
// connector (the remote node's) and parses it strictly.
func (c *Connector) RemoteGraphicsInfo(id, wantType string) (GraphicsInfo, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return GraphicsInfo{}, err
	}
	defer dom.Free()
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return GraphicsInfo{}, fmt.Errorf("get xml: %w", err)
	}
	return ParseGraphicsXML(xmlDesc, wantType)
}
