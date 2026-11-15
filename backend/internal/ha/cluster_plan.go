package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"webkvm/internal/sysutil"
)

// Blocker and warning codes returned by the cluster plan. They double as the
// check ids so the wizard can map a failed check to its message.
const (
	CodeClusterExists      = "cluster_exists"
	CodePackagesMissing    = "packages_missing"
	CodeWatchdogMissing    = "watchdog_missing"
	CodeInvalidNodeList    = "invalid_node_list"
	CodeInvalidClusterName = "invalid_cluster_name"
	CodeCIDRMismatch       = "cluster_cidr_mismatch"
	CodeQuorumNeedsThree   = "quorum_needs_three_nodes"
	CodeLocalNodeMissing   = "local_node_not_in_list"
	CodeHostAuthMissing    = "host_auth_missing"
	CodeHostAuthUnknown    = "host_auth_unknown"
	CodeInvalidSBDTimeout  = "invalid_sbd_timeout"
	CodeInvalidWatchdogDev = "invalid_watchdog_device"
	CodeSoftdogWarning     = "softdog_no_hardware_protection"
	CodeSBDDisabledWarning = "sbd_disabled_no_automatic_failover"
)

// DefaultSBDTimeout is SBD_WATCHDOG_TIMEOUT when the request omits it.
const DefaultSBDTimeout = 5

var (
	clusterNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	clusterNodeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	watchdogDevRE = regexp.MustCompile(`^/dev/watchdog[0-9]*$`)
)

// maxClusterNodes bounds the list well below corosync's own limit.
const maxClusterNodes = 16

// ClusterNode is one planned member. Address is the corosync ring0 address.
type ClusterNode struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// SBDRequest tunes the watchdog fencing layer.
type SBDRequest struct {
	WatchdogDevice string `json:"watchdog_device"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// ClusterRequest is the body of plan and setup. Confirm, SBDEnable and
// EnableOnBoot are only read by setup.
type ClusterRequest struct {
	ClusterName   string        `json:"cluster_name"`
	Nodes         []ClusterNode `json:"nodes"`
	SBD           SBDRequest    `json:"sbd"`
	Fencing       []string      `json:"fencing"`
	AllowTwoNodes bool          `json:"allow_two_nodes"`
	Confirm       bool          `json:"confirm"`
	SBDEnable     *bool         `json:"sbd_enable"`
	EnableOnBoot  bool          `json:"enable_on_boot"`
}

// SBDEnabled applies the default (true).
func (r ClusterRequest) SBDEnabled() bool { return r.SBDEnable == nil || *r.SBDEnable }

// SBDTimeout applies the default.
func (r ClusterRequest) SBDTimeout() int {
	if r.SBD.TimeoutSeconds == 0 {
		return DefaultSBDTimeout
	}
	return r.SBD.TimeoutSeconds
}

// PlanCheck is one assertion of the plan.
type PlanCheck struct {
	ID       string `json:"id"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"` // critical | warning | info
	Detail   string `json:"detail"`
}

// WatchdogInfo describes the local watchdog.
type WatchdogInfo struct {
	Present     bool     `json:"present"`
	Devices     []string `json:"devices"`
	Module      string   `json:"module"`
	Type        string   `json:"type"` // hardware | i6300esb | softdog | none
	Recommended bool     `json:"recommended"`
}

// VirtInfo is the systemd-detect-virt result.
type VirtInfo struct {
	Kind string `json:"kind"`
	IsVM bool   `json:"is_vm"`
}

// ClusterPlan is the answer of POST /cluster/plan.
type ClusterPlan struct {
	OK           bool         `json:"ok"`
	Blockers     []string     `json:"blockers"`
	Warnings     []string     `json:"warnings"`
	Checks       []PlanCheck  `json:"checks"`
	Watchdog     WatchdogInfo `json:"watchdog"`
	Virt         VirtInfo     `json:"virt"`
	FencingHint  string       `json:"fencing_hint"`
	Commands     []string     `json:"commands"`
	ManualSteps  []string     `json:"manual_steps"`
	NodesNeeded  int          `json:"nodes_needed"`
	WatchdogPath string       `json:"-"`
}

