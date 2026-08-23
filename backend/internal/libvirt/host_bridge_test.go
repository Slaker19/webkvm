package libvirt

import (
	"sort"
	"testing"
)

func TestIsLinuxBridge(t *testing.T) {
	// We can't easily create a bridge in a test, but the host
	// running the test always has `lo` and the test process might
	// or might not have a real bridge available. Validate the
	// empty and "definitely not a bridge" cases deterministically.
	cases := []struct {
		name string
		want bool
	}{
		{"", false},
		{"definitely-not-a-real-iface-12345", false},
		{"/etc/passwd", false}, // path traversal: should never match
		{"lo", false},          // loopback has no bridge subdir
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isLinuxBridge(c.name); got != c.want {
				t.Errorf("isLinuxBridge(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

func TestListLinuxBridges(t *testing.T) {
	got := listLinuxBridges()
	// The result must be sorted (caller prints it in an error
	// message and we want a stable order).
	if !sort.StringsAreSorted(got) {
		t.Errorf("listLinuxBridges() = %v, want sorted", got)
	}
	// `lo` must never appear (it's not a bridge).
	for _, n := range got {
		if n == "lo" {
			t.Errorf("listLinuxBridges() contains %q, want it excluded", n)
		}
		// virbr* must never appear either.
		if len(n) >= 5 && n[:5] == "virbr" {
			t.Errorf("listLinuxBridges() contains %q, want it excluded", n)
		}
	}
	// Every entry must pass isLinuxBridge (otherwise the caller
	// would get a self-contradicting hint).
	for _, n := range got {
		if !isLinuxBridge(n) {
			t.Errorf("listLinuxBridges() returned %q but isLinuxBridge(%q) is false", n, n)
		}
	}
}
