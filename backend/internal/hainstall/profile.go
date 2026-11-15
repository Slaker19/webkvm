package hainstall

import (
	"context"
	"fmt"
	"strings"
)

// Package layers: why a package is on the list.
const (
	LayerBase    = "base"    // cluster stack: membership, resources, management
	LayerStorage = "storage" // replicated storage (DRBD/LINSTOR), opt-in pieces
	LayerFencing = "fencing" // SBD and fence agents
)

// Package origins.
const (
	OriginOfficial   = "official"    // the distribution's own repositories
	OriginAUR        = "aur"         // Arch User Repository: built by the operator
	OriginThirdParty = "third_party" // a vendor repository the operator configured
	OriginManual     = "manual"      // not packaged: the operator does it by hand
)

// officialPacmanRepos are the only repositories pacman installs from. Any
// other repository (testing, chaotic-aur, distro-specific) is third party.
var officialPacmanRepos = []string{"core", "extra", "multilib"}

// pkgSpec is one logical package of a family profile.
type pkgSpec struct {
	Logical  string
	Alts     []string
	Layer    string
	Origin   string
	Optional bool // only with the storage opt-in
	// Informational entries are shown to the operator but never counted
	// as missing: nothing can verify or install them.
	Informational bool
	Source        string
	// BestEffort packages are installed when a repository carries them and
	// left to the operator otherwise (never a blocker). Rocky/Alma/RHEL do
	// not package drbd-utils; Fedora does.
	BestEffort bool
	// Companion packages are only added when the profile resolved the
	// related split package (see resolvePackages). Ubuntu 24.04's
	// resource-agents-base lacks VirtualDomain/Filesystem, which live in
	// resource-agents-extra; Debian's monolithic package already has them.
	Companion bool
}

func (s pkgSpec) alternatives() []string {
	if len(s.Alts) > 0 {
		return s.Alts
	}
	return []string{s.Logical}
}

// profile is the package set of a family. Names were checked against the
// real repositories on 2026-11-12 (see tasks/HA-DEPS-PLAN.md).
func profile(family string, storage bool) []pkgSpec {
	switch family {
	case FamilyApt:
		specs := []pkgSpec{
			{Logical: "pacemaker", Layer: LayerBase, Origin: OriginOfficial},
			{Logical: "corosync", Layer: LayerBase, Origin: OriginOfficial},
			{Logical: "pcs", Layer: LayerBase, Origin: OriginOfficial},
			// Debian 13 splits crm_mon out of pacemaker; without it `pcs
			// status` cannot run. Ubuntu ships it too.
			{Logical: "pacemaker-cli-utils", Layer: LayerBase, Origin: OriginOfficial},
			{Logical: "sbd", Layer: LayerFencing, Origin: OriginOfficial},
			{Logical: "fence-agents", Alts: packageAlternatives["fence-agents"], Layer: LayerFencing, Origin: OriginOfficial},
			{Logical: "resource-agents", Alts: packageAlternatives["resource-agents"], Layer: LayerBase, Origin: OriginOfficial},
			{Logical: "resource-agents-extra", Layer: LayerBase, Origin: OriginOfficial, Companion: true},
			{Logical: "drbd-utils", Layer: LayerStorage, Origin: OriginOfficial},
		}
		if storage {
			for _, name := range LinstorPackages {
				specs = append(specs, pkgSpec{Logical: name, Layer: LayerStorage, Origin: OriginThirdParty, Optional: true, Source: "LINBIT (packages.linbit.com)"})
			}
		}
		return specs
	case FamilyDnf:
		specs := []pkgSpec{
			{Logical: "pacemaker", Layer: LayerBase, Origin: OriginOfficial},
			{Logical: "corosync", Layer: LayerBase, Origin: OriginOfficial},
			{Logical: "pcs", Layer: LayerBase, Origin: OriginOfficial},
			// crm_mon lives in pacemaker-cli on Fedora/RHEL; pcs status
			// needs it just like on Debian.
			{Logical: "pacemaker-cli", Layer: LayerBase, Origin: OriginOfficial},
			{Logical: "sbd", Layer: LayerFencing, Origin: OriginOfficial},
			{Logical: "fence-agents-all", Layer: LayerFencing, Origin: OriginOfficial},
			{Logical: "resource-agents", Layer: LayerBase, Origin: OriginOfficial},
			{Logical: "drbd-utils", Layer: LayerStorage, Origin: OriginOfficial, BestEffort: true},
		}
		if storage {
			for _, name := range LinstorPackages {
				specs = append(specs, pkgSpec{Logical: name, Layer: LayerStorage, Origin: OriginManual, Optional: true, Informational: true})
			}
		}
		return specs
	case FamilyPacman:
		// On Arch the whole stack lives in the AUR (checked 2026-11-12):
		// each entry still gets resolved against `pacman -Si`, so a package
		// that moves to an official repo is installed by WebKVM.
		specs := []pkgSpec{
			{Logical: "corosync", Layer: LayerBase, Origin: OriginAUR},
			{Logical: "resource-agents", Layer: LayerBase, Origin: OriginAUR},
			{Logical: "fence-agents", Layer: LayerFencing, Origin: OriginAUR},
			{Logical: "sbd", Layer: LayerFencing, Origin: OriginAUR},
			{Logical: "pacemaker", Layer: LayerBase, Origin: OriginAUR},
			{Logical: "pcs", Layer: LayerBase, Origin: OriginAUR},
		}
		if storage {
			specs = append(specs,
				pkgSpec{Logical: "drbd-dkms", Layer: LayerStorage, Origin: OriginAUR, Optional: true},
				pkgSpec{Logical: "linstor-server", Layer: LayerStorage, Origin: OriginManual, Optional: true, Informational: true})
		}
		return specs
	}
	return nil
}