// ClusterEnv holds every host path and probe the planner and the setup job
// use, so tests redirect them to a temp tree and a mock runner.
type ClusterEnv struct {
	Runner        sysutil.CommandRunner
	CorosyncConf  string
	CIBPath       string
	PcsKnownHosts string
	WatchdogGlob  string
	SBDPaths      []string
	Hostname      func() (string, error)
	LocalIPs      func() []netip.Addr
	// ClusterCIDR is ha.cluster_cidr; empty means "not configured".
	ClusterCIDR string
	// PackagesOK is the hainstall gate result.
	PackagesOK bool
}

// DefaultClusterEnv points at the real host files.
func DefaultClusterEnv(runner sysutil.CommandRunner) ClusterEnv {
	if runner == nil {
		runner = sysutil.DefaultRunner
	}
	return ClusterEnv{
		Runner:        runner,
		CorosyncConf:  "/etc/corosync/corosync.conf",
		CIBPath:       "/var/lib/pacemaker/cib/cib.xml",
		PcsKnownHosts: "/var/lib/pcsd/known-hosts",
		WatchdogGlob:  "/dev/watchdog*",
		SBDPaths:      []string{"/etc/sysconfig/sbd", "/etc/default/sbd"},
		Hostname:      os.Hostname,
		LocalIPs:      interfaceAddrs,
		PackagesOK:    true,
	}
}

func interfaceAddrs() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out
}

func (e ClusterEnv) run(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	if e.Runner == nil {
		return nil, errors.New("command runner unavailable")
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return e.Runner.Run(cctx, name, args...)
}

// ClusterExists is the fail-closed guard of setup. It reports every reason
// the host may already belong to a cluster; "cannot tell" counts as exists.
func (e ClusterEnv) ClusterExists(ctx context.Context) []string {
	reasons := []string{}
	for _, item := range []struct{ path, label string }{
		{e.CorosyncConf, "corosync.conf"}, {e.CIBPath, "cluster CIB"},
	} {
		switch _, err := os.Stat(item.path); {
		case err == nil:
			reasons = append(reasons, item.label+" exists")
		case errors.Is(err, fs.ErrNotExist):
		default:
			reasons = append(reasons, item.label+" cannot be read")
		}
	}
	for _, unit := range []string{"corosync", "pacemaker"} {
		out, err := e.run(ctx, 5*time.Second, "systemctl", "is-active", unit)
		state := strings.TrimSpace(string(out))
		switch state {
		case "inactive", "failed", "unknown":
			// definitively not running (is-active exits non-zero for these)
		case "":
			reasons = append(reasons, unit+" state cannot be read")
		default:
			// active, activating, reloading, deactivating, or anything odd
			reasons = append(reasons, unit+" is "+firstWord(state))
		}
		_ = err
	}
	return reasons
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 && len(f[0]) <= 24 {
		return f[0]
	}
	return "in an unexpected state"
}

var softdogLike = map[string]bool{"softdog": true}

var knownHardwareWatchdogs = []string{"iTCO_wdt", "wdat_wdt", "sp5100_tco", "ipmi_watchdog", "hpwdt", "w83627hf_wdt", "it87_wdt", "sbc_fitpc2_wdt", "mei_wdt", "ie6xx_wdt", "i6300esb", "ib700wdt", "alim7101_wdt", "cadence_wdt", "dw_wdt", "bcm2835_wdt"}

// ProbeWatchdog reads /dev/watchdog* and lsmod/modinfo. Probe failures leave
// the corresponding fields empty; they never abort the plan.
func (e ClusterEnv) ProbeWatchdog(ctx context.Context) (WatchdogInfo, bool, bool) {
	info := WatchdogInfo{Devices: []string{}, Type: "none"}
	matches, _ := filepath.Glob(e.WatchdogGlob)
	sort.Strings(matches)
	for _, m := range matches {
		// Report canonical /dev paths (the glob root is a test seam).
		if base := filepath.Base(m); watchdogDevRE.MatchString("/dev/" + base) {
			info.Devices = append(info.Devices, "/dev/"+base)
		}
	}
	info.Present = len(info.Devices) > 0
	lsmodOK := false
	if out, err := e.run(ctx, 5*time.Second, "lsmod"); err == nil {
		lsmodOK = true
		loaded := map[string]bool{}
		for i, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) > 0 && i > 0 {
				loaded[f[0]] = true
			}
		}
		switch {
		case loaded["i6300esb"]:
			info.Module = "i6300esb"
		default:
			for _, m := range knownHardwareWatchdogs {
				if loaded[m] {
					info.Module = m
					break
				}
			}
			if info.Module == "" && loaded["softdog"] {
				info.Module = "softdog"
			}
		}
	}
	softdogAvailable := false
	if _, err := e.run(ctx, 5*time.Second, "modinfo", "softdog"); err == nil {
		softdogAvailable = true
	}
	switch {
	case info.Module == "i6300esb":
		info.Type = "i6300esb"
	case softdogLike[info.Module]:
		info.Type = "softdog"
	case info.Module != "":
		info.Type = "hardware"
	case info.Present:
		// Driver built into the kernel or lsmod unreadable: a device node
		// exists, so treat it as hardware but do not claim a module.
		info.Type = "hardware"
	case softdogAvailable:
		info.Type = "softdog"
	}
	info.Recommended = info.Present && (info.Type == "hardware" || info.Type == "i6300esb")
	return info, softdogAvailable, lsmodOK
}

