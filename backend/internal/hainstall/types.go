// Package hainstall installs the optional Cluster/HA packages on the local
// node. It never creates or modifies a cluster: the wizard does that later,
// explicitly, once the operator asks for it.
package hainstall

import (
	"strings"
	"time"
)

// Module states persisted across restarts.
const (
	StateNotInstalled = "not_installed"
	StateInstalling   = "installing"
	StateInstalled    = "installed"
	StateUninstalling = "uninstalling"
	StatePartial      = "partial"
	// StatePartialGuided: WebKVM installed everything it may install; the
	// rest (AUR packages on Arch) is the operator's. Verify re-checks it.
	StatePartialGuided = "partial_guided"
)

// Step states.
const (
	StepPending = "pending"
	StepRunning = "running"
	StepDone    = "done"
	StepSkipped = "skipped"
	StepFailed  = "failed"
)

// Packages are the distro packages installed by the module. They come from
// the distribution repositories; no third-party repo is added implicitly.
var Packages = []string{
	"pacemaker",
	"corosync",
	"pcs",
	"pacemaker-cli-utils",
	"sbd",
	"fence-agents",
	"resource-agents",
	"drbd-utils",
}

// packageAlternatives lists, in order of preference, the real package names
// that satisfy a logical entry of Packages. Ubuntu (24.04+) split the
// monolithic fence-agents/resource-agents packages into curated "-base"
// packages; Debian still ships the monolithic names. The first alternative
// that is installed wins; otherwise the first one with a candidate.
var packageAlternatives = map[string][]string{
	"fence-agents":    {"fence-agents-base", "fence-agents"},
	"resource-agents": {"resource-agents-base", "resource-agents"},
}

// alternatives returns the real package names that satisfy name.
func alternatives(name string) []string {
	if alts, ok := packageAlternatives[name]; ok {
		return alts
	}
	return []string{name}
}

// Options select what an install, plan or preflight covers.
type Options struct {
	// Linstor opts into the LINBIT packages (repo must already exist).
	Linstor bool `json:"linstor"`
	// AcceptExperimental acknowledges an experimental distribution tier
	// (for example an apt-based derivative that is not Debian/Ubuntu).
	AcceptExperimental bool `json:"accept_experimental"`
	// Fencing lists the fencing layers the operator plans to use. Unknown
	// kinds are dropped. fence-agents is part of the base package set, so
	// this only shapes hints; it never installs anything extra.
	Fencing []string `json:"fencing"`
}

// FencingKinds are the accepted values of Options.Fencing.
var FencingKinds = []string{"sbd", "pve", "redfish", "ipmi"}

