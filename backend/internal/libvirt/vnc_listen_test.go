package libvirt

import (
	"os"
	"strings"
	"testing"
)

// VNC must never be generated on a wildcard address (audit #14): the console
// proxy reaches it over 127.0.0.1.
func TestGeneratedGraphicsListenOnLoopbackOnly(t *testing.T) {
	for _, f := range []string{"domain.go", "ova.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "0.0.0.0") {
			t.Errorf("%s still contains a 0.0.0.0 listen address", f)
		}
	}
	xml, err := ovfToLibvirtXML(`<Envelope xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1"><References>
<File ovf:id="f" ovf:href="disks/a.qcow2"/></References><VirtualSystem><Name>x</Name></VirtualSystem></Envelope>`,
		"/pool", map[string]bool{"a.qcow2": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(xml, "listen='127.0.0.1'") || strings.Contains(xml, "0.0.0.0") {
		t.Errorf("OVA-generated graphics not on loopback:\n%s", xml)
	}
}