var containerKinds = map[string]bool{
	"openvz": true, "lxc": true, "lxc-libvirt": true, "systemd-nspawn": true,
	"docker": true, "podman": true, "rkt": true, "wsl": true, "proot": true, "pouch": true, "container-other": true,
}

// ProbeVirt runs systemd-detect-virt. Exit 1 with "none" means bare metal.
func (e ClusterEnv) ProbeVirt(ctx context.Context) (VirtInfo, bool) {
	out, err := e.run(ctx, 5*time.Second, "systemd-detect-virt")
	kind := strings.TrimSpace(string(out))
	if kind == "" || strings.ContainsAny(kind, " \n\t") || len(kind) > 32 {
		if err != nil || kind == "" {
			return VirtInfo{Kind: "unknown"}, false
		}
		return VirtInfo{Kind: "unknown"}, false
	}
	return VirtInfo{Kind: kind, IsVM: kind != "none" && !containerKinds[kind]}, true
}

type planBuilder struct {
	plan *ClusterPlan
	seen map[string]bool
}

func (b *planBuilder) add(id string, ok bool, severity, detail string) {
	b.plan.Checks = append(b.plan.Checks, PlanCheck{ID: id, OK: ok, Severity: severity, Detail: detail})
	if ok || b.seen[id] {
		return
	}
	b.seen[id] = true
	switch severity {
	case "critical":
		b.plan.Blockers = append(b.plan.Blockers, id)
	case "warning":
		b.plan.Warnings = append(b.plan.Warnings, id)
	}
}

