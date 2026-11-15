package hainstall

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const minDiskKB = 2 * 1024 * 1024 // 2 GiB

// watchdogDrivers are the modules that implement a watchdog, by kind.
// Anything else that looks like a watchdog driver counts as hardware.
func classifyWatchdog(devices []string, lsmod string) WatchdogInfo {
	info := WatchdogInfo{Type: "none", Devices: devices}
	for _, line := range strings.Split(lsmod, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		module := fields[0]
		switch {
		case module == "softdog":
			if info.Type == "none" {
				info.Type, info.Module = "softdog", module
			}
		case module == "i6300esb":
			info.Type, info.Module = "i6300esb", module
		default:
			if info.Type != "i6300esb" {
				info.Type, info.Module = "hardware", module
			}
		}
	}
	if info.Type == "none" && len(devices) > 0 {
		// A device with no listed module: built into the kernel.
		info.Type = "hardware"
	}
	return info
}

func (m *Manager) probeWatchdog(ctx context.Context) WatchdogInfo {
	devices := []string{}
	if out, err := m.run(ctx, probeTimeout, "sh", "-c", "ls -1 /dev/watchdog* 2>/dev/null"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				devices = append(devices, line)
			}
		}
	}
	lsmod, _ := m.run(ctx, probeTimeout, "sh", "-c", "lsmod | grep -E '^(softdog|i6300esb|iTCO_wdt|sp5100_tco|wdat_wdt|ipmi_watchdog|hpwdt|ib700wdt|w83627hf_wdt|[a-z0-9_]*_wdt|[a-z0-9_]*dog)( |$)'")
	return classifyWatchdog(devices, string(lsmod))
}

