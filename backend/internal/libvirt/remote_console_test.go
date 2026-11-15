package libvirt

import (
	"errors"
	"testing"
)

func TestParseGraphicsXML(t *testing.T) {
	const dom = `<domain><devices>
<graphics type='spice' port='5901' autoport='yes' listen='127.0.0.1'><listen type='address' address='127.0.0.1'/></graphics>
<graphics type='vnc' port='5907' autoport='yes' listen='127.0.0.1'><listen type='address' address='127.0.0.1'/></graphics>
</devices></domain>`
	g, err := ParseGraphicsXML(dom, "vnc")
	if err != nil || g.Port != 5907 || g.Type != "vnc" || g.Host != "127.0.0.1" {
		t.Fatalf("vnc: %+v %v", g, err)
	}
	if g, err := ParseGraphicsXML(dom, "spice"); err != nil || g.Port != 5901 {
		t.Fatalf("spice: %+v %v", g, err)
	}
	cases := map[string]error{
		`<graphics type='vnc' port='-1' autoport='yes'/>`:                                          ErrGraphicsNotRunning,
		`<graphics type='vnc' autoport='yes'/>`:                                                    ErrGraphicsNotRunning,
		`<graphics type='vnc' port='5900' listen='192.0.2.5'/>`:                                    ErrGraphicsNotTunnel,
		`<graphics type='vnc' port='5900'><listen type='address' address='192.0.2.5'/></graphics>`: ErrGraphicsNotTunnel,
		`<graphics type='vnc' port='5900'><listen type='socket' socket='/x'/></graphics>`:          ErrGraphicsNotTunnel,
	}
	for x, want := range cases {
		if _, err := ParseGraphicsXML("<domain>"+x+"</domain>", "vnc"); !errors.Is(err, want) {
			t.Errorf("%s: got %v want %v", x, err, want)
		}
	}
	if _, err := ParseGraphicsXML(`<domain><graphics type='vnc' port='5900' listen='0.0.0.0'/></domain>`, "vnc"); err != nil {
		t.Errorf("wildcard listen must be reachable via loopback: %v", err)
	}
	if _, err := ParseGraphicsXML(`<domain></domain>`, "vnc"); err == nil {
		t.Error("missing device must fail")
	}
}
