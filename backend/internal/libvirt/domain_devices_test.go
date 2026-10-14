package libvirt

import (
	"reflect"
	"testing"
)

func TestExtractAllMACs(t *testing.T) {
	xml := `<domain type='kvm'>
  <devices>
    <interface type='network'>
      <mac address='52:54:00:12:34:56'/>
      <source network='default'/>
    </interface>
    <interface type='bridge'>
      <mac address='52:54:00:ab:cd:ef'/>
      <source bridge='br0'/>
    </interface>
  </devices>
</domain>`

	got := extractAllMACs(xml)
	want := []string{"52:54:00:12:34:56", "52:54:00:ab:cd:ef"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractAllMACs() = %v, want %v", got, want)
	}

	emptyGot := extractAllMACs("<domain><devices></devices></domain>")
	if len(emptyGot) != 0 {
		t.Fatalf("extractAllMACs() expected empty, got %v", emptyGot)
	}
}

func TestFindInterfaceBlockByMAC(t *testing.T) {
	xml := `<domain type='kvm'>
  <devices>
    <interface type='network'>
      <mac address='52:54:00:11:11:11'/>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
    <interface type='bridge'>
      <mac address='52:54:00:22:22:22'/>
      <source bridge='br0'/>
      <model type='e1000'/>
    </interface>
  </devices>
</domain>`

	b1 := findInterfaceBlockByMAC(xml, "52:54:00:11:11:11")
	if b1 == "" {
		t.Fatalf("expected to find interface with mac 52:54:00:11:11:11")
	}
	if !reflect.DeepEqual(extractAllMACs(b1), []string{"52:54:00:11:11:11"}) {
		t.Fatalf("block 1 contained unexpected MACs: %v", extractAllMACs(b1))
	}

	b2 := findInterfaceBlockByMAC(xml, "52:54:00:22:22:22")
	if b2 == "" {
		t.Fatalf("expected to find interface with mac 52:54:00:22:22:22")
	}
	if !reflect.DeepEqual(extractAllMACs(b2), []string{"52:54:00:22:22:22"}) {
		t.Fatalf("block 2 contained unexpected MACs: %v", extractAllMACs(b2))
	}

	missing := findInterfaceBlockByMAC(xml, "52:54:00:33:33:33")
	if missing != "" {
		t.Fatalf("expected empty for missing MAC, got %q", missing)
	}
}

func TestExtractTagAttr(t *testing.T) {
	cases := []struct {
		xml      string
		attr     string
		expected string
	}{
		{xml: `<source file='/var/lib/webkvm/disk.qcow2'/>`, attr: "file", expected: "/var/lib/webkvm/disk.qcow2"},
		{xml: `<source file="/var/lib/webkvm/disk.qcow2"/>`, attr: "file", expected: "/var/lib/webkvm/disk.qcow2"},
		{xml: `<target dev='vda' bus='virtio'/>`, attr: "dev", expected: "vda"},
		{xml: `<target dev="vda" bus="virtio"/>`, attr: "bus", expected: "virtio"},
		{xml: `<target dev='vda'/>`, attr: "bus", expected: ""},
		{xml: ``, attr: "file", expected: ""},
	}

	for _, c := range cases {
		got := extractTagAttr(c.xml, c.attr)
		if got != c.expected {
			t.Errorf("extractTagAttr(%q, %q) = %q, want %q", c.xml, c.attr, got, c.expected)
		}
	}
}
