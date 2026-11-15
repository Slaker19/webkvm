package libvirt

import "testing"

func TestMatchesDiskChainFile(t *testing.T) {
	cases := []struct {
		name, base, stem string
		want             bool
	}{
		{"vm.qcow2", "vm.1794", "vm", true},
		{"vm.1794", "vm.1794", "vm", true},
		{"vm.s1", "vm.1794", "vm", true},
		{"vm2.qcow2", "vm.1794", "vm", false},
		{"other.qcow2", "vm.qcow2", "vm", false},
		{"vmdata.qcow2", "vm.qcow2", "vm", false},
	}
	for _, tc := range cases {
		if got := matchesDiskChainFile(tc.name, tc.base, tc.stem); got != tc.want {
			t.Errorf("matchesDiskChainFile(%q,%q,%q) = %v, want %v", tc.name, tc.base, tc.stem, got, tc.want)
		}
	}
}