// Normalized drops unknown and duplicate fencing kinds (never nil).
func (o Options) Normalized() Options {
	kinds := []string{}
	for _, kind := range o.Fencing {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if containsAny(FencingKinds, kind) && !containsAny(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	o.Fencing = kinds
	return o
}

// LinstorPackages live in the LINBIT vendor repository and are only attempted
// when the operator explicitly opts in. WebKVM never pipes a remote script
// into a shell: the repo must already be configured.
var LinstorPackages = []string{
	"linstor-controller",
	"linstor-satellite",
}

// Services must stay disabled after install: corosync's package starts a
// single-node instance by default, which would fight the wizard later.
// pcsd-ruby, logd and sbd_remote are shipped (and enabled) by the Debian 13
// packages only; a distro without them reports "not found" and is skipped.
var Services = []string{"corosync", "pacemaker", "pcsd", "sbd", "pcsd-ruby", "logd", "sbd_remote"}

// Check is one preflight assertion.
type Check struct {
	ID       string `json:"id"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"` // blocker | warning
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

// Preflight is the result of probing the local node.
type Preflight struct {
	OK      bool    `json:"ok"`
	Distro  string  `json:"distro"`
	Version string  `json:"version"`
	Family  string  `json:"family,omitempty"`
	Tier    string  `json:"tier,omitempty"`
	Checks  []Check `json:"checks"`
	// Watchdog is what the node offers SBD; Virt is systemd-detect-virt
	// ("none" on bare metal); FencingHint is vm | physical | unknown and
	// drives the plain-language fencing recommendation in the UI.
	Watchdog    WatchdogInfo `json:"watchdog"`
	Virt        string       `json:"virt"`
	FencingHint string       `json:"fencing_hint"`
}

// WatchdogInfo describes the local watchdog. Type is hardware | i6300esb |
// softdog | none (softdog is a software watchdog: it does not protect
// against a hung kernel).
type WatchdogInfo struct {
	Type    string   `json:"type"`
	Devices []string `json:"devices"`
	Module  string   `json:"module,omitempty"`
}

// Package is one planned package and where it comes from.
type Package struct {
	// Name is the real package name on this node (for example
	// fence-agents-base on Ubuntu). Logical is the module entry it
	// satisfies when it differs (fence-agents).
	Name      string `json:"name"`
	Logical   string `json:"logical,omitempty"`
	Origin    string `json:"origin"`
	Installed bool   `json:"installed"`
	Candidate string `json:"candidate,omitempty"`
	Optional  bool   `json:"optional,omitempty"`
	// Layer is base | storage | fencing; Origin is official | aur |
	// third_party | manual. Guided means WebKVM never installs it: the
	// operator does (AUR, vendor repos). Source is the repository or vendor.
	Layer  string `json:"layer"`
	Guided bool   `json:"guided"`
	Source string `json:"source,omitempty"`
}

// Plan is shown to the operator before anything is installed.
type Plan struct {
	Preflight      Preflight `json:"preflight"`
	Packages       []Package `json:"packages"`
	Missing        []string  `json:"missing"`
	Services       []string  `json:"services"`
	ManualCommands []string  `json:"manual_commands"`
	Linstor        bool      `json:"linstor"`
	// Guided is true when part of the stack is the operator's job (AUR
	// packages, a repository to enable, manual storage): see GuidedSteps.
	Guided bool `json:"guided"`
	// CanInstall is true when WebKVM itself has packages to install here.
	CanInstall bool `json:"can_install"`
	// AcceptRequired is true when the tier is experimental or partial and
	// the request must carry accept_experimental.
	AcceptRequired bool     `json:"accept_required"`
	Fencing        []string `json:"fencing"`
	Family         string   `json:"family"`
	Tier           string   `json:"tier"`
	// ReposNeeded are repositories to enable by hand before installing.
	ReposNeeded []RepoNeed   `json:"repos_needed"`
	GuidedSteps []GuidedStep `json:"guided_steps"`
}

// Step is the live state of one install stage.
type Step struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// Status is persisted in DATA_DIR so the UI survives a panel failover.
type Status struct {
	State string `json:"state"`
	// Hostname is the node this status belongs to. The state file lives in
	// the shared DATA_DIR (DRBD), so the status must never be inherited by a
	// different member after a failover.
	Hostname string `json:"hostname,omitempty"`
	// UnitsActive lists the module units that are enabled or active and
	// need operator action (never null).
	UnitsActive []string `json:"units_active"`
	// UnknownUnits could not be read before the install: they were treated
	// as protected and never touched (never null).
	UnknownUnits []string `json:"unknown_units"`
	Steps        []Step   `json:"steps"`
	Missing      []string `json:"missing"`
	// GuidedMissing are missing packages only the operator can install.
	GuidedMissing []string  `json:"guided_missing"`
	FailedStep    string    `json:"failed_step,omitempty"`
	Error         string    `json:"error,omitempty"`
	Cause         string    `json:"cause,omitempty"`
	Commands      []string  `json:"commands,omitempty"`
	Log           []string  `json:"log,omitempty"`
	Linstor       bool      `json:"linstor,omitempty"`
	Distro        string    `json:"distro,omitempty"`
	Version       string    `json:"version,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}