func specFor(family, logicalOrName string) (pkgSpec, bool) {
	for _, spec := range profile(family, true) {
		if spec.Logical == logicalOrName || containsAny(spec.alternatives(), logicalOrName) {
			return spec, true
		}
	}
	return pkgSpec{}, false
}

// resolvedPackage is a logical package mapped to the real name on this node.
type resolvedPackage struct {
	Package
	// Available is true when WebKVM can get it: installed, a repo
	// candidate, or not WebKVM's job (guided packages).
	Available bool
}

// resolvePackage maps a spec to the alternative that is installed or, failing
// that, the first with a candidate. Unavailable packages keep the logical
// name so the operator sees what the module asked for.
func (m *Manager) resolvePackage(ctx context.Context, pm packageManager, spec pkgSpec) resolvedPackage {
	build := func(name string) Package {
		p := Package{Name: name, Origin: spec.Origin, Layer: spec.Layer, Optional: spec.Optional, Source: spec.Source, Guided: spec.Origin == OriginAUR || spec.Origin == OriginManual}
		if name != spec.Logical {
			p.Logical = spec.Logical
		}
		return p
	}
	if spec.Informational {
		return resolvedPackage{Package: build(spec.Logical), Available: true}
	}
	alts := spec.alternatives()
	for _, name := range alts {
		if pm.installed(ctx, name) {
			p := build(name)
			p.Installed = true
			return resolvedPackage{Package: p, Available: true}
		}
	}
	for _, name := range alts {
		version, source, ok := pm.candidate(ctx, name)
		if !ok {
			continue
		}
		p := build(name)
		p.Candidate = version
		if pm.family() == FamilyPacman {
			if containsAny(officialPacmanRepos, source) {
				p.Origin, p.Guided, p.Source = OriginOfficial, false, source
			} else {
				p.Origin, p.Guided, p.Source = OriginThirdParty, true, source
			}
		}
		return resolvedPackage{Package: p, Available: true}
	}
	p := build(spec.Logical)
	if spec.BestEffort {
		p.Origin, p.Guided = OriginManual, true
		p.Source = "not in this distribution's repositories (vendor or third-party: ELRepo, LINBIT); only needed for DRBD"
		return resolvedPackage{Package: p, Available: true}
	}
	// Guided packages are the operator's to build: not finding them in the
	// repos is expected, not a blocker.
	return resolvedPackage{Package: p, Available: p.Guided}
}

