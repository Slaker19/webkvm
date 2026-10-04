package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

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

		_, hasDevice := os.Stat(filepath.Join(base, "device"))
		_, hasBonding := os.Stat(filepath.Join(base, "bonding"))
		if hasDevice != nil && hasBonding != nil {
			continue
		}

		iface := models.HostInterface{Name: name}

		if hasBonding == nil {
			iface.Type = "bond"
			iface.Driver = "bonding"
			if modeData, err := os.ReadFile(filepath.Join(base, "bonding", "mode")); err == nil {
				parts := strings.Fields(string(modeData))
				if len(parts) > 0 {
					iface.BondMode = parts[0]
				}
			}
			if slavesData, err := os.ReadFile(filepath.Join(base, "bonding", "slaves")); err == nil {
				iface.Slaves = strings.Fields(string(slavesData))
			}
		} else {
			// Detect wifi via phy80211 or wireless directory
			_, hasPhy := os.Stat(filepath.Join(base, "phy80211"))
			_, hasWireless := os.Stat(filepath.Join(base, "wireless"))
			if hasPhy == nil || hasWireless == nil {
				iface.Type = "wifi"
			} else if data, err := os.ReadFile(filepath.Join(base, "type")); err == nil {
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
			if link, err := os.Readlink(filepath.Join(base, "device", "driver")); err == nil {
				iface.Driver = filepath.Base(link)
			}
		}

		if data, err := os.ReadFile(filepath.Join(base, "operstate")); err == nil {
			iface.State = strings.TrimSpace(string(data))
		} else {
			iface.State = "unknown"
		}

		if data, err := os.ReadFile(filepath.Join(base, "address")); err == nil {
			iface.MAC = strings.TrimSpace(string(data))
		}

		if data, err := os.ReadFile(filepath.Join(base, "speed")); err == nil {
			var sp int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &sp); err == nil && sp > 0 {
				iface.Speed = sp
			}
		}

		if link, err := os.Readlink(filepath.Join(base, "master")); err == nil {
			iface.Master = filepath.Base(link)
		}

		// ip_source: "dhcp" if the iface appears in `ip route`'s
		// default route with proto dhcp, "static" if it has any
		// global IPv4 but isn't on the DHCP list, "none" if it has
		// no IPv4 at all.
		ipVal, hasIP := hasGlobalIPv4(name)
		if hasIP {
			iface.IPv4 = ipVal
		}
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

var bondNameRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,14}$`)

func cleanSysfsNetPath(name string, parts ...string) (string, error) {
	if !bondNameRE.MatchString(name) || filepath.Base(name) != name || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid interface name: %q", name)
	}
	allParts := append([]string{"/sys/class/net", name}, parts...)
	clean := filepath.Clean(filepath.Join(allParts...))
	if !strings.HasPrefix(clean, "/sys/class/net/") {
		return "", fmt.Errorf("path traversal: %q", clean)
	}
	return clean, nil
}

func validHostInterfaceName(name string) bool {
	p, err := cleanSysfsNetPath(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func getInterfaceMaster(iface string) string {
	p, err := cleanSysfsNetPath(iface, "master")
	if err != nil {
		return ""
	}
	link, err := os.Readlink(p)
	if err != nil {
		return ""
	}
	return filepath.Base(link)
}

// isHostPrimaryUplink checks whether the given interface is the primary network
// uplink connecting the host to the network (e.g. holds default route or is the
// sole physical/bond uplink for the bridge holding the default route).
func isHostPrimaryUplink(name string) (bool, string) {
	out, err := exec.Command("ip", "-4", "route", "show", "default").Output()
	if err != nil {
		return false, ""
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	var defaultDev string
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "dev" {
			defaultDev = fields[i+1]
			break
		}
	}
	if defaultDev == "" {
		return false, ""
	}

	if defaultDev == name {
		return true, defaultDev
	}

	// Check if defaultDev is a bridge containing this interface.
	// If the bridge has multiple physical/bond uplinks, isolating one of them does NOT
	// disconnect the host because the other uplink(s) remain active.
	if _, err := os.Stat(filepath.Join("/sys/class/net", defaultDev, "bridge")); err == nil {
		slaves := []string{}
		if entries, err := os.ReadDir(filepath.Join("/sys/class/net", defaultDev, "brif")); err == nil {
			for _, e := range entries {
				sName := e.Name()
				base := filepath.Join("/sys/class/net", sName)
				_, hasDev := os.Stat(filepath.Join(base, "device"))
				_, hasBond := os.Stat(filepath.Join(base, "bonding"))
				if hasDev == nil || hasBond == nil {
					slaves = append(slaves, sName)
				}
			}
		}
		for _, s := range slaves {
			if s == name {
				if len(slaves) <= 1 {
					return true, defaultDev
				}
				return false, ""
			}
		}
	}

	// Check if name's master is defaultDev or a bond attached to defaultDev
	master := getInterfaceMaster(name)
	if master != "" {
		if master == defaultDev {
			return true, defaultDev
		}
		if bMaster := getInterfaceMaster(master); bMaster == defaultDev {
			return true, defaultDev
		}
	}

	return false, ""
}

// ConfigureHostInterface applies state/IP/isolation changes with safe rollback.
func (h *Handler) ConfigureHostInterface(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !validHostInterfaceName(name) {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("invalid or non-existent interface %q", name))
		return
	}

	var req models.ConfigureHostInterfaceRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Capture initial state for rollback
	oldIP, hasOldIP := hasGlobalIPv4(name)
	oldState := "down"
	if data, err := os.ReadFile(filepath.Join("/sys/class/net", name, "operstate")); err == nil {
		oldState = strings.TrimSpace(string(data))
	}
	var oldMaster string
	if link, err := os.Readlink(filepath.Join("/sys/class/net", name, "master")); err == nil {
		oldMaster = filepath.Base(link)
	}

	// If isolating from bridge
	if req.Isolate {
		if isPrimary, defBridge := isHostPrimaryUplink(name); isPrimary {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("cannot isolate interface %q: it is the primary uplink for bridge %s holding the host default route; isolating it would disconnect the server", name, defBridge))
			return
		}
		if out, err := exec.Command("ip", "link", "set", name, "nomaster").CombinedOutput(); err != nil {
			jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to isolate %s: %s", name, strings.TrimSpace(string(out))))
			return
		}
	}

	// Handle IPv4 change
	if req.IPv4 != nil {
		newCIDR := strings.TrimSpace(*req.IPv4)
		if newCIDR != "" {
			if _, _, err := net.ParseCIDR(newCIDR); err != nil {
				jsonErr(w, http.StatusBadRequest, fmt.Sprintf("invalid CIDR %q: %v", newCIDR, err))
				return
			}
		}

		// Flush existing IPs on this dev
		if out, err := exec.Command("ip", "addr", "flush", "dev", name).CombinedOutput(); err != nil {
			jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to flush addresses on %s: %s", name, strings.TrimSpace(string(out))))
			return
		}

		if newCIDR != "" {
			if out, err := exec.Command("ip", "addr", "add", newCIDR, "dev", name).CombinedOutput(); err != nil {
				// Rollback: restore old IP
				if hasOldIP {
					_ = exec.Command("ip", "addr", "add", oldIP, "dev", name).Run()
				}
				jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to add address %s to %s: %s", newCIDR, name, strings.TrimSpace(string(out))))
				return
			}
		}
	}

	// Handle Gateway route change
	if req.Gateway != nil {
		gw := strings.TrimSpace(*req.Gateway)
		if gw != "" {
			if ip := net.ParseIP(gw); ip == nil {
				jsonErr(w, http.StatusBadRequest, fmt.Sprintf("invalid gateway IP %q", gw))
				return
			}
			// Add route with higher metric so it doesn't break primary default unless wanted
			if out, err := exec.Command("ip", "route", "append", "default", "via", gw, "dev", name, "metric", "1024").CombinedOutput(); err != nil {
				jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to set gateway %s for %s: %s", gw, name, strings.TrimSpace(string(out))))
				return
			}
		} else {
			_ = exec.Command("ip", "route", "del", "default", "dev", name).Run()
		}
	}

	// Handle State change (up/down)
	if req.State != nil {
		st := strings.ToLower(strings.TrimSpace(*req.State))
		if st != "up" && st != "down" {
			jsonErr(w, http.StatusBadRequest, "state must be 'up' or 'down'")
			return
		}
		if out, err := exec.Command("ip", "link", "set", name, st).CombinedOutput(); err != nil {
			// Rollback state
			if oldState == "up" {
				_ = exec.Command("ip", "link", "set", name, "up").Run()
			}
			jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to set link %s %s: %s", name, st, strings.TrimSpace(string(out))))
			return
		}
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"interface":  name,
		"isolated":   req.Isolate,
		"old_master": oldMaster,
	})
}

// CreateHostBond creates a Linux bonding interface (e.g. bond0).
func (h *Handler) CreateHostBond(w http.ResponseWriter, r *http.Request) {
	var req models.CreateHostBondRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if !bondNameRE.MatchString(name) {
		jsonErr(w, http.StatusBadRequest, "invalid bond name: must be 1-15 characters [a-zA-Z0-9_.-] and start with an alphanumeric character or underscore")
		return
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", name)); err == nil {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("interface %q already exists", name))
		return
	}

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "active-backup"
	}
	validModes := map[string]bool{
		"balance-rr":    true,
		"active-backup": true,
		"balance-xor":   true,
		"broadcast":     true,
		"802.3ad":       true,
		"balance-tlb":   true,
		"balance-alb":   true,
	}
	if !validModes[mode] {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("invalid bond mode %q", mode))
		return
	}

	if len(req.Interfaces) < 2 {
		jsonErr(w, http.StatusBadRequest, "at least 2 interfaces required to create a bond")
		return
	}

	targetBridge := strings.TrimSpace(req.Bridge)
	if targetBridge != "" {
		if !validHostInterfaceName(targetBridge) {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("target bridge %s not found", targetBridge))
			return
		}
		// If attached to a bridge, assigning an IP or Gateway directly to the bond breaks bridge routing.
		if strings.TrimSpace(req.IPv4) != "" || strings.TrimSpace(req.Gateway) != "" {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("cannot assign IP or gateway directly to bond %q when attached to master bridge %q; the bridge holds the host IP and routing", name, targetBridge))
			return
		}
	}

	for _, iface := range req.Interfaces {
		if !validHostInterfaceName(iface) {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("invalid member interface %q", iface))
			return
		}
		if isPrimary, defBridge := isHostPrimaryUplink(iface); isPrimary {
			if targetBridge != defBridge {
				jsonErr(w, http.StatusBadRequest, fmt.Sprintf("cannot enslave primary uplink interface %q into bond %q unless target bridge is %q (holding default route); otherwise the server will lose connectivity", iface, name, defBridge))
				return
			}
		}
	}

	primaryIface := strings.TrimSpace(req.Primary)
	if primaryIface == "" && mode == "active-backup" {
		// Prefer the interface that is already attached to targetBridge
		for _, iface := range req.Interfaces {
			if getInterfaceMaster(iface) == targetBridge {
				primaryIface = iface
				break
			}
		}
		if primaryIface == "" {
			primaryIface = req.Interfaces[0]
		}
	}

	// If netplan is available and target bridge is specified, persist to Netplan first
	// so systemd-networkd doesn't fight the live enslavement.
	if hasNetplan() && targetBridge != "" {
		if err := persistNetplanBond(targetBridge, name, mode, req.Interfaces); err != nil {
			jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to configure netplan bond: %v", err))
			return
		}
		jsonResp(w, http.StatusOK, map[string]any{
			"status":     "ok",
			"bond":       name,
			"mode":       mode,
			"interfaces": req.Interfaces,
			"bridge":     targetBridge,
			"primary":    primaryIface,
		})
		return
	}

	// 1. Create bond interface with 100ms link monitoring
	bondArgs := []string{"link", "add", name, "type", "bond", "mode", mode, "miimon", "100"}
	if mode == "active-backup" && primaryIface != "" {
		bondArgs = append(bondArgs, "primary", primaryIface)
	}
	if out, err := exec.Command("ip", bondArgs...).CombinedOutput(); err != nil {
		jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("create bond %s: %v (%s)", name, err, strings.TrimSpace(string(out))))
		return
	}

	// Track original masters for rollback
	prevMasters := make(map[string]string)
	for _, iface := range req.Interfaces {
		if m := getInterfaceMaster(iface); m != "" {
			prevMasters[iface] = m
		}
	}

	rollback := func(reason error) {
		for _, iface := range req.Interfaces {
			_ = exec.Command("ip", "link", "set", iface, "nomaster").Run()
			if oldM, ok := prevMasters[iface]; ok && oldM != "" {
				_ = exec.Command("ip", "link", "set", iface, "master", oldM).Run()
			}
			_ = exec.Command("ip", "link", "set", iface, "up").Run()
		}
		_ = exec.Command("ip", "link", "set", name, "down").Run()
		_ = exec.Command("ip", "link", "del", name).Run()
	}

	// 2. Bring bond UP
	if out, err := exec.Command("ip", "link", "set", name, "up").CombinedOutput(); err != nil {
		rollback(err)
		jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("bring up bond %s: %v (%s)", name, err, strings.TrimSpace(string(out))))
		return
	}

	// 3. Attach bond to master bridge if specified BEFORE moving member interfaces
	if targetBridge != "" {
		if out, err := exec.Command("ip", "link", "set", name, "master", targetBridge).CombinedOutput(); err != nil {
			rollback(err)
			jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("attach bond %s to bridge %s: %v (%s)", name, targetBridge, err, strings.TrimSpace(string(out))))
			return
		}
	}

	// 4. Enslave member interfaces: secondary interfaces first, uplink last
	sortedIfaces := make([]string, 0, len(req.Interfaces))
	var uplinkIface string
	for _, iface := range req.Interfaces {
		if prevMasters[iface] == targetBridge && targetBridge != "" {
			uplinkIface = iface
		} else {
			sortedIfaces = append(sortedIfaces, iface)
		}
	}
	if uplinkIface != "" {
		sortedIfaces = append(sortedIfaces, uplinkIface)
	}

	for _, iface := range sortedIfaces {
		_ = exec.Command("ip", "link", "set", iface, "nomaster").Run()
		_ = exec.Command("ip", "addr", "flush", "dev", iface).Run()
		_ = exec.Command("ip", "link", "set", iface, "down").Run()
		if out, err := exec.Command("ip", "link", "set", iface, "master", name).CombinedOutput(); err != nil {
			rollback(err)
			jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("enslave %s to %s: %v (%s)", iface, name, err, strings.TrimSpace(string(out))))
			return
		}
		_ = exec.Command("ip", "link", "set", iface, "up").Run()
	}

	// 5. Optional CIDR / Gateway for standalone bonds only
	cidr := strings.TrimSpace(req.IPv4)
	if targetBridge == "" && cidr != "" {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			rollback(err)
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("invalid CIDR %q", cidr))
			return
		}
		if out, err := exec.Command("ip", "addr", "add", cidr, "dev", name).CombinedOutput(); err != nil {
			rollback(err)
			jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("assign %s to %s: %v (%s)", cidr, name, err, strings.TrimSpace(string(out))))
			return
		}
	}

	gw := strings.TrimSpace(req.Gateway)
	if targetBridge == "" && gw != "" {
		if ip := net.ParseIP(gw); ip == nil {
			rollback(fmt.Errorf("invalid gateway"))
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("invalid gateway IP %q", gw))
			return
		}
		_ = exec.Command("ip", "route", "append", "default", "via", gw, "dev", name, "metric", "1024").Run()
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"bond":       name,
		"mode":       mode,
		"interfaces": req.Interfaces,
		"bridge":     targetBridge,
		"primary":    primaryIface,
		"ipv4":       cidr,
	})
}

// DeleteHostBond removes a bonding interface safely restoring original slave if attached to host bridge.
func (h *Handler) DeleteHostBond(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !bondNameRE.MatchString(name) {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("invalid bond name %q", name))
		return
	}

	base := filepath.Join("/sys/class/net", name)
	if _, err := os.Stat(filepath.Join(base, "bonding")); err != nil {
		jsonErr(w, http.StatusNotFound, fmt.Sprintf("interface %q is not a bonding interface", name))
		return
	}

	var masterBridge string
	if link, err := os.Readlink(filepath.Join(base, "master")); err == nil {
		masterBridge = filepath.Base(link)
	}

	var slaves []string
	if data, err := os.ReadFile(filepath.Join(base, "bonding", "slaves")); err == nil {
		slaves = strings.Fields(string(data))
	}

	var primarySlave string
	if data, err := os.ReadFile(filepath.Join(base, "bonding", "primary")); err == nil {
		primarySlave = strings.TrimSpace(string(data))
	}
	if primarySlave == "" && len(slaves) > 0 {
		primarySlave = slaves[0]
	}

	// If Netplan manages this bridge and bond:
	if hasNetplan() && masterBridge != "" {
		if err := revertNetplanBond(masterBridge, name, primarySlave); err != nil {
			jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to revert netplan bond: %v", err))
			return
		}
		_ = exec.Command("ip", "link", "del", name).Run()
		jsonResp(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"deleted":        name,
			"restored_slave": primarySlave,
			"bridge":         masterBridge,
		})
		return
	}

	// Pure kernel teardown:
	// 1. If attached to bridge, attach primarySlave back to bridge before tearing down bond
	if masterBridge != "" && primarySlave != "" {
		_ = exec.Command("ip", "link", "set", primarySlave, "nomaster").Run()
		_ = exec.Command("ip", "link", "set", primarySlave, "master", masterBridge).Run()
		_ = exec.Command("ip", "link", "set", primarySlave, "up").Run()
	}

	// 2. Release other slaves
	for _, s := range slaves {
		if s != primarySlave || masterBridge == "" {
			_ = exec.Command("ip", "link", "set", s, "nomaster").Run()
			_ = exec.Command("ip", "link", "set", s, "up").Run()
		}
	}

	// 3. Remove bond
	_ = exec.Command("ip", "link", "set", name, "nomaster").Run()
	_ = exec.Command("ip", "link", "set", name, "down").Run()
	if out, err := exec.Command("ip", "link", "del", name).CombinedOutput(); err != nil {
		jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("delete bond %s: %v (%s)", name, err, strings.TrimSpace(string(out))))
		return
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"deleted":        name,
		"restored_slave": primarySlave,
		"bridge":         masterBridge,
	})
}
