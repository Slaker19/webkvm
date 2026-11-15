package libvirt

import (
	"errors"
	"fmt"
	"testing"

	"libvirt.org/go/libvirt"
)

func TestInterfaceXMLIsBridge(t *testing.T) {
	if !interfaceXMLIsBridge(`<interface type='bridge' name='vmbr0'><bridge/></interface>`) {
		t.Fatal("bridge not detected")
	}
	if interfaceXMLIsBridge(`<interface type="ethernet" name="eth0"/>`) {
		t.Fatal("ethernet taken as bridge")
	}
}

func TestConnectorIsRemote(t *testing.T) {
	for uri, want := range map[string]bool{
		"qemu:///system":                                 false,
		"qemu+ssh://u@node2.example/system":              true,
		"qemu+libssh://u@10.0.0.2:22/system?keyfile=/k":  true,
		"qemu+libssh2://u@10.0.0.2:22/system?keyfile=/k": true,
	} {
		if got := NewTestConnector(uri).isRemote(); got != want {
			t.Errorf("%s: isRemote=%v want %v", uri, got, want)
		}
	}
	if NewTestConnector("qemu:///system").bridgeExists("../etc") {
		t.Fatal("traversal name accepted")
	}
}

func TestNetworkFromLibvirtXML(t *testing.T) {
	n, ok := networkFromLibvirtXML(`<network><name>default</name><forward mode='nat'/><bridge name='virbr0' stp='on' delay='0'/><ip address='192.168.122.1' netmask='255.255.255.0'><dhcp><range start='192.168.122.2' end='192.168.122.254'/></dhcp></ip></network>`, true, true)
	if !ok || n.Name != "virbr0" || n.Bridge != "virbr0" || n.Kind != "nat" || n.CIDR != "192.168.122.1/24" || !n.DHCP || !n.Active || !n.Autostart {
		t.Fatalf("got %+v ok=%v", n, ok)
	}
	n, ok = networkFromLibvirtXML(`<network><name>iso</name><bridge name='virbr5'/></network>`, false, false)
	if !ok || n.Kind != "isolated" || n.Active {
		t.Fatalf("isolated: %+v", n)
	}
	if _, ok := networkFromLibvirtXML(`<network><name>x</name><forward mode='bridge'/></network>`, true, true); ok {
		t.Fatal("network without a <bridge name> must be skipped")
	}
}

func TestIsNoSupport(t *testing.T) {
	if isNoSupportErr(nil) || isNoSupportErr(errTest("boom")) {
		t.Fatal("false positive")
	}
	if !isNoSupportErr(libvirtNoSupportErr()) {
		t.Fatal("VIR_ERR_NO_SUPPORT not detected")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestIsDomainNotFound(t *testing.T) {
	for _, err := range []error{
		libvirt.Error{Code: libvirt.ERR_NO_DOMAIN},
		fmt.Errorf("%w: %w", ErrDomainNotFound, errors.New("x")),
		errors.New("domain not found: virError(Code=42, Domain=10, Message='Domain not found: no domain with matching name')"),
	} {
		if !IsDomainNotFound(err) {
			t.Errorf("%v must be domain-not-found", err)
		}
	}
	for _, err := range []error{nil, errors.New("connection reset"), libvirt.Error{Code: libvirt.ERR_NO_SUPPORT}} {
		if IsDomainNotFound(err) {
			t.Errorf("%v must not be domain-not-found", err)
		}
	}
}

func TestValidRemoteBridgeName(t *testing.T) {
	for _, n := range []string{"vmbr0", "br0", "br-lan", "bond0.100", "a", "abcdefghijklmno"} {
		if !ValidRemoteBridgeName(n) {
			t.Errorf("%q must be accepted", n)
		}
	}
	for _, n := range []string{"", ".", "..", "lo", "a..b", "abcdefghijklmnop", "vm br", "vm/br", "vm;br", "vmbr0\n", "ñandú"} {
		if ValidRemoteBridgeName(n) {
			t.Errorf("%q must be rejected", n)
		}
	}
}

func TestDomainLookupResultIsTyped(t *testing.T) {
	if ok, err := domainLookupResult(nil, libvirt.Error{Code: libvirt.ERR_NO_DOMAIN, Message: "x"}); ok || err != nil {
		t.Fatalf("no-domain: %v %v", ok, err)
	}
	boom := libvirt.Error{Code: libvirt.ERR_INTERNAL_ERROR, Message: "Domain not found (text only)"}
	if ok, err := domainLookupResult(nil, boom); ok || err == nil {
		t.Fatalf("other code must stay an error, even with matching text: %v %v", ok, err)
	}
	if ok, err := NewTestConnector("qemu:///system").HasDomain("x"); ok || err == nil {
		t.Fatalf("no connection must be an error: %v %v", ok, err)
	}
}
