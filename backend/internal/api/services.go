package api

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// serviceSpec describes one systemd unit candidate for a services-list
// row. Some rows have more than one candidate unit name (libvirtd vs
// virtqemud, the split-daemon variant used by newer Fedora/Arch) — the
// first one that exists on the host wins, mirroring the exact fallback
// packaging/standalone/install.sh already uses when enabling libvirt.
type serviceSpec struct {
	key        string
	candidates []string // tried in order; first one systemd knows about wins
}

// collectServiceStatuses builds the "System Services" list. It never
// errors the whole endpoint: a host without systemd, or a unit that
// can't be probed, becomes Found:false/State:"unknown" rows instead of
// failing SystemStatus.
func (h *Handler) collectServiceStatuses(ctx context.Context) []ServiceInfo {
	specs := []serviceSpec{
		{key: "libvirt", candidates: []string{"libvirtd.service", "virtqemud.service"}},
	}
	if h.cfg.IncusEnabled {
		specs = append(specs, serviceSpec{key: "incus", candidates: []string{"incus.service"}})
	}

	out := make([]ServiceInfo, 0, len(specs)+2)
	for _, s := range specs {
		out = append(out, probeUnit(ctx, s))
	}

	// Per-bridge dnsmasq units: there is no single generic "dnsmasq"
	// unit in this codebase — one is created only for a nat/isolated
	// network that has DHCP enabled (see internal/libvirt/network.go).
	if nets, err := h.lv.ListNetworks(); err == nil {
		for _, n := range nets {
			if (n.Kind == "nat" || n.Kind == "isolated") && n.DHCP {
				unit := "webkvm-" + n.Bridge + "-dnsmasq.service"
				out = append(out, probeUnit(ctx, serviceSpec{
					key:        "dnsmasq:" + n.Bridge,
					candidates: []string{unit},
				}))
			}
		}
	}
	return out
}

// probeUnit tries each candidate unit name in order and returns the
// first one that is actually ACTIVE, else the first one merely found
// (installed but not running), else the last candidate tried with
// Found:false. Preferring "active" over "just found" matters on hosts
// that ship BOTH the legacy monolithic unit and its split-daemon
// replacement side by side — e.g. Arch's own libvirt package installs
// libvirtd.service (enabled, but left inactive/dead, socket-activated
// only for compat) alongside virtqemud.service (the one actually
// running, via virtqemud.socket). Stopping at "first found" would keep
// reporting the legacy unit's real "stopped" state even while the
// modern daemon doing the actual work is up — technically accurate for
// that one unit, but misleading as the answer to "is libvirt running".
func probeUnit(ctx context.Context, s serviceSpec) ServiceInfo {
	var firstFound, last ServiceInfo
	haveFound := false
	for _, unit := range s.candidates {
		info := queryUnit(ctx, unit)
		result := ServiceInfo{
			Unit: unit, Key: s.key,
			Description: info.desc, Active: info.active, State: info.state, Found: info.found,
		}
		last = result
		if info.found && info.active {
			return result
		}
		if info.found && !haveFound {
			firstFound = result
			haveFound = true
		}
	}
	if haveFound {
		return firstFound
	}
	return last // nothing found at all — report the last candidate tried
}

type unitQueryResult struct {
	found  bool
	active bool
	state  string
	desc   string
}

// systemctlShowRunner is a package var (not a hard call to exec.Command)
// so tests can stub it — same pattern journalctlRunner already uses in
// system.go.
var systemctlShowRunner = func(ctx context.Context, unit string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "show", unit,
		"--property=LoadState,ActiveState,Description", "--no-pager").Output()
}

// queryUnit shells out to `systemctl show` for one unit. A single call
// gets both ActiveState and Description, avoiding two subprocess spawns
// per unit. LoadState=="not-found" means the unit doesn't exist on this
// host at all (Found:false) — this is how a "package not installed
// here" is told apart from "installed but stopped".
func queryUnit(ctx context.Context, unit string) unitQueryResult {
	out, err := systemctlShowRunner(ctx, unit)
	if err != nil {
		return unitQueryResult{found: false, state: "unknown"}
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			props[k] = v
		}
	}
	if props["LoadState"] == "not-found" || props["LoadState"] == "" {
		return unitQueryResult{found: false, state: "unknown"}
	}
	state := props["ActiveState"]
	return unitQueryResult{
		found:  true,
		active: state == "active",
		state:  state,
		desc:   props["Description"],
	}
}
