package hainstall

import (
	"strconv"
	"strings"
)

// Package-manager families.
const (
	FamilyApt     = "apt"
	FamilyDnf     = "dnf"
	FamilyPacman  = "pacman"
	FamilyUnknown = "unknown"
)

// Support tiers. Only TierSupported installs automatically today; the other
// tiers are detected so the UI can explain why the node is not eligible.
const (
	TierSupported    = "supported"
	TierExperimental = "experimental"
	TierPartial      = "partial" // part installs automatically, part is guided (Arch)
	TierGuided       = "guided"
	TierUnsupported  = "unsupported"
)

// Distro is the parsed /etc/os-release of the local node.
type Distro struct {
	ID      string   `json:"id"`
	Like    []string `json:"like"`
	Version string   `json:"version"`
	Pretty  string   `json:"pretty,omitempty"`
	Family  string   `json:"family"`
	Tier    string   `json:"tier"`
}

// ParseDistro classifies an os-release file into a package-manager family
// and a support tier.
func ParseDistro(data []byte) Distro {
	d := Distro{Like: []string{}}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"'`)
		switch key {
		case "ID":
			d.ID = strings.ToLower(value)
		case "VERSION_ID":
			d.Version = value
		case "PRETTY_NAME":
			d.Pretty = value
		case "ID_LIKE":
			d.Like = append(d.Like, strings.Fields(strings.ToLower(value))...)
		}
	}
	d.Family, d.Tier = classifyDistro(d)
	return d
}

func (d Distro) is(ids ...string) bool {
	for _, id := range ids {
		if d.ID == id {
			return true
		}
	}
	return false
}

func (d Distro) likes(ids ...string) bool {
	for _, like := range d.Like {
		for _, id := range ids {
			if like == id {
				return true
			}
		}
	}
	return false
}

func (d Distro) major() int {
	part, _, _ := strings.Cut(d.Version, ".")
	n, err := strconv.Atoi(part)
	if err != nil {
		return -1
	}
	return n
}

func classifyDistro(d Distro) (family, tier string) {
	switch {
	case d.is("ubuntu"):
		if d.major() >= 22 {
			return FamilyApt, TierSupported
		}
		return FamilyApt, TierUnsupported
	case d.is("debian"):
		if d.major() >= 12 {
			return FamilyApt, TierSupported
		}
		return FamilyApt, TierUnsupported
	case d.likes("ubuntu", "debian"):
		return FamilyApt, TierExperimental
	case d.is("fedora", "rhel", "centos", "rocky", "almalinux") || d.likes("fedora", "rhel"):
		return FamilyDnf, TierExperimental
	case d.is("arch", "manjaro") || d.likes("arch"):
		return FamilyPacman, TierPartial
	}
	return FamilyUnknown, TierUnsupported
}