func (m *Manager) resolvePackages(ctx context.Context, pm packageManager, storage bool) []resolvedPackage {
	if pm == nil {
		return nil
	}
	specs := profile(pm.family(), storage)
	out := make([]resolvedPackage, 0, len(specs))
	baseName := ""
	for _, spec := range specs {
		if spec.Companion {
			continue
		}
		resolved := m.resolvePackage(ctx, pm, spec)
		out = append(out, resolved)
		if spec.Logical == "resource-agents" {
			baseName = resolved.Name
		}
	}
	// Ubuntu 24.04+ split the agents: the curated base package lacks
	// VirtualDomain/Filesystem. Add the extra package only when the base
	// was chosen; Debian's monolithic resource-agents already has them.
	if baseName == "resource-agents-base" {
		for _, spec := range specs {
			if spec.Companion && spec.Logical == "resource-agents-extra" {
				out = append(out, m.resolvePackage(ctx, pm, spec))
			}
		}
	}
	return out
}

// missingNames lists the real names of what is not installed, leaving out
// informational entries (they cannot be verified).
func missingNames(packages []Package) []string {
	missing := []string{}
	for _, pkg := range packages {
		if !pkg.Installed && pkg.Origin != OriginManual {
			missing = append(missing, pkg.Name)
		}
	}
	return missing
}

// installable are the missing packages WebKVM itself may install.
func installable(packages []Package) []string {
	names := []string{}
	for _, pkg := range packages {
		if !pkg.Installed && !pkg.Guided && pkg.Origin != OriginManual {
			names = append(names, pkg.Name)
		}
	}
	return names
}

// guidedMissing are the missing packages the operator must handle.
func guidedMissing(packages []Package) []Package {
	out := []Package{}
	for _, pkg := range packages {
		if !pkg.Installed && pkg.Guided {
			out = append(out, pkg)
		}
	}
	return out
}

// verifyState reports installed / partial / partial_guided / not_installed
// for the local node from what the package manager says.
func (m *Manager) verifyState(ctx context.Context, storage bool) (string, []string) {
	return stateOf(m.resolvePackages(ctx, m.localPM(ctx), storage))
}

func stateOf(resolved []resolvedPackage) (string, []string) {
	packages := make([]Package, 0, len(resolved))
	for _, pkg := range resolved {
		packages = append(packages, pkg.Package)
	}
	return stateOfPackages(packages)
}

func stateOfPackages(packages []Package) (string, []string) {
	missing := missingNames(packages)
	countable, installedCount, officialMissing := 0, 0, 0
	for _, pkg := range packages {
		if pkg.Origin == OriginManual {
			continue
		}
		countable++
		if pkg.Installed {
			installedCount++
		} else if !pkg.Guided {
			officialMissing++
		}
	}
	switch {
	case len(missing) == 0:
		return StateInstalled, missing
	case officialMissing == 0:
		// Only operator-built packages are left: tracked until Verify
		// finds them.
		return StatePartialGuided, missing
	case installedCount == 0:
		return StateNotInstalled, missing
	}
	return StatePartial, missing
}

// RepoNeed is a repository the operator must enable before WebKVM can
// install the stack. WebKVM never enables repositories by itself.
type RepoNeed struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Note    string `json:"note,omitempty"`
}

// haRepoCandidates are the repository ids that carry the High Availability
// packages on RHEL-like distributions (checked 2026-11-12).
func haRepoCandidates(d Distro, arch string) []string {
	major := d.major()
	switch d.ID {
	case "rhel":
		if arch == "" {
			arch = "x86_64"
		}
		return []string{fmt.Sprintf("rhel-%d-for-%s-highavailability-rpms", major, arch)}
	case "rocky":
		if major == 8 {
			return []string{"ha"}
		}
		return []string{"highavailability"}
	case "almalinux", "centos":
		return []string{"highavailability"}
	case "ol":
		return []string{fmt.Sprintf("ol%d_addons", major)}
	}
	return []string{"highavailability"}
}

