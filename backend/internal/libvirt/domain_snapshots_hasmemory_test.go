package libvirt

import "testing"

func TestSnapshotHasMemory(t *testing.T) {
	cases := []struct {
		name string
		xml  string
		want bool
	}{
		{"no memory element", "<domainsnapshot><state>shutoff</state></domainsnapshot>", false},
		{"disk-only reports snapshot=no", "<domainsnapshot><state>disk-snapshot</state>\n  <memory snapshot='no'/>\n</domainsnapshot>", false},
		{"double-quoted no", `<domainsnapshot><memory snapshot="no"/></domainsnapshot>`, false},
		{"internal", "<domainsnapshot><memory snapshot='internal'/></domainsnapshot>", true},
		{"external with file", "<domainsnapshot><memory snapshot='external' file='/x.mem'/></domainsnapshot>", true},
		{"double-quoted internal", `<domainsnapshot><memory snapshot="internal"/></domainsnapshot>`, true},
	}
	for _, c := range cases {
		if got := snapshotHasMemory(c.xml); got != c.want {
			t.Errorf("%s: snapshotHasMemory = %v, want %v", c.name, got, c.want)
		}
	}
}
