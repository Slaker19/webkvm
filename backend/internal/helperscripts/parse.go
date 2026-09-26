// Package helperscripts imports the community-scripts catalog (the
// project widely known as "Proxmox VE Helper-Scripts") into WebKVM's
// appliance catalog.
//
// # Why parsing, and why only the metadata
//
// Each application upstream is split in two files:
//
//	ct/<app>.sh        the *launcher*: declares APP, var_cpu, var_ram,
//	                   var_disk, var_os, var_version, then calls
//	                   build_container to create an LXC/Incus container.
//	install/<app>.sh   the *installer*: plain Debian/Alpine shell that
//	                   installs the application inside that container.
//
// Only the launcher is platform specific. The installers are byte for
// byte identical between the Proxmox and the Incus fork of the project,
// which is what makes this worth doing at all: WebKVM already creates
// Incus containers, so it only needs the declared sizing metadata, never
// the Proxmox container-creation logic.
//
// This file therefore parses ct/*.sh for *metadata only*. It never
// executes it, and deliberately does not attempt to translate shell into
// anything else — a launcher is ordinary bash with conditionals and
// menus, so a rewriting "translator" would be a shell compiler that
// breaks every time upstream refactors. Extracting declared values and
// reusing the untouched upstream installer is both smaller and far more
// robust.
package helperscripts

import (
	"regexp"
	"strconv"
	"strings"
)

// Script is one parsed launcher.
type Script struct {
	// Slug is the upstream file name without .sh ("adguard"), which is
	// also the key for install/<slug>-install.sh.
	Slug string `json:"slug"`
	// Name is the human title from APP= ("AdGuard Home").
	Name string `json:"name"`
	// Tags are upstream's own categories ("adblock", "media").
	Tags []string `json:"tags,omitempty"`
	// VCPUs, RAMMB and DiskGB are the recommended resources. Zero means
	// the launcher did not declare one (some compute it at runtime).
	VCPUs  int   `json:"vcpus,omitempty"`
	RAMMB  int64 `json:"ram_mb,omitempty"`
	DiskGB int64 `json:"disk_gb,omitempty"`
	// OS and Version identify the base image ("debian", "13").
	OS      string `json:"os,omitempty"`
	Version string `json:"version,omitempty"`
	// Port and WebPath come from the final "Access it using..." line.
	Port    int    `json:"port,omitempty"`
	WebPath string `json:"web_path,omitempty"`
	// Unprivileged mirrors var_unprivileged (1 = unprivileged).
	Unprivileged bool `json:"unprivileged,omitempty"`
	// NeedsGPU marks apps requesting hardware acceleration, which needs
	// a device passthrough WebKVM cannot infer on its own.
	NeedsGPU bool `json:"needs_gpu,omitempty"`
	// Source is the upstream URL the metadata came from.
	Source string `json:"source,omitempty"`
	// Website is the upstream project's own page, from the header.
	Website string `json:"website,omitempty"`
}

// Upstream variables are written with a default-expansion idiom:
//
//	var_cpu="${var_cpu:-2}"
//
// so the value we want is the text after ":-" and before the closing
// brace. A few scripts use a bare form (var_cpu="2"); both are matched.
var (
	reApp = regexp.MustCompile(`(?m)^APP="([^"]+)"`)
	// Captures the value of var_<name>, preferring the :- default.
	// Leading whitespace is allowed: a few launchers (adguard, bitmagnet)
	// declare sizes inside an if/else that picks Alpine vs Debian, so the
	// declarations are indented. Anchoring to column zero silently lost
	// the RAM and disk values for exactly those apps.
	reVar = regexp.MustCompile(`(?m)^[ \t]*var_([a-z0-9_]+)="?(?:\$\{var_[a-z0-9_]+:-([^}]*)\}|([^"\n]*))"?`)
	// The closing banner: echo -e "...http://${IP}:3000/admin${CL}".
	reAccess = regexp.MustCompile(`https?://\$\{IP\}(?::(\d+))?([^"$\s]*)`)
	// "# Source: https://adguard.com/ | Github: ..." in the header.
	reSource = regexp.MustCompile(`(?m)^#\s*Source:\s*(\S+)`)
	// Upstream marks withdrawn apps by printing an error and exiting
	// instead of building anything.
	reRetired = regexp.MustCompile(`no longer available|has been deprecated|is deprecated`)
)