// ValidateClusterRequest checks names, addresses and the cluster CIDR. It is
// pure so the table tests cover every blocker without touching the host.
func (e ClusterEnv) validateRequest(b *planBuilder, req ClusterRequest) {
	if !clusterNameRE.MatchString(req.ClusterName) {
		b.add(CodeInvalidClusterName, false, "critical", "cluster name must be 1-32 characters: letters, digits, '-' or '_', starting with a letter or digit")
	} else {
		b.add(CodeInvalidClusterName, true, "critical", req.ClusterName)
	}

	listErr := ""
	names := map[string]bool{}
	addrs := map[netip.Addr]bool{}
	parsed := make([]netip.Addr, 0, len(req.Nodes))
	switch {
	case len(req.Nodes) == 0:
		listErr = "no nodes were given"
	case len(req.Nodes) > maxClusterNodes:
		listErr = fmt.Sprintf("at most %d nodes", maxClusterNodes)
	}
	for _, n := range req.Nodes {
		if listErr != "" {
			break
		}
		if !clusterNodeRE.MatchString(n.Name) {
			listErr = "invalid node name (letters, digits, '.', '_' or '-', starting with a letter or digit)"
			break
		}
		key := strings.ToLower(n.Name)
		if names[key] {
			listErr = "duplicate node name " + n.Name
			break
		}
		names[key] = true
		ip, err := netip.ParseAddr(strings.TrimSpace(n.Address))
		if err != nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.Zone() != "" {
			listErr = "node " + n.Name + " needs a valid unicast IP address"
			break
		}
		if addrs[ip] {
			listErr = "duplicate node address " + ip.String()
			break
		}
		addrs[ip] = true
		parsed = append(parsed, ip)
	}
	if listErr != "" {
		b.add(CodeInvalidNodeList, false, "critical", listErr)
		return
	}
	b.add(CodeInvalidNodeList, true, "critical", fmt.Sprintf("%d nodes", len(req.Nodes)))

	switch {
	case len(req.Nodes) < 2:
		b.add(CodeInvalidNodeList, false, "critical", "a cluster needs at least two nodes")
	case len(req.Nodes) < 3 && !req.AllowTwoNodes:
		b.add(CodeQuorumNeedsThree, false, "critical", "three nodes are needed to keep quorum when one fails")
	case len(req.Nodes) < 3:
		b.add(CodeQuorumNeedsThree, false, "warning", "two-node cluster accepted explicitly: losing one node loses quorum")
	default:
		b.add(CodeQuorumNeedsThree, true, "critical", "")
	}

	if cidr := strings.TrimSpace(e.ClusterCIDR); cidr != "" {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			b.add(CodeCIDRMismatch, false, "critical", "ha.cluster_cidr is not a valid CIDR")
		} else {
			bad := []string{}
			for i, ip := range parsed {
				if !prefix.Contains(ip) {
					bad = append(bad, req.Nodes[i].Name)
				}
			}
			if len(bad) > 0 {
				b.add(CodeCIDRMismatch, false, "critical", "outside "+prefix.String()+": "+strings.Join(bad, ", "))
			} else {
				b.add(CodeCIDRMismatch, true, "critical", prefix.String())
			}
		}
	}

	e.checkLocalMember(b, req, parsed)
}

func (e ClusterEnv) checkLocalMember(b *planBuilder, req ClusterRequest, parsed []netip.Addr) {
	host := ""
	if e.Hostname != nil {
		host, _ = e.Hostname()
	}
	host = strings.ToLower(strings.TrimSpace(host))
	short := host
	if i := strings.IndexByte(short, '.'); i > 0 {
		short = short[:i]
	}
	local := map[netip.Addr]bool{}
	if e.LocalIPs != nil {
		for _, ip := range e.LocalIPs() {
			local[ip] = true
		}
	}
	for i, n := range req.Nodes {
		name := strings.ToLower(n.Name)
		if host != "" && (name == host || name == short) || (i < len(parsed) && local[parsed[i]]) {
			b.add(CodeLocalNodeMissing, true, "critical", n.Name)
			return
		}
	}
	b.add(CodeLocalNodeMissing, false, "critical", "this host is not in the node list (matched by hostname and local IPs)")
}

// hostAuth verifies `pcs host auth` was done by reading pcsd's known-hosts
// (host names only: the tokens are never decoded). Nothing is executed.
func (e ClusterEnv) hostAuth(req ClusterRequest) (missing []string, unknown bool) {
	data, err := os.ReadFile(e.PcsKnownHosts)
	if errors.Is(err, fs.ErrNotExist) {
		for _, n := range req.Nodes {
			missing = append(missing, n.Name)
		}
		return missing, false
	}
	if err != nil {
		return nil, true
	}
	var file struct {
		KnownHosts map[string]json.RawMessage `json:"known_hosts"`
	}
	if json.Unmarshal(data, &file) != nil || file.KnownHosts == nil {
		return nil, true
	}
	for _, n := range req.Nodes {
		if _, ok := file.KnownHosts[n.Name]; !ok {
			missing = append(missing, n.Name)
		}
	}
	return missing, false
}

