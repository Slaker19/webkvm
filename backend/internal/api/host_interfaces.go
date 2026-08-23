package api

import (
	"bufio"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"webkvm/internal/models"
)

// listHostInterfaces returns the host's physical/wireless network interfaces
// that are candidates for libvirt bridge-mode networks. We exclude the
// loopback, libvirt-managed bridges (virbr*) and per-VM tap devices (vnet*).
//
// For each candidate we also report `ip_source`: "dhcp" if the iface
// (or, more precisely, the default route) is currently using a DHCP
// lease, "static" if it has a static address, or "none" if it has no
// IPv4 at all. The frontend uses this to warn the operator that
// creating a Linux bridge with move_ip=true on a DHCP iface will
// inherit the lease — which the operator should reserve on the
// router before it's lost on lease renewal.
func (h *Handler) ListHostInterfaces(w http.ResponseWriter, r *http.Request) {
	out := []models.HostInterface{}

	dhcpIfaces := dhcpInterfacesByRoute()

	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		jsonResp(w, http.StatusOK, out)
		return
	}

	for _, e := range entries {
		name := e.Name()
		if name == "lo" {
			continue
		}
		if strings.HasPrefix(name, "vnet") || strings.HasPrefix(name, "virbr") {
			continue
		}

		base := filepath.Join("/sys/class/net", name)

		if _, err := os.Stat(filepath.Join(base, "bridge")); err == nil {
			continue
		}

		if _, err := os.Stat(filepath.Join(base, "device")); err != nil {
			continue
		}

		iface := models.HostInterface{Name: name}

		if data, err := os.ReadFile(filepath.Join(base, "type")); err == nil {
			t := strings.TrimSpace(string(data))
			switch t {
			case "1":
				iface.Type = "ethernet"
			case "6":
				iface.Type = "wifi"
			default:
				iface.Type = "other"
			}
		} else {
			iface.Type = "other"
		}

		if data, err := os.ReadFile(filepath.Join(base, "operstate")); err == nil {
			iface.State = strings.TrimSpace(string(data))
		} else {
			iface.State = "unknown"
		}

		if data, err := os.ReadFile(filepath.Join(base, "address")); err == nil {
			iface.MAC = strings.TrimSpace(string(data))
		}

		// ip_source: "dhcp" if the iface appears in `ip route`'s
		// default route with proto dhcp, "static" if it has any
		// global IPv4 but isn't on the DHCP list, "none" if it has
		// no IPv4 at all.
		_, hasIP := hasGlobalIPv4(name)
		switch {
		case dhcpIfaces[name]:
			iface.IPSource = "dhcp"
		case hasIP:
			iface.IPSource = "static"
		default:
			iface.IPSource = "none"
		}

		out = append(out, iface)
	}

	jsonResp(w, http.StatusOK, out)
}

// dhcpInterfacesByRoute returns a set of interface names that are
// currently using DHCP, derived from the kernel routing table
// (`ip route` shows `proto dhcp` for routes learned via DHCP). The
// returned set is the set of ifaces that appear in a DHCP default
// route. We use the route table rather than per-iface inspection
// because the kernel is the source of truth: the iface might be
// configured for DHCP but if no lease is currently active, the
// route table will reflect that.
func dhcpInterfacesByRoute() map[string]bool {
	out := map[string]bool{}
	cmd := exec.Command("ip", "route")
	data, err := cmd.Output()
	if err != nil {
		return out
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		// Match e.g. "default via 192.168.1.1 dev br0 proto dhcp ..."
		// We don't care about the metric, src, or other suffixes.
		if !strings.Contains(line, "proto dhcp") {
			continue
		}
		fields := strings.Fields(line)
		devIdx := -1
		for i, f := range fields {
			if f == "dev" && i+1 < len(fields) {
				devIdx = i + 1
				break
			}
		}
		if devIdx > 0 {
			out[fields[devIdx]] = true
		}
	}
	return out
}

// hasGlobalIPv4 reports whether the named interface has any
// non-link-local IPv4 address. Used to distinguish "static" (has
// IP, not on DHCP list) from "none" (no IP at all).
func hasGlobalIPv4(name string) (string, bool) {
	cmd := exec.Command("ip", "-4", "-o", "addr", "show", "dev", name, "scope", "global")
	data, err := cmd.Output()
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 4 {
		return "", false
	}
	return fields[3], true
}