// Parse extracts metadata from a ct/<slug>.sh launcher.
//
// ok is false when the script is not an installable application:
// upstream keeps retired entries in the tree as stubs that print an
// error and exit, and they must not reach the catalog as if they were
// installable. build_container is the reliable marker of a real one.
func Parse(slug, content string) (Script, bool) {
	if !strings.Contains(content, "build_container") || reRetired.MatchString(content) {
		return Script{}, false
	}

	s := Script{Slug: slug}
	if m := reApp.FindStringSubmatch(content); len(m) > 1 {
		s.Name = strings.TrimSpace(m[1])
	}
	if s.Name == "" {
		return Script{}, false
	}
	if m := reSource.FindStringSubmatch(content); len(m) > 1 {
		s.Website = strings.TrimSpace(m[1])
	}

	for _, m := range reVar.FindAllStringSubmatch(content, -1) {
		name := m[1]
		value := strings.TrimSpace(m[2])
		if value == "" {
			value = strings.TrimSpace(m[3])
		}
		if value == "" {
			continue
		}
		// Only the first declaration counts. A handful of launchers
		// re-declare sizes inside an if/else picking Alpine vs Debian;
		// taking the first keeps the value paired with the OS default
		// rather than silently mixing the two branches.
		switch name {
		case "cpu":
			if s.VCPUs == 0 {
				s.VCPUs = atoiSafe(value)
			}
		case "ram":
			if s.RAMMB == 0 {
				s.RAMMB = int64(atoiSafe(value))
			}
		case "disk":
			if s.DiskGB == 0 {
				s.DiskGB = int64(atoiSafe(value))
			}
		case "os":
			// Some launchers assign var_os from an interactive menu
			// ($(msg_menu ...)) when running on a real Proxmox host.
			// That is a command substitution, not a distro name, and
			// storing it would produce an unusable base image ref.
			if s.OS == "" && isPlainValue(value) {
				s.OS = value
			}
		case "version":
			if s.Version == "" && isPlainValue(value) {
				s.Version = value
			}
		case "tags":
			if len(s.Tags) == 0 {
				s.Tags = splitTags(value)
			}
		case "unprivileged":
			s.Unprivileged = value == "1"
		case "gpu":
			s.NeedsGPU = value == "yes"
		}
	}

	// A launcher that picks its OS from an interactive menu declares the
	// per-branch versions inside the if/else that follows. With no
	// literal var_os there is no way to tell which branch is the default,
	// and the first match is simply whichever came first in the file
	// (typically Alpine). Pairing that version with an unknown OS would
	// show "3.24" next to a Debian container, so both are dropped and the
	// importer falls back to its own default base image.
	if s.OS == "" {
		s.Version = ""
	}

	if m := reAccess.FindStringSubmatch(content); len(m) > 0 {
		s.Port = atoiSafe(m[1])
		// "http://${IP}" with no colon means the plain HTTP port. Leaving
		// it at zero would render as "no web UI" for apps that do have
		// one (bookstack, nextcloud).
		if s.Port == 0 {
			s.Port = 80
		}
		if p := strings.TrimSpace(m[2]); p != "" && p != "/" {
			s.WebPath = p
		}
	}

	return s, true
}

// plainValueRE matches a literal distro name or version — letters,
// digits, dots and dashes only. Anything with $, (, backticks or spaces
// is shell to be evaluated at runtime, not a value we can record.
var plainValueRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func isPlainValue(v string) bool { return plainValueRE.MatchString(v) }

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func splitTags(v string) []string {
	var out []string
	for _, t := range strings.Split(v, ";") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
