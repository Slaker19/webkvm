package helperscripts

import (
	"strings"
	"testing"
)

func TestApplianceIDRoundTrip(t *testing.T) {
	id := ApplianceID("adguard")
	if !strings.HasPrefix(id, IDPrefix) {
		t.Fatalf("ID = %q, want the %q namespace", id, IDPrefix)
	}
	slug, ok := SlugFromApplianceID(id)
	if !ok || slug != "adguard" {
		t.Errorf("round trip gave (%q, %v)", slug, ok)
	}

	// A plain appliance ID must not be mistaken for an imported one, or a
	// curated entry could be deployed through the third-party path.
	if _, ok := SlugFromApplianceID("wordpress"); ok {
		t.Error("an unprefixed ID must not resolve to a community script")
	}
	// The prefix must not become a way to smuggle a path into a URL.
	if _, ok := SlugFromApplianceID(IDPrefix + "../../etc/passwd"); ok {
		t.Error("a traversal slug must be rejected even with the prefix")
	}
}

func TestToAppliance_MapsMetadata(t *testing.T) {
	a, err := ToAppliance(Script{
		Slug: "jellyfin", Name: "Jellyfin", Tags: []string{"media"},
		VCPUs: 2, RAMMB: 2048, DiskGB: 16,
		OS: "ubuntu", Version: "24.04", Port: 8096,
		Website: "https://jellyfin.org/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "cs:jellyfin" || a.Name != "Jellyfin" {
		t.Errorf("ID/Name = %q/%q", a.ID, a.Name)
	}
	if a.BaseImage != "images:ubuntu/24.04" {
		t.Errorf("BaseImage = %q", a.BaseImage)
	}
	if a.Category != "media" {
		t.Errorf("Category = %q, want media", a.Category)
	}
	if a.DefaultType != "container" {
		t.Errorf("DefaultType = %q, want container", a.DefaultType)
	}
	if !a.IsHelperScript {
		t.Error("IsHelperScript must be set so the UI can flag third-party code")
	}
	if !strings.Contains(a.ProvisionScript, InstallURL("jellyfin")) {
		t.Error("the provision script must reference the upstream installer")
	}
	if a.DocumentationURL != "https://jellyfin.org/" {
		t.Errorf("DocumentationURL = %q", a.DocumentationURL)
	}
}

// Launchers that compute their sizing at runtime declare nothing. A zero
// would be rejected deep in the create path with an opaque error.
func TestToAppliance_AppliesDefaultsForMissingSizing(t *testing.T) {
	a, err := ToAppliance(Script{Slug: "x", Name: "X"})
	if err != nil {
		t.Fatal(err)
	}
	if a.VCPUs <= 0 || a.RAMMB <= 0 || a.DiskGB <= 0 {
		t.Errorf("sizing not defaulted: %d/%d/%d", a.VCPUs, a.RAMMB, a.DiskGB)
	}
	// With no declared OS the fallback must still be a usable alias.
	if !strings.HasPrefix(a.BaseImage, "images:") {
		t.Errorf("BaseImage = %q, want an Incus alias", a.BaseImage)
	}
}

func TestBaseImage(t *testing.T) {
	cases := map[[2]string]string{
		{"debian", "13"}:    "images:debian/13",
		{"ubuntu", "24.04"}: "images:ubuntu/24.04",
		{"alpine", "3.24"}:  "images:alpine/3.24",
		{"debian", ""}:      "images:debian/13",
		// Rolling releases have a single unversioned alias in Incus.
		// Upstream declares a Proxmox template name ("base", "current")
		// which, appended, yields an alias that does not exist and fails
		// the deploy at create time.
		{"archlinux", "base"}: "images:archlinux",
		{"gentoo", "current"}: "images:gentoo",
		// Devuan is only aliased by codename, never by release number.
		{"devuan", "5.0"}: "images:devuan/daedalus",
		{"devuan", "4.0"}: "images:devuan/chimaera",
		{"devuan", ""}:    "images:devuan/daedalus",
		// Numbered distros keep the straightforward mapping.
		{"almalinux", "10"}:    "images:almalinux/10",
		{"openeuler", "25.03"}: "images:openeuler/25.03",
		// An unknown or menu-derived OS must fall back rather than
		// produce an alias that fails at create time.
		{"", ""}:       "images:debian/13",
		{"plan9", "1"}: "images:debian/13",
	}
	for in, want := range cases {
		if got := baseImage(in[0], in[1]); got != want {
			t.Errorf("baseImage(%q,%q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestCategory(t *testing.T) {
	if got := category([]string{"adblock"}); got != "networking" {
		t.Errorf("adblock → %q, want networking", got)
	}
	if got := category([]string{"os"}); got != "cloud" {
		t.Errorf("os → %q, want cloud", got)
	}
	if got := category(nil); got != "app" {
		t.Errorf("no tags → %q, want app", got)
	}
}

func TestToAppliance_RejectsBadSlug(t *testing.T) {
	if _, err := ToAppliance(Script{Slug: "../x", Name: "X"}); err == nil {
		t.Fatal("a bad slug must not produce a deployable appliance")
	}
}
