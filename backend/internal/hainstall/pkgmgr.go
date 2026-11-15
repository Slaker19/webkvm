package hainstall

import (
	"context"
	"strings"
	"time"
)

// installOutcome is what a package manager reports back to the install state
// machine. Phase is "refresh" when the metadata refresh failed (nothing was
// installed) and "install" otherwise.
type installOutcome struct {
	Phase string
	Out   []byte
	Err   error
}

// packageManager hides the differences between apt, dnf and pacman. Every
// call goes through the Manager's CommandRunner, so tests never touch the
// real package manager.
type packageManager interface {
	family() string
	installed(ctx context.Context, name string) bool
	// candidate reports whether name can be installed from the enabled
	// repositories. source is the repository it comes from when known.
	candidate(ctx context.Context, name string) (version, source string, ok bool)
	lockHolders(ctx context.Context) string
	// install installs only packages WebKVM is allowed to install.
	install(ctx context.Context, names []string) installOutcome
	remove(ctx context.Context, names []string, purge bool) ([]byte, error)
	removeCommand(names []string, purge bool) string
	installCommands(names []string) []string
}

// pmFor returns the manager for a family, or nil when WebKVM has none.
func (m *Manager) pmFor(family string) packageManager {
	switch family {
	case FamilyApt:
		return aptManager{m}
	case FamilyDnf:
		return dnfManager{m}
	case FamilyPacman:
		return pacmanManager{m}
	}
	return nil
}

// currentDistro reads the local os-release.
func (m *Manager) currentDistro(ctx context.Context) Distro {
	out, err := m.run(ctx, probeTimeout, "cat", "/etc/os-release")
	if err != nil {
		return ParseDistro(nil)
	}
	return ParseDistro(out)
}

// ---------------------------------------------------------------- apt

type aptManager struct{ m *Manager }

func (aptManager) family() string { return FamilyApt }

func (a aptManager) installed(ctx context.Context, name string) bool {
	out, err := a.m.run(ctx, probeTimeout, "dpkg-query", "-W", "-f=${Status}", name)
	return err == nil && strings.Contains(string(out), "install ok installed")
}

func (a aptManager) candidate(ctx context.Context, name string) (string, string, bool) {
	out, err := a.m.run(ctx, probeTimeout, "apt-cache", "policy", name)
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Candidate:") {
			candidate := strings.TrimSpace(strings.TrimPrefix(line, "Candidate:"))
			if candidate == "" || candidate == "(none)" {
				return "", "", false
			}
			return candidate, "", true
		}
	}
	return "", "", false
}

func (a aptManager) lockHolders(ctx context.Context) string {
	out, _ := a.m.run(ctx, probeTimeout, "sh", "-c",
		`if command -v fuser >/dev/null 2>&1; then fuser /var/lib/dpkg/lock-frontend /var/lib/apt/lists/lock /var/lib/dpkg/lock 2>/dev/null; fi; true`)
	return strings.TrimSpace(string(out))
}

func (a aptManager) install(ctx context.Context, names []string) installOutcome {
	a.m.logf("running apt-get update")
	if out, err := a.m.aptWithRetry(ctx, "apt-get", "update", "-o", "Acquire::Retries=0"); err != nil {
		return installOutcome{Phase: "refresh", Out: out, Err: err}
	}
	a.m.logf("running apt-get install %s", strings.Join(names, " "))
	args := append([]string{"install", "-y", "--no-install-recommends"}, names...)
	out, err := a.m.aptWithRetry(ctx, "apt-get", args...)
	return installOutcome{Phase: "install", Out: out, Err: err}
}

func (a aptManager) remove(ctx context.Context, names []string, purge bool) ([]byte, error) {
	action := "remove"
	if purge {
		action = "purge"
	}
	return a.m.aptWithRetry(ctx, "apt-get", append([]string{action, "-y"}, names...)...)
}

func (aptManager) removeCommand(names []string, purge bool) string {
	action := "remove"
	if purge {
		action = "purge"
	}
	return "sudo DEBIAN_FRONTEND=noninteractive apt-get " + action + " -y " + strings.Join(names, " ")
}

func (aptManager) installCommands(names []string) []string {
	base := []string{}
	linstorOnly := []string{}
	for _, name := range names {
		if isLinstorPackage(name) {
			linstorOnly = append(linstorOnly, name)
		} else {
			base = append(base, name)
		}
	}
	commands := []string{}
	if len(base) > 0 {
		commands = append(commands, "sudo apt-get update", "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "+strings.Join(base, " "))
	}
	if len(linstorOnly) > 0 {
		commands = append(commands,
			"# LINSTOR needs the LINBIT repository first: https://packages.linbit.com/ (do not pipe curl into a shell)",
			"sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "+strings.Join(linstorOnly, " "))
	}
	return commands
}

func (m *Manager) apt(ctx context.Context, name string, args ...string) ([]byte, error) {
	full := append([]string{"DEBIAN_FRONTEND=noninteractive", name}, args...)
	return m.run(ctx, stepTimeout, "env", full...)
}

