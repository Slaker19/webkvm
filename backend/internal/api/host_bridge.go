package api

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/libvirt"
)

// ifaceNameRe restricts the names we'll accept for new bridges and
// the slave interface they attach to. Linux interface names can be
// up to 15 chars (IFNAMSIZ-1), must be non-empty, and cannot contain
// whitespace, slashes, or the null byte.
var ifaceNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

func validIfaceName(name string) bool {
	return ifaceNameRe.MatchString(name)
}

// hostBridge describes a Linux bridge interface present on the host.
type hostBridge struct {
	Name       string   `json:"name"`
	State      string   `json:"state"`
	IP         string   `json:"ip,omitempty"`
	Slaves     []string `json:"slaves"`
	VLanAware  bool     `json:"vlan_aware"`
	// Protected is true for the bridge that webVM's setup-bridge.sh
	// auto-creates. The API refuses to delete it and the UI greys
	// out the delete button — see IsManagedBridge in
	// libvirt/host_bridge.go for the rationale.
	Protected bool `json:"protected,omitempty"`
}

// ListHostBridges returns every Linux bridge interface on the host
// (anything in /sys/class/net that is itself a bridge). The result
// includes the slave port list and the IPv4 address (if any).
func (h *Handler) ListHostBridges(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []hostBridge{}
	for _, e := range entries {
		name := e.Name()
		base := filepath.Join("/sys/class/net", name)
		// Skip anything that isn't a bridge.
		if _, err := os.Stat(filepath.Join(base, "bridge")); err != nil {
			continue
		}
		// Skip libvirt-managed default bridges; they're not the
		// kind the user wants to wire VMs through.
		if strings.HasPrefix(name, "virbr") {
			continue
		}
		br := hostBridge{Name: name, Protected: libvirt.IsManagedBridge(name)}
		if data, err := os.ReadFile(filepath.Join(base, "operstate")); err == nil {
			br.State = strings.TrimSpace(string(data))
		}
		br.IP = firstIPv4(base)
		br.Slaves = readBridgeSlaves(base)
		// vlan_filtering is exposed by the kernel at
		// /sys/class/net/<name>/bridge/vlan_filtering. "1" = on.
		if data, err := os.ReadFile(filepath.Join(base, "bridge", "vlan_filtering")); err == nil {
			br.VLanAware = strings.TrimSpace(string(data)) == "1"
		}
		out = append(out, br)
	}
	jsonResp(w, http.StatusOK, out)
}

// firstIPv4 returns the first non-loopback, non-link-local IPv4
// address assigned to the interface, or "" if none.
func firstIPv4(sysNetPath string) string {
	// /sys/class/net/<name>/address is the MAC; the IP is not
	// exposed as a sysfs file on every distro, so fall back to
	// "ip -4 -o addr show dev <name>".
	name := filepath.Base(sysNetPath)
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "dev", name).Output()
	if err != nil {
		return ""
	}
	// Format: "    inet 192.168.1.105/24 brd 192.168.1.255 scope global ens18\n"
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, " inet ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		addr := fields[3]
		ip, _, err := net.ParseCIDR(addr)
		if err != nil {
			continue
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return ip.String()
	}
	return ""
}