func (m *Manager) preflight(ctx context.Context, opts Options) (Preflight, []Package) {
	storage := opts.Linstor
	pf := Preflight{Checks: []Check{}, Watchdog: WatchdogInfo{Type: "none", Devices: []string{}}, Virt: "none", FencingHint: "unknown"}
	finish := func(id string, ok bool, severity, detail, hint string) {
		pf.Checks = append(pf.Checks, Check{ID: id, OK: ok, Severity: severity, Detail: detail, Hint: hint})
	}

	osRelease, osErr := m.run(ctx, probeTimeout, "cat", "/etc/os-release")
	distro := ParseDistro(osRelease)
	pf.Distro, pf.Version, pf.Family, pf.Tier = distro.ID, distro.Version, distro.Family, distro.Tier
	m.setFamily(distro.Family)
	pm := m.pmFor(distro.Family)
	switch {
	case osErr != nil || pf.Distro == "":
		finish("distro", false, "blocker", "cannot read /etc/os-release", "Only Debian, Ubuntu, Fedora/RHEL-like and Arch-like systems are handled.")
		pf.OK = false
		return pf, []Package{}
	case pm == nil:
		finish("distro", false, "blocker", pf.Distro+" "+pf.Version+" ("+distro.Tier+")",
			"No supported package manager (apt, dnf, pacman) was found for this distribution; install the packages by hand.")
		pf.OK = false
		return pf, []Package{}
	case distro.Tier == TierUnsupported:
		finish("distro", false, "blocker", pf.Distro+" "+pf.Version+" ("+distro.Tier+")",
			"Automatic install needs Debian 12+ or Ubuntu 22.04+; on other versions install the packages by hand.")
	default:
		finish("distro", true, "blocker", pf.Distro+" "+pf.Version, "")
		if distro.Tier == TierExperimental || distro.Tier == TierPartial {
			finish("experimental", opts.AcceptExperimental, "blocker", pf.Distro+" "+pf.Version+" ("+distro.Tier+")",
				"This distribution is not fully supported: send accept_experimental=true to install anyway.")
		}
	}

	uid, uidErr := m.run(ctx, probeTimeout, "id", "-u")
	root := uidErr == nil && strings.TrimSpace(string(uid)) == "0"
	finish("privileges", root, "blocker", strings.TrimSpace(string(uid)), "WebKVM must run as root to install packages.")

	holders := pm.lockHolders(ctx)
	finish("pkg_lock", holders == "", "blocker", holders, "Another "+pm.family()+" process holds the package lock. Wait and retry.")

	if needsHARepo(distro) {
		arch := ""
		if out, err := m.run(ctx, probeTimeout, "uname", "-m"); err == nil {
			arch = strings.TrimSpace(string(out))
		}
		repos, err := m.run(ctx, probeTimeout, "dnf", "-q", "repolist")
		need := repoNeed(distro, arch)
		switch {
		case err != nil:
			finish("ha_repo", false, "blocker", "cannot list repositories (dnf repolist failed)", "Enable the High Availability repository by hand: "+need.Command)
		case repoEnabled(string(repos), haRepoCandidates(distro, arch)):
			finish("ha_repo", true, "blocker", "High Availability repository enabled", "")
		default:
			finish("ha_repo", false, "blocker", "High Availability repository not enabled (expected "+need.ID+")", need.Command)
		}
	}

	if pm.family() == FamilyPacman {
		dbs, _ := m.run(ctx, probeTimeout, "sh", "-c", "ls -1 /var/lib/pacman/sync/*.db 2>/dev/null")
		old, _ := m.run(ctx, probeTimeout, "sh", "-c", "find /var/lib/pacman/sync -name '*.db' -mtime +14 2>/dev/null")
		switch {
		case strings.TrimSpace(string(dbs)) == "":
			finish("sync_db", false, "blocker", "pacman sync databases are missing",
				"Run 'sudo pacman -Syu' yourself first. WebKVM never runs a bare -Sy: partial upgrades are not supported on Arch.")
		case strings.TrimSpace(string(old)) != "":
			finish("sync_db", true, "warning", "pacman sync databases are older than 14 days",
				"Run 'sudo pacman -Syu' yourself before installing, so package lookups are current.")
		default:
			finish("sync_db", true, "warning", "pacman sync databases present", "")
		}
	}

	resolved := m.resolvePackages(ctx, pm, storage)
	packages := make([]Package, 0, len(resolved))
	unavailable := []string{}
	for _, pkg := range resolved {
		if !pkg.Available {
			name := pkg.Name
			if alts := alternatives(name); len(alts) > 1 {
				name += " (" + strings.Join(alts, " | ") + ")"
			}
			unavailable = append(unavailable, name)
		}
		packages = append(packages, pkg.Package)
	}
	if len(unavailable) == 0 {
		finish("repo_scope", true, "blocker", "", "")
	} else {
		hint := "No candidate in the enabled repositories. Check the distro repos."
		if storage && containsAny(unavailable, LinstorPackages...) {
			hint = "No candidate in the enabled repositories. Check the distro repos and the LINBIT repository (https://packages.linbit.com/)."
		}
		finish("repo_scope", false, "blocker", strings.Join(unavailable, ", "), hint)
	}

	extraHint := ""
	if storage && containsAny(unavailable, LinstorPackages...) {
		extraHint = " LINSTOR needs the LINBIT repo configured manually; WebKVM never pipes a remote script into a shell."
	}

	diskOut, _ := m.run(ctx, probeTimeout, "df", "-Pk", "/")
	available := int64(0)
	lines := strings.Split(strings.TrimSpace(string(diskOut)), "\n")
	if len(lines) > 1 {
		fields := strings.Fields(lines[len(lines)-1])
		if len(fields) >= 4 {
			available, _ = strconv.ParseInt(fields[3], 10, 64)
		}
	}
	finish("disk", available >= minDiskKB, "blocker", fmt.Sprintf("%d MiB free", available/1024), "At least 2 GiB free is required on /.")

	ntpOut, ntpErr := m.run(ctx, probeTimeout, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
	if ntpErr != nil {
		finish("time_sync", true, "warning", "cannot query timedatectl", "Verify time synchronization manually (chrony/systemd-timesyncd).")
	} else {
		finish("time_sync", strings.TrimSpace(string(ntpOut)) == "yes", "warning", strings.TrimSpace(string(ntpOut)), "Cluster nodes must agree on time.")
	}

	hostOut, hostErr := m.run(ctx, probeTimeout, "hostname", "-f")
	hostname := strings.TrimSpace(string(hostOut))
	if hostErr != nil || hostname == "" {
		// Minimal Fedora/Arch installs may not ship the hostname tool.
		if out, err := m.run(ctx, probeTimeout, "uname", "-n"); err == nil {
			hostname, hostErr = strings.TrimSpace(string(out)), nil
		}
	}
	hostOK := hostErr == nil && hostname != ""
	hostHint := "Set a fully qualified hostname before joining a cluster."
	if hostOK {
		if _, err := m.run(ctx, probeTimeout, "getent", "hosts", hostname); err != nil {
			hostOK = false
			hostHint = "The hostname does not resolve locally; add it to /etc/hosts."
		}
	}
	finish("hostname", hostOK, "blocker", hostname, hostHint)

	if _, err := m.run(ctx, probeTimeout, "modinfo", "drbd"); err == nil {
		finish("drbd_module", true, "warning", "drbd module available", "")
	} else {
		finish("drbd_module", false, "warning", "drbd kernel module not loaded/available",
			"Optional unless you plan to use DRBD/LINSTOR. Install the DRBD kernel module (linux-modules-extra, kmod-drbd or drbd-dkms) or build DRBD."+extraHint)
	}

	virtOut, _ := m.run(ctx, probeTimeout, "systemd-detect-virt")
	pf.Virt = strings.TrimSpace(string(virtOut))
	if pf.Virt == "" {
		pf.Virt = "none"
	}
	finish("virt", true, "warning", pf.Virt, "")
	pf.FencingHint = "physical"
	if pf.Virt != "none" {
		pf.FencingHint = "vm"
	}

	pf.Watchdog = m.probeWatchdog(ctx)
	switch pf.Watchdog.Type {
	case "hardware", "i6300esb":
		finish("watchdog", true, "warning", pf.Watchdog.Type+" "+strings.Join(pf.Watchdog.Devices, " "), "")
	case "softdog":
		finish("watchdog", false, "warning", "softdog "+strings.Join(pf.Watchdog.Devices, " "),
			"softdog is only a software watchdog: it does not protect against a hung kernel. Use it for tests only; add a real watchdog for production.")
	default:
		hint := "No watchdog device found. On a physical server enable the hardware watchdog (BIOS/IPMI) and load its driver."
		if pf.FencingHint == "vm" {
			hint = "No watchdog device found. Add a watchdog to the virtual machine (libvirt: <watchdog model='i6300esb' action='reset'/>), or fence through the hypervisor."
		}
		finish("watchdog", false, "warning", "none", hint)
	}

	pf.OK = true
	for _, check := range pf.Checks {
		if !check.OK && check.Severity == "blocker" {
			pf.OK = false
		}
	}
	return pf, packages
}

func isLinstorPackage(name string) bool { return containsAny(LinstorPackages, name) }

func containsAny(values []string, needles ...string) bool {
	for _, value := range values {
		for _, needle := range needles {
			if value == needle {
				return true
			}
		}
	}
	return false
}

// Preflight probes the local node without changing anything.
func (m *Manager) Preflight(ctx context.Context, opts Options) Preflight {
	pf, _ := m.preflight(ctx, opts.Normalized())
	return pf
}

// Plan probes the node and returns the packages the operator would install.
func (m *Manager) Plan(ctx context.Context, opts Options) Plan {
	opts = opts.Normalized()
	pf, packages := m.preflight(ctx, opts)
	distro := m.currentDistro(ctx)
	missing := []string{}
	for _, pkg := range packages {
		if !pkg.Installed && pkg.Origin != OriginManual {
			missing = append(missing, pkg.Name)
		}
	}
	plan := Plan{
		Preflight:      pf,
		Packages:       packages,
		Missing:        missing,
		Services:       append([]string(nil), Services...),
		Linstor:        opts.Linstor,
		AcceptRequired: pf.Tier == TierExperimental || pf.Tier == TierPartial,
		Fencing:        opts.Fencing,
		Family:         pf.Family,
		Tier:           pf.Tier,
		ReposNeeded:    []RepoNeed{},
		GuidedSteps:    []GuidedStep{},
	}
	toInstall := installable(packages)
	plan.CanInstall = knownFamily(pf.Family) && len(toInstall) > 0 && pf.Tier != TierUnsupported

	for _, check := range pf.Checks {
		switch {
		case check.ID == "ha_repo" && !check.OK:
			need := repoNeed(distro, "")
			plan.ReposNeeded = append(plan.ReposNeeded, need)
			commands := []string{need.Command}
			if distro.ID != "rhel" {
				// `dnf config-manager` is a plugin: minimal Rocky/Alma images do
				// not ship it (verified on Rocky 10: "No such command").
				commands = []string{"sudo dnf install -y dnf-plugins-core", need.Command}
			}
			plan.GuidedSteps = append(plan.GuidedSteps, GuidedStep{ID: "enable_ha_repo", RunAs: "root", Commands: commands})
		case check.ID == "sync_db" && (!check.OK || strings.Contains(check.Detail, "older")):
			plan.GuidedSteps = append(plan.GuidedSteps, GuidedStep{ID: "sync_db", RunAs: "root", Commands: []string{
				"# Full system upgrade first (never a bare -Sy):", "sudo pacman -Syu"}})
		}
	}
	if aur := guidedMissing(packages); len(aur) > 0 {
		names := []string{}
		for _, pkg := range aur {
			if pkg.Origin == OriginAUR {
				names = append(names, pkg.Name)
			}
		}
		if len(names) > 0 {
			plan.GuidedSteps = append(plan.GuidedSteps, aurSteps(names))
		}
	}
	if opts.Linstor && (pf.Family == FamilyDnf || pf.Family == FamilyPacman) {
		plan.GuidedSteps = append(plan.GuidedSteps, storageStep(pf.Family))
	}
	plan.Guided = len(plan.GuidedSteps) > 0

	// Top to bottom: what the install depends on (repository, full upgrade)
	// first, then the install, then the remaining guided steps.
	for _, step := range plan.GuidedSteps {
		if step.ID == "enable_ha_repo" || step.ID == "sync_db" {
			plan.ManualCommands = append(plan.ManualCommands, step.Commands...)
		}
	}
	if p := m.pmFor(pf.Family); p != nil {
		plan.ManualCommands = append(plan.ManualCommands, p.installCommands(toInstall)...)
	}
	for _, step := range plan.GuidedSteps {
		if step.ID != "enable_ha_repo" && step.ID != "sync_db" {
			plan.ManualCommands = append(plan.ManualCommands, step.Commands...)
		}
	}
	if plan.ManualCommands == nil {
		plan.ManualCommands = []string{}
	}
	if pf.Family == FamilyApt || pf.Family == FamilyDnf {
		plan.ManualCommands = append(plan.ManualCommands, manualDisableCommands(pf.Family)...)
	}
	return plan
}

func knownFamily(family string) bool {
	return family == FamilyApt || family == FamilyDnf || family == FamilyPacman
}
