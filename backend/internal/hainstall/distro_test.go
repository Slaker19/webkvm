package hainstall

import (
	"strings"
	"testing"
)

func TestParseDistroFamiliesAndTiers(t *testing.T) {
	cases := []struct {
		name    string
		release string
		family  string
		tier    string
		like    string
	}{
		{"ubuntu 26.04", "ID=ubuntu\nVERSION_ID=\"26.04\"\nPRETTY_NAME=\"Ubuntu 26.04.1 LTS\"\nID_LIKE=debian\n", FamilyApt, TierSupported, "debian"},
		{"ubuntu 22.04", "ID=ubuntu\nVERSION_ID=\"22.04\"\n", FamilyApt, TierSupported, ""},
		{"ubuntu 20.04 too old", "ID=ubuntu\nVERSION_ID=\"20.04\"\n", FamilyApt, TierUnsupported, ""},
		{"debian 12", "ID=debian\nVERSION_ID=\"12\"\n", FamilyApt, TierSupported, ""},
		{"debian 13", "ID=debian\nVERSION_ID=\"13\"\n", FamilyApt, TierSupported, ""},
		{"debian 11 too old", "ID=debian\nVERSION_ID=\"11\"\n", FamilyApt, TierUnsupported, ""},
		{"debian testing without version", "ID=debian\n", FamilyApt, TierUnsupported, ""},
		{"linux mint (ubuntu like)", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"22\"\n", FamilyApt, TierExperimental, "ubuntu debian"},
		{"pop (ubuntu like)", "ID=pop\nID_LIKE=\"ubuntu debian\"\n", FamilyApt, TierExperimental, "ubuntu debian"},
		{"fedora", "ID=fedora\nVERSION_ID=41\n", FamilyDnf, TierExperimental, ""},
		{"rocky", "ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID=\"9.4\"\n", FamilyDnf, TierExperimental, "rhel centos fedora"},
		{"almalinux", "ID=almalinux\nVERSION_ID=9.4\n", FamilyDnf, TierExperimental, ""},
		{"centos", "ID=centos\nVERSION_ID=9\n", FamilyDnf, TierExperimental, ""},
		{"rhel", "ID=rhel\nVERSION_ID=9.4\n", FamilyDnf, TierExperimental, ""},
		{"oracle (rhel like)", "ID=ol\nID_LIKE=fedora\n", FamilyDnf, TierExperimental, "fedora"},
		{"arch", "ID=arch\nPRETTY_NAME=\"Arch Linux\"\n", FamilyPacman, TierPartial, ""},
		{"endeavouros", "ID=endeavouros\nID_LIKE=arch\n", FamilyPacman, TierPartial, "arch"},
		{"cachyos", "ID=cachyos\nID_LIKE=\"arch\"\n", FamilyPacman, TierPartial, "arch"},
		{"manjaro", "ID=manjaro\nID_LIKE=arch\n", FamilyPacman, TierPartial, "arch"},
		{"alpine", "ID=alpine\nVERSION_ID=3.20\n", FamilyUnknown, TierUnsupported, ""},
		{"opensuse", "ID=opensuse-leap\nID_LIKE=\"suse opensuse\"\n", FamilyUnknown, TierUnsupported, "suse opensuse"},
		{"empty", "", FamilyUnknown, TierUnsupported, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := ParseDistro([]byte(tc.release))
			if d.Family != tc.family || d.Tier != tc.tier {
				t.Fatalf("family/tier = %s/%s, want %s/%s (%+v)", d.Family, d.Tier, tc.family, tc.tier, d)
			}
			if got := strings.Join(d.Like, " "); got != tc.like {
				t.Fatalf("like = %q, want %q", got, tc.like)
			}
			if d.Like == nil {
				t.Fatal("like must be an empty list, never null")
			}
		})
	}
}

func TestParseDistroFields(t *testing.T) {
	d := ParseDistro([]byte("NAME=\"Ubuntu\"\nID=Ubuntu\nVERSION_ID=\"26.04\"\nPRETTY_NAME=\"Ubuntu 26.04.1 LTS\"\n# comment\nID_LIKE='Debian'\n"))
	if d.ID != "ubuntu" || d.Version != "26.04" || d.Pretty != "Ubuntu 26.04.1 LTS" {
		t.Fatalf("unexpected distro: %+v", d)
	}
	if len(d.Like) != 1 || d.Like[0] != "debian" {
		t.Fatalf("like = %v", d.Like)
	}
}