// Plan validates the request and probes the local node. It never fails: a
// probe it cannot answer becomes a check with ok=false.
func (e ClusterEnv) Plan(ctx context.Context, req ClusterRequest) ClusterPlan {
	plan := ClusterPlan{
		Blockers: []string{}, Warnings: []string{}, Checks: []PlanCheck{},
		Commands: []string{}, ManualSteps: []string{}, NodesNeeded: 3,
		Watchdog: WatchdogInfo{Devices: []string{}, Type: "none"},
	}
	b := &planBuilder{plan: &plan, seen: map[string]bool{}}

	if e.PackagesOK {
		b.add(CodePackagesMissing, true, "critical", "")
	} else {
		b.add(CodePackagesMissing, false, "critical", "the Cluster and HA packages are not installed on this node")
	}
	e.validateRequest(b, req)

	if reasons := e.ClusterExists(ctx); len(reasons) > 0 {
		b.add(CodeClusterExists, false, "critical", strings.Join(reasons, "; "))
	} else {
		b.add(CodeClusterExists, true, "critical", "no corosync.conf, CIB or active cluster service")
	}

	sbdOn := req.SBDEnabled()
	timeout := req.SBDTimeout()
	if timeout < 5 || timeout > 120 {
		b.add(CodeInvalidSBDTimeout, false, "critical", "SBD timeout must be 5-120 seconds")
	} else {
		b.add(CodeInvalidSBDTimeout, true, "critical", strconv.Itoa(timeout)+"s")
	}

	wd, softdogAvail, _ := e.ProbeWatchdog(ctx)
	plan.Watchdog = wd
	virt, virtOK := e.ProbeVirt(ctx)
	plan.Virt = virt
	b.add("virt", virtOK, "info", virt.Kind)

	device := ""
	switch {
	case req.SBD.WatchdogDevice != "":
		if !watchdogDevRE.MatchString(req.SBD.WatchdogDevice) {
			b.add(CodeInvalidWatchdogDev, false, "critical", "watchdog_device must look like /dev/watchdog or /dev/watchdog0")
		} else if wd.Present && !containsStr(wd.Devices, req.SBD.WatchdogDevice) {
			b.add(CodeInvalidWatchdogDev, false, "critical", req.SBD.WatchdogDevice+" does not exist on this node")
		} else {
			device = req.SBD.WatchdogDevice
			b.add(CodeInvalidWatchdogDev, true, "critical", device)
		}
	case containsStr(wd.Devices, "/dev/watchdog"):
		device = "/dev/watchdog"
	case len(wd.Devices) > 0:
		device = wd.Devices[0]
	default:
		device = "/dev/watchdog"
	}
	plan.WatchdogPath = device

	wdSeverity := "critical"
	if !sbdOn {
		wdSeverity = "warning"
	}
	switch {
	case wd.Present:
		b.add(CodeWatchdogMissing, true, "critical", strings.Join(wd.Devices, ", "))
	case softdogAvail:
		// Not loaded yet: the operator loads it by hand; setup re-checks.
		b.add(CodeWatchdogMissing, true, "critical", "no /dev/watchdog yet; softdog is available and must be loaded")
		plan.ManualSteps = append(plan.ManualSteps,
			"On every node load the softdog module and keep it across reboots: sudo modprobe softdog && echo softdog | sudo tee /etc/modules-load.d/softdog.conf")
	default:
		b.add(CodeWatchdogMissing, false, wdSeverity, "no /dev/watchdog* device and no softdog module available")
	}
	if wd.Type == "softdog" {
		b.add(CodeSoftdogWarning, false, "warning", "softdog is a software watchdog: it does not protect against a hung kernel; add external fencing")
	}
	if !sbdOn {
		b.add(CodeSBDDisabledWarning, false, "warning", "without SBD there is no watchdog fencing: automatic failover must stay disabled")
	}

	plan.FencingHint = fencingHint(wd, virt, sbdOn)

	if missing, unknown := e.hostAuth(req); unknown {
		b.add(CodeHostAuthUnknown, false, "critical", "pcsd known-hosts could not be read: authenticate the nodes with pcs host auth")
	} else if len(missing) > 0 {
		b.add(CodeHostAuthMissing, false, "critical", "pcs host auth not done for: "+strings.Join(missing, ", "))
	} else if len(req.Nodes) > 0 {
		b.add(CodeHostAuthMissing, true, "critical", "all nodes authenticated")
	}

	plan.Commands = ClusterCommands(req, device)
	plan.ManualSteps = append(plan.ManualSteps, manualSteps(req)...)
	plan.OK = len(plan.Blockers) == 0
	return plan
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func fencingHint(wd WatchdogInfo, virt VirtInfo, sbdOn bool) string {
	switch {
	case !sbdOn:
		return "external_fencing_required"
	case !wd.Present && wd.Type == "none":
		return "watchdog_required"
	case wd.Type == "softdog":
		return "softdog_add_external_fencing"
	case virt.IsVM:
		return "sbd_vm_watchdog_add_hypervisor_fencing"
	default:
		return "sbd_hardware_add_redfish_ipmi"
	}
}

func authCommand(req ClusterRequest) string {
	parts := []string{"sudo pcs host auth"}
	for _, n := range req.Nodes {
		parts = append(parts, n.Name, "addr="+n.Address)
	}
	parts = append(parts, "-u hacluster")
	return strings.Join(parts, " ")
}

func manualSteps(req ClusterRequest) []string {
	steps := []string{
		"On every node set a password for the hacluster user: sudo passwd hacluster",
		"On every node start pcsd: sudo systemctl enable --now pcsd",
	}
	if len(req.Nodes) > 0 {
		steps = append(steps,
			"On this node authenticate all members (pcs asks for the hacluster password; WebKVM never receives it): "+authCommand(req))
	}
	steps = append(steps, "Check that every node has a working watchdog (ls /dev/watchdog*) before enabling SBD")
	return steps
}

// ClusterCommands returns the pcs commands of the setup, in order, exactly as
// the job runs them (with sudo for copy/paste). Inputs are validated tokens.
func ClusterCommands(req ClusterRequest, device string) []string {
	cmds := []string{}
	for _, c := range clusterStepCommands(req, device) {
		cmds = append(cmds, c.commands...)
	}
	return cmds
}

type stepCommands struct {
	step     string
	commands []string
}

func setupArgs(req ClusterRequest) []string {
	args := []string{"cluster", "setup", req.ClusterName}
	for _, n := range req.Nodes {
		args = append(args, n.Name, "addr="+n.Address)
	}
	return args
}

func sbdEnableArgs(req ClusterRequest, device string) []string {
	return []string{"stonith", "sbd", "enable", "watchdog=" + device, "SBD_WATCHDOG_TIMEOUT=" + strconv.Itoa(req.SBDTimeout())}
}

func propertyArgs(req ClusterRequest) []string {
	args := []string{"property", "set", "stonith-enabled=true"}
	if req.SBDEnabled() {
		args = append(args, "stonith-watchdog-timeout="+strconv.Itoa(2*req.SBDTimeout())+"s")
	}
	return append(args, "no-quorum-policy=stop")
}

func pcsLine(args []string) string { return "sudo pcs " + strings.Join(args, " ") }

func clusterStepCommands(req ClusterRequest, device string) []stepCommands {
	setup := []string{pcsLine(setupArgs(req))}
	if req.SBDEnabled() {
		setup = append(setup, pcsLine(sbdEnableArgs(req, device)))
	}
	start := []string{"sudo pcs cluster start --all"}
	if req.EnableOnBoot {
		start = append(start, "sudo pcs cluster enable --all")
	}
	out := []stepCommands{}
	if req.SBDEnabled() {
		out = append(out, stepCommands{"sbd_config", []string{
			"# on every node edit the SBD file (/etc/default/sbd or /etc/sysconfig/sbd): SBD_WATCHDOG_DEV=" + device +
				" SBD_WATCHDOG_TIMEOUT=" + strconv.Itoa(req.SBDTimeout()) + " SBD_PACEMAKER=yes"}})
	}
	return append(out,
		stepCommands{"cluster_setup", setup},
		stepCommands{"cluster_start", start},
		stepCommands{"stonith_properties", []string{pcsLine(propertyArgs(req))}},
		stepCommands{"verify", []string{"sudo pcs status", "sudo pcs quorum status"}},
	)
}
