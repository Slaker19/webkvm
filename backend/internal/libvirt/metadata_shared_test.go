package libvirt

import (
	"encoding/xml"
	"strings"
	"testing"
)

// The Shared flag is written as <shared>true</shared> inside
// <webkvm:meta> and read back by both GetVMMeta's xmlMetaRoot and the
// inline parser ListDomains uses.
func TestXMLMetaRoot_SharedRoundTrip(t *testing.T) {
	out, err := xml.Marshal(xmlMetaRoot{XMLNS: webkvmNamespace, Template: true, Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<shared>true</shared>") {
		t.Fatalf("falta <shared> en el XML: %s", out)
	}
	var back xmlMetaRoot
	if err := xml.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Template || !back.Shared {
		t.Fatalf("shared/template perdidos en el round-trip: %+v", back)
	}

	// Omitted when false, so existing domains' XML is unchanged.
	out, _ = xml.Marshal(xmlMetaRoot{XMLNS: webkvmNamespace, Template: true})
	if strings.Contains(string(out), "shared") {
		t.Fatalf("<shared> no debería escribirse si es false: %s", out)
	}
}
