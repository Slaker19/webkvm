package libvirt

import (
	"strings"
	"testing"

	"webkvm/internal/models"
)

// v2.4: WebKVM uses ONE network model — real OS-level Linux bridges
// (vmbr0, vmbr1, …). libvirt virtual/NAT networks no longer exist. These
// tests pin the new semantics.

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

// TestIsManagedNetworkAllProtected: in the agnostic-L2 model every network
// is an OS-level host bridge, so the API must refuse to delete any of them.
func TestIsManagedNetworkAllProtected(t *testing.T) {
	for _, n := range []string{"vmbr0", "vmbr1", "br0", "anything"} {
		if !IsManagedNetwork(n) {
			t.Errorf("IsManagedNetwork(%q) = false, want true (all host bridges are OS-managed)", n)
		}
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

// TestCreateNetworkRefusesNatForward: forward=nat is meaningless now.
func TestCreateNetworkRefusesNatForward(t *testing.T) {
	c := &Connector{}
	if _, err := c.CreateNetwork(models.CreateNetworkRequest{Name: "vmbr1", Forward: "nat"}); err == nil {
		t.Errorf("CreateNetwork with forward=nat should be refused")
	}
}

// TestNetworkMutationsRefused: update/delete/start/stop of host bridges are
// OS-level concerns and must always be refused.
func TestNetworkMutationsRefused(t *testing.T) {
	c := &Connector{}
	if _, err := c.UpdateNetwork("vmbr0", models.UpdateNetworkRequest{}); err == nil {
		t.Error("UpdateNetwork should be refused")
	}
	if err := c.DeleteNetwork("vmbr0"); err == nil {
		t.Error("DeleteNetwork should be refused")
	}
	if _, err := c.StartNetwork("vmbr0"); err == nil {
		t.Error("StartNetwork should be refused")
	}
	if _, err := c.StopNetwork("vmbr0"); err == nil {
		t.Error("StopNetwork should be refused")
	}
}

// TestListNetworksReturnsOnlyBridges: ListNetworks must contain ONLY Linux
// bridges (vmbrX/brX/…), never libvirt virtual networks — verified against
// the host when bridges exist.
func TestListNetworksReturnsOnlyBridges(t *testing.T) {
	c := &Connector{}
	nets, err := c.ListNetworks()
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	for _, n := range nets {
		if n.Forward != "bridge" || n.Bridge != n.Name || !n.Protected {
			t.Errorf("network %q is not a protected host bridge: %+v", n.Name, n)
		}
		for _, p := range []string{"virbr", "lxdbr", "lxcbr", "docker", "br-"} {
			if strings.HasPrefix(n.Name, p) {
				t.Errorf("virtual bridge %q leaked into ListNetworks", n.Name)
			}
		}
	}
}