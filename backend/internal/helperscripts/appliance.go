package helperscripts

import (
	"fmt"
	"strings"
)

// IDPrefix namespaces imported entries so they can never collide with a
// curated appliance ID, and so the deploy path can tell at a glance that
// an ID refers to third-party code.
const IDPrefix = "cs:"

// ApplianceID returns the catalog ID for a slug.
func ApplianceID(slug string) string { return IDPrefix + slug }

// SlugFromApplianceID returns the slug behind a prefixed ID.
func SlugFromApplianceID(id string) (string, bool) {
	slug, ok := strings.CutPrefix(id, IDPrefix)
	if !ok || !ValidSlug(slug) {
		return "", false
	}
	return slug, true
}

// Default sizing for launchers that compute their resources at runtime
// instead of declaring them. Deploying with zero vCPUs or zero disk
// would be rejected far downstream with a confusing error, so a sane
// floor is applied here where the reason is visible.
const (
	defaultVCPUs  = 1
	defaultRAMMB  = 512
	defaultDiskGB = 4
)

// baseImage maps the launcher's declared OS onto an Incus image alias.
//
// Upstream targets Proxmox LXC templates, whose naming does not match
// Incus's. The mapping is deliberately conservative: an unknown distro
// falls back to Debian rather than guessing an alias that would fail at
// create time with a remote-image error nobody can act on.
func baseImage(os, version string) string {
	os = strings.ToLower(strings.TrimSpace(os))
	version = strings.TrimSpace(version)

	switch os {
	case "debian":
		if version == "" {
			version = "13"
		}
		return "images:debian/" + version
	case "ubuntu":
		if version == "" {
			version = "24.04"
		}
		return "images:ubuntu/" + version
	case "alpine":
		if version == "" {
			version = "3.21"
		}
		return "images:alpine/" + version
	case "archlinux", "gentoo":
		// Rolling releases: Incus publishes a single unversioned alias.
		// Upstream declares a Proxmox template name here ("base",
		// "current"), and appending it produced "images:archlinux/base",
		// which does not exist — the deploy failed at create time with an
		// opaque remote-image error.
		return "images:" + os
	case "devuan":
		// Devuan is aliased by codename in Incus, never by number, so
		// the declared "5.0" has to be translated. An unknown release
		// falls back to the codename series rather than to a number that
		// is guaranteed not to resolve.
		switch version {
		case "4.0":
			return "images:devuan/chimaera"
		case "5.0", "":
			return "images:devuan/daedalus"
		case "6.0":
			return "images:devuan/excalibur"
		}
		return "images:devuan/daedalus"
	case "almalinux", "rockylinux", "fedora", "centos", "opensuse", "openeuler":
		// Straight pass-through. Note this can still name a release the
		// Incus image server does not publish (upstream targets Proxmox
		// templates, and the two catalogs track releases at their own
		// pace — openEuler is the current example). That is deliberately
		// not papered over with a pinned substitution, which would go
		// stale the same way: the create then fails with Incus's own
		// "requested image couldn't be found", and the operator can pick
		// another base image.
		if version == "" {
			return "images:" + os
		}
		return "images:" + os + "/" + version
	default:
		// Includes the case where the launcher picked its OS from an
		// interactive menu, which the parser deliberately leaves empty.
		return "images:debian/13"
	}
}

// Category maps upstream tags onto the App Store's own categories so an
// imported entry lands somewhere sensible in the existing UI.
func category(tags []string) string {
	for _, t := range tags {
		switch strings.ToLower(t) {
		case "adblock", "network", "networking", "dns", "vpn", "proxy":
			return "networking"
		case "monitoring", "metrics", "logging", "alerting":
			return "monitoring"
		case "media", "photos", "music", "video":
			return "media"
		case "docker", "ci-cd", "development", "automation":
			return "devops"
		case "security", "authentication", "password":
			return "security"
		case "os":
			return "cloud"
		}
	}
	return "app"
}

// ToAppliance renders a parsed script as a catalog entry.
//
// This is what lets a community script reuse the appliance deploy path
// unchanged — with its RBAC checks, quota accounting, pool validation
// and name-collision handling — instead of grazing past them through a
// second, thinner endpoint. The provision script is generated here and
// carried on the entry, so the deploy sees an ordinary container
// appliance that happens to have a script attached.
func ToAppliance(s Script) (Appliance, error) {
	provision, err := BuildProvisionScript(s)
	if err != nil {
		return Appliance{}, err
	}

	vcpus := s.VCPUs
	if vcpus <= 0 {
		vcpus = defaultVCPUs
	}
	ram := s.RAMMB
	if ram <= 0 {
		ram = defaultRAMMB
	}
	disk := s.DiskGB
	if disk <= 0 {
		disk = defaultDiskGB
	}

	desc := fmt.Sprintf("%s, installed by the community-scripts project.", s.Name)
	if s.NeedsGPU {
		desc += " Needs GPU passthrough for hardware acceleration."
	}

	return Appliance{
		ID:              ApplianceID(s.Slug),
		Name:            s.Name,
		Description:     desc,
		Category:        category(s.Tags),
		VCPUs:           vcpus,
		RAMMB:           ram,
		DiskGB:          disk,
		BaseImage:       baseImage(s.OS, s.Version),
		DefaultType:     "container",
		Port:            s.Port,
		WebPath:         s.WebPath,
		IsHelperScript:  true,
		ProvisionScript: provision,
		DocumentationURL: func() string {
			if s.Website != "" {
				return s.Website
			}
			return LauncherURL(s.Slug)
		}(),
	}, nil
}

// Appliance mirrors the fields of appliances.Appliance that an imported
// entry populates.
//
// It is declared here rather than imported so that this package does not
// depend on internal/appliances: the catalog store already reads and
// writes its own JSON file, and coupling the importer to it would let a
// parsing change reach persisted admin-owned state. The API layer copies
// between the two explicitly.
type Appliance struct {
	ID               string
	Name             string
	Description      string
	Category         string
	VCPUs            int
	RAMMB            int64
	DiskGB           int64
	BaseImage        string
	DefaultType      string
	Port             int
	WebPath          string
	IsHelperScript   bool
	ProvisionScript  string
	DocumentationURL string
}