// aptWithRetry retries only classified network failures. A lock, a missing
// package or broken dependencies are deterministic: retrying them just adds
// noise and time.
func (m *Manager) aptWithRetry(ctx context.Context, name string, args ...string) ([]byte, error) {
	var out []byte
	var err error
	delay := 2 * time.Second
	for attempt := 1; attempt <= 3; attempt++ {
		out, err = m.apt(ctx, name, args...)
		if err == nil {
			return out, nil
		}
		if classify(string(out)+" "+err.Error()) != "network" || attempt == 3 {
			return out, err
		}
		m.logf("network error; retrying in %s (attempt %d/3)", delay, attempt+1)
		m.sleep(delay)
		delay *= 2
	}
	return out, err
}

// ---------------------------------------------------------------- dnf

type dnfManager struct{ m *Manager }

func (dnfManager) family() string { return FamilyDnf }

func (d dnfManager) installed(ctx context.Context, name string) bool {
	_, err := d.m.run(ctx, probeTimeout, "rpm", "-q", name)
	return err == nil
}

// candidate asks the enabled repositories. repoquery needs dnf-plugins-core
// on dnf4; when it is missing the plain list answers the same question.
func (d dnfManager) candidate(ctx context.Context, name string) (string, string, bool) {
	if out, err := d.m.run(ctx, probeTimeout, "dnf", "-q", "repoquery", "--available", "--latest-limit=1", name); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				return line, "", true
			}
		}
		return "", "", false
	}
	out, err := d.m.run(ctx, probeTimeout, "dnf", "-q", "list", "--available", name)
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.HasPrefix(fields[0], name+".") {
			return fields[1], fields[2], true
		}
	}
	return "", "", false
}

func (d dnfManager) lockHolders(ctx context.Context) string {
	out, _ := d.m.run(ctx, probeTimeout, "sh", "-c",
		`if command -v fuser >/dev/null 2>&1; then fuser /var/lib/rpm/.rpm.lock /var/cache/dnf/metadata_lock.pid /var/run/dnf.pid 2>/dev/null; fi; true`)
	return strings.TrimSpace(string(out))
}

func (d dnfManager) install(ctx context.Context, names []string) installOutcome {
	d.m.logf("running dnf install %s", strings.Join(names, " "))
	args := append([]string{"install", "-y", "--setopt=install_weak_deps=False"}, names...)
	out, err := d.m.run(ctx, stepTimeout, "dnf", args...)
	return installOutcome{Phase: "install", Out: out, Err: err}
}

func (d dnfManager) remove(ctx context.Context, names []string, _ bool) ([]byte, error) {
	return d.m.run(ctx, stepTimeout, "dnf", append([]string{"remove", "-y"}, names...)...)
}

func (dnfManager) removeCommand(names []string, _ bool) string {
	return "sudo dnf remove -y " + strings.Join(names, " ")
}

func (dnfManager) installCommands(names []string) []string {
	if len(names) == 0 {
		return []string{}
	}
	return []string{"sudo dnf install -y --setopt=install_weak_deps=False " + strings.Join(names, " ")}
}

// ------------------------------------------------------------- pacman

type pacmanManager struct{ m *Manager }

func (pacmanManager) family() string { return FamilyPacman }

func (p pacmanManager) installed(ctx context.Context, name string) bool {
	_, err := p.m.run(ctx, probeTimeout, "pacman", "-Q", name)
	return err == nil
}

// candidate only answers for the synced repositories (pacman -Si). It never
// refreshes the database: on Arch a bare -Sy is a partial upgrade.
func (p pacmanManager) candidate(ctx context.Context, name string) (string, string, bool) {
	out, err := p.m.run(ctx, probeTimeout, "pacman", "-Si", name)
	if err != nil {
		return "", "", false
	}
	version, repo := "", ""
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Repository":
			repo = strings.TrimSpace(value)
		case "Version":
			version = strings.TrimSpace(value)
		}
	}
	if repo == "" {
		return "", "", false
	}
	return version, repo, true
}

func (p pacmanManager) lockHolders(ctx context.Context) string {
	out, _ := p.m.run(ctx, probeTimeout, "sh", "-c", `if [ -e /var/lib/pacman/db.lck ]; then echo /var/lib/pacman/db.lck; fi; true`)
	return strings.TrimSpace(string(out))
}

// install uses -S --needed only. Never -Sy, never -u: the operator decides
// when to upgrade the whole system.
func (p pacmanManager) install(ctx context.Context, names []string) installOutcome {
	p.m.logf("running pacman -S --needed --noconfirm %s", strings.Join(names, " "))
	args := append([]string{"-S", "--needed", "--noconfirm"}, names...)
	out, err := p.m.run(ctx, stepTimeout, "pacman", args...)
	return installOutcome{Phase: "install", Out: out, Err: err}
}

func (p pacmanManager) remove(ctx context.Context, names []string, purge bool) ([]byte, error) {
	flag := "-R"
	if purge {
		flag = "-Rn"
	}
	return p.m.run(ctx, stepTimeout, "pacman", append([]string{flag, "--noconfirm"}, names...)...)
}

func (pacmanManager) removeCommand(names []string, purge bool) string {
	flag := "-R"
	if purge {
		flag = "-Rn"
	}
	return "sudo pacman " + flag + " " + strings.Join(names, " ")
}

func (pacmanManager) installCommands(names []string) []string {
	if len(names) == 0 {
		return []string{}
	}
	return []string{"sudo pacman -S --needed " + strings.Join(names, " ")}
}
