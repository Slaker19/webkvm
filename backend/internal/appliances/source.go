package appliances

import (
	"fmt"
	"net/url"
	"strings"
)

// officialHosts is the whitelist of domains from which an appliance disk
// image may be downloaded. Only these are accepted when an admin creates
// or edits an appliance, so a template can never be pointed at an
// arbitrary (possibly malicious) source. All entries are official project
// mirrors / release channels.
var officialHosts = map[string]bool{
	"cloud-images.ubuntu.com":     true,
	"cloud.debian.org":            true,
	"dl.rockylinux.org":           true,
	"cloud.centos.org":            true,
	"download.fedoraproject.org":  true,
	"dl.fedoraproject.org":        true,
	"geo.mirror.pkgbuild.com":     true,
	"download.freebsd.org":        true,
	"download.opensuse.org":       true,
	"dl-cdn.alpinelinux.org":      true,
	"mirror.opnsense.org":         true,
	"mirrors.bfsu.edu.cn":         true, // official OPNsense mirror
	"downloads.openwrt.org":       true,
	"github.com":                  true, // Home Assistant, VyOS nightly releases
	"objects.githubusercontent.com": true, // GitHub release asset host
	"downloads.vyos.io":           true,
	"images.linuxcontainers.org":  true,
	"nyifiles.pfsense.org":        true, // pfSense CE mirror
	"downloads.ipfire.org":        true, // IPFire releases
	"download.truenas.com":       true, // TrueNAS SCALE releases
	"sourceforge.net":            true, // OpenMediaVault ISOs
}

// ValidateSourceURL ensures u is an https URL hosted on an official
// source domain. It returns an error otherwise.
func ValidateSourceURL(u string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("invalid URL: %v", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("URL must use https")
	}
	host := strings.ToLower(parsed.Hostname())
	// Allow subdomains of official hosts only for a few known cases;
	// otherwise require an exact host match.
	if officialHosts[host] {
		return nil
	}
	// github.com release asset redirection is fine, but only for the
	// Home Assistant operating-system repo.
	if host == "github.com" && strings.HasPrefix(parsed.Path, "/home-assistant/operating-system/") {
		return nil
	}
	return fmt.Errorf("URL host %q is not an official source", host)
}