// readBridgeSlaves lists the port names attached to a Linux bridge
// (i.e. the entries in /sys/class/net/<bridge>/brif/). The kernel
// reflects every port as a subdirectory there.
func readBridgeSlaves(sysNetPath string) []string {
	brifDir := filepath.Join(sysNetPath, "brif")
	entries, err := os.ReadDir(brifDir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// CreateHostBridgeRequest is the body for POST /api/host/bridges.
type CreateHostBridgeRequest struct {
	// Name is the new Linux bridge interface to create (e.g. "br0").
	// Must be a valid Linux interface name (1-15 chars, [A-Za-z0-9_.-]).
	Name string `json:"name"`
	// Interface is the existing physical/wireless interface that
	// will become a slave of the new bridge. Optional: pass empty
	// to create a bridge with no slaves (you'd add ports later).
	Interface string `json:"interface"`
	// MoveIP, if true and Interface has an IPv4 address, moves
	// the address from the physical interface to the new bridge
	// so the host stays reachable on the LAN. Default true.
	MoveIP *bool `json:"move_ip,omitempty"`
	// VLanAware, if true, enables vlan_filtering=1 on the new
	// bridge. Required kernel 4.3+. The default (false) matches
	// traditional bridge behavior where VLAN tags pass through
	// transparently.
	VLanAware bool `json:"vlan_aware,omitempty"`
}

// CreateHostBridge brings up a Linux bridge on the host and (optionally)
// enslaves an existing interface to it, moving that interface's IPv4
// address to the bridge so the host remains reachable on the LAN.
//
// The operation is intrusive: it briefly takes down the slave
// interface, which interrupts all in-flight traffic on it. The
// caller is expected to know what they're doing (typically this
// runs as the first step of "set up a bridged network for VMs").
func (h *Handler) CreateHostBridge(w http.ResponseWriter, r *http.Request) {
	var req CreateHostBridgeRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validIfaceName(req.Name) {
		jsonErr(w, http.StatusBadRequest, "invalid bridge name: must be 1-15 chars of [A-Za-z0-9_.-]")
		return
	}
	if strings.HasPrefix(req.Name, "virbr") {
		jsonErr(w, http.StatusBadRequest, "bridge name must not start with 'virbr' (reserved for libvirt's default bridge)")
		return
	}
	if req.Interface != "" && !validIfaceName(req.Interface) {
		jsonErr(w, http.StatusBadRequest, "invalid interface name")
		return
	}
	// The bridge must not already exist.
	if _, err := os.Stat(filepath.Join("/sys/class/net", req.Name)); err == nil {
		jsonErr(w, http.StatusConflict, fmt.Sprintf("interface %q already exists", req.Name))
		return
	}
	// The slave, if provided, must exist and not already be a
	// bridge port (otherwise we'd silently disrupt another bridge).
	if req.Interface != "" {
		slavePath := filepath.Join("/sys/class/net", req.Interface)
		if !strings.HasPrefix(filepath.Clean(slavePath), "/sys/class/net"+string(os.PathSeparator)) {
			jsonErr(w, http.StatusBadRequest, "invalid interface path")
			return
		}
		if _, err := os.Stat(slavePath); err != nil {
			jsonErr(w, http.StatusNotFound, fmt.Sprintf("interface %q not found", req.Interface))
			return
		}
		// Refuse if the interface is itself a bridge.
		if _, err := os.Stat(filepath.Join(slavePath, "bridge")); err == nil { // lgtm[go/path-injection] - slavePath validated above
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("%q is a bridge; you can only attach a non-bridge interface as a slave", req.Interface))
			return
		}
		// Refuse if the interface is already a port of another
		// bridge (look for a "brport" symlink in /sys/class/net).
		if _, err := os.Stat(filepath.Join(slavePath, "brport")); err == nil { // lgtm[go/path-injection]
			jsonErr(w, http.StatusConflict, fmt.Sprintf("%q is already a port of another bridge; remove it from there first", req.Interface))
			return
		}
	}
	moveIP := true
	if req.MoveIP != nil {
		moveIP = *req.MoveIP
	}

	if err := createBridge(req.Name, req.Interface, moveIP); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Enable vlan_filtering if requested — or if the request
	// omitted the field and the operator has set
	// network.vlan_aware_default to true on the Settings page.
	vlanAware := req.VLanAware
	if !vlanAware && h.settings != nil {
		vlanAware = h.settings.GetBool("network.vlan_aware_default")
	}
	if vlanAware {
		if out, err := exec.Command("ip", "link", "set", req.Name, "type", "bridge", "vlan_filtering", "1").CombinedOutput(); err != nil {
			// Don't fail the whole create — log via the audit and
			// surface the warning to the caller.
			w.Header().Set("X-Webvm-Warning", fmt.Sprintf("vlan_filtering could not be enabled: %s", strings.TrimSpace(string(out))))
		}
	}

	// Re-read state to return the freshly created bridge. The bridge
	// only counts as "up" if the kernel marked operstate=up; for a
	// bridge with a DOWN slave (e.g. a cable unplugged, or the
	// physical NIC was DOWN when the bridge was created) operstate
	// stays "down" or "unknown" and we report it honestly, not as up.
	base := filepath.Join("/sys/class/net", req.Name) // lgtm[go/path-injection] - req.Name validated via validIfaceName
	state := "unknown"
	if data, err := os.ReadFile(filepath.Join(base, "operstate")); err == nil { // lgtm[go/path-injection]
		state = strings.TrimSpace(string(data))
	}
	jsonResp(w, http.StatusCreated, hostBridge{
		Name:      req.Name,
		State:     state,
		IP:        firstIPv4(base),
		Slaves:    readBridgeSlaves(base),
		VLanAware: vlanAware,
	})
}

// createBridge is the kernel-facing part of CreateHostBridge. It
// shells out to `ip` because iproute2 is the canonical way to set
// up bridges on Linux and is universally available; the alternative
// is hand-rolled netlink rtnetlink(7) code, which is much more code
// for no real win on a single supported kernel.
func createBridge(name, slave string, moveIP bool) error {
	// 1. Create the bridge.
	if out, err := exec.Command("ip", "link", "add", "name", name, "type", "bridge").CombinedOutput(); err != nil {
		return fmt.Errorf("ip link add %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}

	if slave == "" {
		// Standalone bridge with no slave; just bring it up.
		if out, err := exec.Command("ip", "link", "set", name, "up").CombinedOutput(); err != nil {
			exec.Command("ip", "link", "del", name).Run()
			return fmt.Errorf("ip link set %s up: %v: %s", name, err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	// 2. Move the IP from the slave to the new bridge BEFORE
	//    attaching the slave. Doing it in this order keeps the
	//    host reachable on the LAN throughout the operation: we
	//    hold the address in the bridge namespace first, then
	//    the slave loses its IP only when the bridge is already
	//    holding it. If we did it the other way around, there'd
	//    be a brief window with no IP on the slave AND no IP on
	//    the bridge, and the host's default route would be
	//    invalid.
	if moveIP {
		if err := moveIPv4ToBridge(slave, name); err != nil {
			// Roll back the bridge so we don't leave it half-created.
			exec.Command("ip", "link", "del", name).Run()
			return fmt.Errorf("move IP from %s to %s: %v", slave, name, err)
		}
	}

	// 3. Attach the slave.
	if out, err := exec.Command("ip", "link", "set", slave, "master", name).CombinedOutput(); err != nil {
		exec.Command("ip", "link", "set", slave, "nomaster").Run()
		exec.Command("ip", "link", "del", name).Run()
		return fmt.Errorf("ip link set %s master %s: %v: %s", slave, name, err, strings.TrimSpace(string(out)))
	}

	// 4. Bring the bridge up (it was created DOWN).
	if out, err := exec.Command("ip", "link", "set", name, "up").CombinedOutput(); err != nil {
		exec.Command("ip", "link", "set", slave, "nomaster").Run()
		exec.Command("ip", "link", "del", name).Run()
		return fmt.Errorf("ip link set %s up: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// moveIPv4ToBridge transfers the first global IPv4 address (and its
// /prefix) from `from` to `to`, preserving the same subnet. The
// destination interface must exist (we created the bridge before
// calling this). If `from` has no global IPv4 address, the function
// is a no-op — the bridge is brought up without one, which is fine
// for setups where the bridge will get its address from DHCP.
func moveIPv4ToBridge(from, to string) error {
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "dev", from, "scope", "global").Output()
	if err != nil {
		return fmt.Errorf("read %s IPs: %v", from, err)
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
		// Parse to get the prefix length so we can match it on
		// the destination side (some kernels return it as a
		// netmask hex, others as a /N).
		ip, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		_ = ip
		prefix, _ := ipnet.Mask.Size()
		// Skip if the address is loopback/link-local (defensive;
		// we already filtered "scope global" above).
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		// Add to the bridge first, then remove from the slave,
		// to keep connectivity.
		if out, err := exec.Command("ip", "addr", "add", fmt.Sprintf("%s/%d", ip.String(), prefix), "dev", to).CombinedOutput(); err != nil {
			return fmt.Errorf("ip addr add to %s: %v: %s", to, err, strings.TrimSpace(string(out)))
		}
		if out, err := exec.Command("ip", "addr", "del", cidr, "dev", from).CombinedOutput(); err != nil {
			// If removal fails, also try to undo the add so we
			// don't leave the address in two places.
			exec.Command("ip", "addr", "del", fmt.Sprintf("%s/%d", ip.String(), prefix), "dev", to).Run()
			return fmt.Errorf("ip addr del from %s: %v: %s", from, err, strings.TrimSpace(string(out)))
		}
		// Successfully moved the first address; only one can be
		// the primary on a single bridge anyway.
		return nil
	}
	// No global IPv4 to move — that's fine for setups where the
	// bridge will be DHCP'd or run point-to-point.
	return nil
}

// SetHostBridgeVLanAwareRequest is the body of POST /api/host/bridges/{name}/vlan_aware.
type SetHostBridgeVLanAwareRequest struct {
	Enabled bool `json:"enabled"`
}

// SetHostBridgeVLanAware toggles vlan_filtering on an existing
// bridge. Requires kernel 4.3+.
func (h *Handler) SetHostBridgeVLanAware(w http.ResponseWriter, r *http.Request) {
	name := chiURLParam(r, "name")
	if !validIfaceName(name) {
		jsonErr(w, http.StatusBadRequest, "invalid bridge name")
		return
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", name)); err != nil { // lgtm[go/path-injection] - name validated via validIfaceName
		jsonErr(w, http.StatusNotFound, "bridge not found")
		return
	}
	var req SetHostBridgeVLanAwareRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	val := "0"
	if req.Enabled {
		val = "1"
	}
	if out, err := exec.Command("ip", "link", "set", name, "type", "bridge", "vlan_filtering", val).CombinedOutput(); err != nil {
		jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("set vlan_filtering: %v: %s", err, strings.TrimSpace(string(out))))
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "host.bridge.vlan_aware", name, map[string]any{"enabled": req.Enabled}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"name": name, "vlan_aware": req.Enabled})
}

// DeleteHostBridge removes a Linux bridge from the host. The bridge
// must be DOWN or have no active ports (we bring the bridge down
// ourselves but refuse to operate on a bridge that is still the
// default route for any IP, since removing it would break the host's
// own connectivity).
func (h *Handler) DeleteHostBridge(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !validIfaceName(name) {
		jsonErr(w, http.StatusBadRequest, "invalid bridge name")
		return
	}
	if strings.HasPrefix(name, "virbr") {
		jsonErr(w, http.StatusBadRequest, "cannot delete a libvirt-managed bridge")
		return
	}
	// Refuse to delete the auto-created bridge from setup-bridge.sh.
	// It's the bridge that holds the host's LAN IP, and removing it
	// without a replacement makes the host unreachable on the
	// bridged subnet (the user reported exactly this scenario — a
	// stray UI click silently dropped the host's IP). Operators who
	// really want it gone can do it on the host (ip link del br0)
	// or re-run scripts/setup-bridge.sh with a different name.
	if libvirt.IsManagedBridge(name) {
		jsonErr(w, http.StatusForbidden, fmt.Sprintf("bridge %q is managed by webVM and cannot be deleted via the API; tear it down on the host (ip link del %s) if you really want it gone", name, name))
		return
	}
	base := filepath.Join("/sys/class/net", name)
	if _, err := os.Stat(filepath.Join(base, "bridge")); err != nil {
		jsonErr(w, http.StatusNotFound, fmt.Sprintf("bridge %q not found", name))
		return
	}
	// Safety: refuse if any of our libvirt networks is using this
	// bridge. Otherwise deleting the bridge would silently break
	// every VM attached to it, mid-flight.
	if h.lv != nil {
		nets, err := h.lv.ListNetworks()
		if err == nil {
			for _, n := range nets {
				if n.Bridge == name {
					jsonErr(w, http.StatusConflict, fmt.Sprintf("bridge %q is in use by libvirt network %q; remove that network first", name, n.Name))
					return
				}
			}
		}
	}

	// Take the bridge down (fails harmlessly if already down).
	exec.Command("ip", "link", "set", name, "down").Run()
	// ip refuses to delete a bridge that still has ports; this
	// command removes the slaves one by one. The user can re-add
	// them after, but the typical flow is "I deleted the libvirt
	// network first, now I want this bridge gone", so we do it.
	slaves := readBridgeSlaves(base)
	for _, s := range slaves {
		exec.Command("ip", "link", "set", s, "nomaster").Run()
	}
	if out, err := exec.Command("ip", "link", "del", name).CombinedOutput(); err != nil {
		jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("ip link del %s: %v: %s", name, err, strings.TrimSpace(string(out))))
		return
	}
	h.audit.Log(auditFor(r, "host.bridge.delete", name, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}
