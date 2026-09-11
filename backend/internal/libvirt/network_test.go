package libvirt

import (
	"strings"
	"testing"

	"webkvm/internal/models"
)

// WebKVM uses ONE network model — real OS-level Linux bridges (vmbr0,
// vmbr1, …), never libvirt virtual networks. Every bridge is one of
// 3 kinds: "direct" (a real NIC enslaved, e.g. vmbr0), "isolated" (a
// bare bridge, no internet) or "nat" (a bare bridge with a masquerade
// rule). These tests pin that semantics (v2.5+).

// TestValidBridgeName: only safe, real-bridge identifiers are accepted for
// OS-level bridge creation. Virtual/NAT prefixes and unsafe names are
// rejected outright.
func TestValidBridgeName(t *testing.T) {
	for _, good := range []string{"vmbr0", "vmbr1", "vmbr2", "br0", "br1", "lan0", "my-bridge", "webkvm_lan"} {
		if !validBridgeName(good) {
			t.Errorf("validBridgeName(%q) = false, want true", good)
		}
	}
	for _, bad := range []string{
		"", "lo", "virbr0", "lxdbr0", "lxcbr1", "docker0", "br-foo",
		"has space", "tab\there", "../evil", "net/eth0", "a\\b",
	} {
		if validBridgeName(bad) {
			t.Errorf("validBridgeName(%q) = true, want false", bad)
		}
	}
}

// TestIsManagedBridge: only the host's primary/protected bridge names
// are managed (v2.5+ — NAT/isolated/direct networks the API itself
// created must be deletable; only the bridge holding the host's own
// LAN IP is off-limits).
func TestIsManagedBridge(t *testing.T) {
	for _, n := range []string{"vmbr0", "br0"} {
		if !IsManagedBridge(n) {
			t.Errorf("IsManagedBridge(%q) = false, want true (primary-bridge name)", n)
		}
	}
	if IsManagedBridge("some-made-up-bridge-name-xyz") {
		t.Error("IsManagedBridge(unrelated name) = true, want false")
	}
	// IsManagedNetwork must agree with IsManagedBridge (v2.5 unifies
	// the two concepts).
	if IsManagedNetwork("vmbr0") != IsManagedBridge("vmbr0") {
		t.Error("IsManagedNetwork and IsManagedBridge disagree on vmbr0")
	}
}

// TestCreateNetworkRefusesVirtualNames: creating a libvirt virtual network
// (or any non-bridge name) must fail before touching the OS — WebKVM only
// manages real Linux bridges.
func TestCreateNetworkRefusesVirtualNames(t *testing.T) {
	c := &Connector{}
	for _, name := range []string{"virbr0", "lxdbr0", "webkvm-bridge", "default", "", "has space"} {
		if _, err := c.CreateNetwork(models.CreateNetworkRequest{Name: name, Forward: "nat"}); err == nil {
			t.Errorf("CreateNetwork(%q) should be refused", name)
		}
	}
}

// TestCreateNetworkNATRequiresCIDR: kind=nat is a supported network
// kind (v2.5+, added real internet-egress support) — it must reach
// CIDR validation rather than being refused outright as "unsupported"
// (the old v2.4 behavior). Deliberately never supplies a valid CIDR,
// so this never reaches the real `ip link add` (no host mutation).
func TestCreateNetworkNATRequiresCIDR(t *testing.T) {
	c := &Connector{}
	_, err := c.CreateNetwork(models.CreateNetworkRequest{Name: "wk-test-nat-cidr-check", Kind: "nat"})
	if err == nil {
		t.Fatal("CreateNetwork(kind=nat, no cidr) should be refused")
	}
	if !strings.Contains(err.Error(), "cidr") {
		t.Errorf("expected a cidr-related error, got: %v", err)
	}
}

// TestCreateNetworkDirectRequiresInterface: kind=direct without an
// interface must be refused before any host mutation.
func TestCreateNetworkDirectRequiresInterface(t *testing.T) {
	c := &Connector{}
	_, err := c.CreateNetwork(models.CreateNetworkRequest{Name: "wk-test-direct-iface-check", Kind: "direct"})
	if err == nil {
		t.Fatal("CreateNetwork(kind=direct, no interface) should be refused")
	}
}

// TestResolveDHCPRange: an explicit caller-chosen range is honored
// (and validated against the CIDR); omitting both falls back to the
// pre-v2.5 auto-derived range.
func TestResolveDHCPRange(t *testing.T) {
	if s, e, err := resolveDHCPRange("10.20.0.0/24", "", ""); err != nil || s == "" || e == "" {
		t.Fatalf("auto-derive failed: %v (%q,%q)", err, s, e)
	}
	if s, e, err := resolveDHCPRange("10.20.0.0/24", "10.20.0.50", "10.20.0.60"); err != nil || s != "10.20.0.50" || e != "10.20.0.60" {
		t.Fatalf("explicit range not honored: %v (%q,%q)", err, s, e)
	}
	if _, _, err := resolveDHCPRange("10.20.0.0/24", "10.20.0.50", ""); err == nil {
		t.Error("one-sided range should be rejected")
	}
	if _, _, err := resolveDHCPRange("10.20.0.0/24", "10.20.1.50", "10.20.1.60"); err == nil {
		t.Error("out-of-subnet range should be rejected")
	}
	if _, _, err := resolveDHCPRange("10.20.0.0/24", "10.20.0.60", "10.20.0.50"); err == nil {
		t.Error("start > end should be rejected")
	}
}

// TestNetworkMutationsOnPrimaryBridge: the primary bridge (holding the
// host's own LAN IP) must never be deletable/stoppable via the API,
// regardless of which of the 3 kinds it's classified as.
func TestNetworkMutationsOnPrimaryBridge(t *testing.T) {
	c := &Connector{}
	if err := c.DeleteNetwork("vmbr0"); err == nil {
		t.Error("DeleteNetwork(vmbr0) should be refused")
	}
	if _, err := c.StopNetwork("vmbr0"); err == nil {
		t.Error("StopNetwork(vmbr0) should be refused")
	}
}

// TestListNetworksReturnsOnlyBridges: ListNetworks must contain ONLY
// Linux bridges (vmbrX/brX/…), never libvirt virtual networks, and
// every entry must resolve to one of the 3 supported kinds.
func TestListNetworksReturnsOnlyBridges(t *testing.T) {
	c := &Connector{}
	nets, err := c.ListNetworks()
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	validKinds := map[string]bool{"nat": true, "isolated": true, "direct": true}
	for _, n := range nets {
		if n.Bridge != n.Name {
			t.Errorf("network %q: Bridge (%q) != Name", n.Name, n.Bridge)
		}
		if !validKinds[n.Kind] {
			t.Errorf("network %q: unexpected Kind %q", n.Name, n.Kind)
		}
		if n.Forward != n.Kind {
			t.Errorf("network %q: deprecated Forward alias (%q) should mirror Kind (%q)", n.Name, n.Forward, n.Kind)
		}
		for _, p := range []string{"virbr", "lxdbr", "lxcbr", "docker", "br-"} {
			if strings.HasPrefix(n.Name, p) {
				t.Errorf("virtual bridge %q leaked into ListNetworks", n.Name)
			}
		}
	}
}