// needsHARepo is true for RHEL-like distributions (Fedora ships the stack in
// its main repository).
func needsHARepo(d Distro) bool {
	return d.Family == FamilyDnf && d.ID != "fedora"
}

func repoEnabled(repolist string, candidates []string) bool {
	for _, line := range strings.Split(repolist, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		id := strings.ToLower(fields[0])
		if strings.Contains(id, "highavailability") || containsAny(candidates, fields[0]) {
			return true
		}
	}
	return false
}

func repoNeed(d Distro, arch string) RepoNeed {
	id := haRepoCandidates(d, arch)[0]
	need := RepoNeed{ID: id, Command: "sudo dnf config-manager --set-enabled " + id,
		Note: "needs dnf-plugins-core; the repo id can differ on your release (list them with: dnf repolist --all)"}
	if d.ID == "rhel" {
		need.Command = "sudo subscription-manager repos --enable " + id
		need.Note = "requires a RHEL subscription that includes High Availability"
	}
	return need
}

// GuidedStep is a block of commands the operator runs by hand.
type GuidedStep struct {
	ID       string   `json:"id"`
	RunAs    string   `json:"run_as"` // root | user
	Commands []string `json:"commands"`
}

// aurSteps are the commands for operator-built packages. Order matters:
// corosync and the agents first, pacemaker and pcs after.
func aurSteps(names []string) GuidedStep {
	ordered := []string{}
	for _, spec := range profile(FamilyPacman, true) {
		// drbd-dkms depends on drbd-utils, which is not in the AUR; it is
		// handled by storageStep after the operator builds drbd-utils.
		if spec.Logical == "drbd-dkms" {
			continue
		}
		if containsAny(names, spec.Logical) {
			ordered = append(ordered, spec.Logical)
		}
	}
	for _, name := range names {
		if name == "drbd-dkms" {
			continue
		}
		if !containsAny(ordered, name) {
			ordered = append(ordered, name)
		}
	}
	list := strings.Join(ordered, " ")
	commands := []string{
		"# Run as a NORMAL user, never as root. Review every PKGBUILD before building.",
		"yay -S --needed " + list,
		"# or: paru -S --needed " + list,
		"# or build each one by hand, in this order:",
	}
	for _, name := range ordered {
		commands = append(commands, "git clone https://aur.archlinux.org/"+name+".git && cd "+name+" && less PKGBUILD && makepkg -si && cd ..")
	}
	return GuidedStep{ID: "aur_build", RunAs: "user", Commands: commands}
}

// storageNotes are the manual storage hints for dnf/pacman (never a third
// party repo, never curl | sh).
func storageStep(family string) GuidedStep {
	switch family {
	case FamilyDnf:
		return GuidedStep{ID: "storage_manual", RunAs: "root", Commands: []string{
			"# LINSTOR is not in the Fedora/RHEL repositories. WebKVM does not add third-party repos;",
			"# see https://linbit.com/drbd-user-guide/ for the vendor instructions and review them first."}}
	case FamilyPacman:
		return GuidedStep{ID: "storage_manual", RunAs: "user", Commands: []string{
			"# 1) Build drbd-utils first: it is not in the official repos or the AUR.",
			"git clone https://github.com/LINBIT/drbd-utils.git && cd drbd-utils && ./autogen.sh && ./configure && make && sudo make install",
			"# 2) Then the DRBD 9 kernel module from the AUR (depends on drbd-utils):",
			"yay -S --needed drbd-dkms",
			"# LINSTOR is not packaged for Arch; follow the vendor instructions if you need it."}}
	}
	return GuidedStep{}
}

// localPM is the package manager of this node. When the distribution cannot
// be identified it falls back to apt: that only ever reads dpkg, which on a
// foreign system simply reports nothing installed.
func (m *Manager) localPM(ctx context.Context) packageManager {
	family := m.currentDistro(ctx).Family
	pm := m.pmFor(family)
	if pm == nil {
		pm, family = aptManager{m}, FamilyApt
	}
	m.setFamily(family)
	return pm
}
