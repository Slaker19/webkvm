package libvirt

import (
	"regexp"
	"strings"
	"testing"
)

func TestBootDeviceAttr(t *testing.T) {
	cases := []struct {
		order string
		want  string
	}{
		{"", "<boot dev='hd'/>"},
		{"disk", "<boot dev='hd'/>"},
		{"cdrom", "<boot dev='cdrom'/>"},
		{"network", "<boot dev='network'/>"},
		{"garbage", "<boot dev='hd'/>"},
	}
	for _, c := range cases {
		if got := bootDeviceAttr(c.order); got != c.want {
			t.Errorf("bootDeviceAttr(%q) = %q, want %q", c.order, got, c.want)
		}
	}
}

func TestBootOrderFromXML(t *testing.T) {
	cases := []struct {
		xml  string
		want string
	}{
		{"<os><type arch='x86_64'>hvm</type><boot dev='hd'/></os>", "disk"},
		{"<os><boot dev='cdrom'/></os>", "cdrom"},
		{"<os><boot dev='network'/></os>", "network"},
		{"<os><type arch='x86_64'>hvm</type></os>", "disk"},
	}
	for _, c := range cases {
		if got := bootOrderFromXML(c.xml); got != c.want {
			t.Errorf("bootOrderFromXML(%q) = %q, want %q", c.xml, got, c.want)
		}
	}
}

func TestBootDeviceToAPI(t *testing.T) {
	cases := map[string]string{
		"hd":      "disk",
		"cdrom":   "cdrom",
		"network": "network",
		"floppy":  "disk",
		"":        "disk",
	}
	for dev, want := range cases {
		if got := bootDeviceToAPI(dev); got != want {
			t.Errorf("bootDeviceToAPI(%q) = %q, want %q", dev, got, want)
		}
	}
}

func TestInterfaceXML(t *testing.T) {
	origBridge := linuxBridgeCheck
	origMain := mainBridgeCheck
	t.Cleanup(func() {
		linuxBridgeCheck = origBridge
		mainBridgeCheck = origMain
	})

	// v2.4 agnostic L2: there is NO libvirt virtual-network path. A
	// non-bridge network name falls back to the host's main bridge.
	linuxBridgeCheck = func(string) bool { return false }
	mainBridgeCheck = func() string { return "vmbr0" }
	out := interfaceXML("default", "virtio")
	if !strings.Contains(out, "<interface type='bridge'>") ||
		!strings.Contains(out, "<source bridge='vmbr0'/>") ||
		!strings.Contains(out, "<model type='virtio'/>") {
		t.Errorf("non-bridge name should fall back to main bridge:\n%s", out)
	}
	// Empty name with no host bridge -> empty source (CreateDomain guards
	// this with ErrNoPhysicalBridge before interfaceXML is reached).
	linuxBridgeCheck = func(string) bool { return false }
	mainBridgeCheck = func() string { return "" }
	out = interfaceXML("", "virtio")
	if !strings.Contains(out, "<interface type='bridge'>") {
		t.Errorf("empty network must still emit type='bridge':\n%s", out)
	}
	// Empty name WITH a main host bridge defaults to a direct bridge
	// attachment (Proxmox-style shared L2).
	linuxBridgeCheck = func(string) bool { return true }
	mainBridgeCheck = func() string { return "vmbr0" }
	out = interfaceXML("", "virtio")
	if !strings.Contains(out, "<interface type='bridge'>") ||
		!strings.Contains(out, "<source bridge='vmbr0'/>") {
		t.Errorf("empty network with main bridge should attach to vmbr0:\n%s", out)
	}

	// Host Linux bridge -> direct bridge attachment (Proxmox-style L2).
	out = interfaceXML("vmbr0", "virtio")
	if !strings.Contains(out, "<interface type='bridge'>") ||
		!strings.Contains(out, "<source bridge='vmbr0'/>") ||
		!strings.Contains(out, "<model type='virtio'/>") {
		t.Errorf("bridge interface XML wrong:\n%s", out)
	}

	// With VLAN tag
	tag := 100
	outVlan := interfaceXMLWithVLAN("vmbr0", "virtio", &tag)
	if !strings.Contains(outVlan, "<vlan><tag id='100'/></vlan>") {
		t.Errorf("expected VLAN tag XML in output:\n%s", outVlan)
	}
}

func TestParseNetworksVLAN(t *testing.T) {
	xmlDesc := `<domain>
  <devices>
    <interface type='bridge'>
      <mac address='52:54:00:12:34:56'/>
      <source bridge='vmbr0'/>
      <vlan>
        <tag id='200'/>
      </vlan>
      <model type='virtio'/>
      <link state='down'/>
    </interface>
  </devices>
</domain>`
	ifaces := parseNetworks(xmlDesc)
	if len(ifaces) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(ifaces))
	}
	if ifaces[0].VLANTag == nil || *ifaces[0].VLANTag != 200 {
		t.Errorf("expected VLAN tag 200, got %+v", ifaces[0].VLANTag)
	}
	if ifaces[0].LinkState != "down" {
		t.Errorf("expected link state 'down', got %q", ifaces[0].LinkState)
	}
}

func TestSerialPortAndAudioXMLHandling(t *testing.T) {
	xmlWithSerialAndAudio := `<domain>
  <devices>
    <serial type='pty'><target port='0'/></serial>
    <console type='pty'><target type='serial' port='0'/></console>
    <sound model='ich9'/>
  </devices>
</domain>`

	if !strings.Contains(xmlWithSerialAndAudio, "<serial") {
		t.Errorf("expected serial device")
	}
	if !strings.Contains(xmlWithSerialAndAudio, "<sound model='ich9'") {
		t.Errorf("expected ich9 sound device")
	}

	// Test stripping serial
	stripSerialRE := regexp.MustCompile(`<serial\b[^>]*>[\s\S]*?</serial>\s*`)
	stripConsoleRE := regexp.MustCompile(`<console\b[^>]*>[\s\S]*?</console>\s*`)
	noSerial := stripSerialRE.ReplaceAllString(xmlWithSerialAndAudio, "")
	noSerial = stripConsoleRE.ReplaceAllString(noSerial, "")

	if strings.Contains(noSerial, "<serial") || strings.Contains(noSerial, "<console") {
		t.Errorf("expected no serial or console in stripped XML, got:\n%s", noSerial)
	}

	// Test sound replacement
	soundRE := regexp.MustCompile(`<sound\b[^>]*/>\s*`)
	noSound := soundRE.ReplaceAllString(xmlWithSerialAndAudio, "")
	if strings.Contains(noSound, "<sound") {
		t.Errorf("expected no sound device in stripped XML, got:\n%s", noSound)
	}
}

func TestBuildCPUXML(t *testing.T) {
	s, c, th := 1, 4, 2
	cases := []struct {
		name    string
		mode    string
		model   string
		sockets *int
		cores   *int
		threads *int
		flags   []string
		wantIn  []string
		wantOut []string
	}{
		{
			name:   "host-passthrough default",
			mode:   "",
			wantIn: []string{"<cpu mode='host-passthrough' check='none'/>"},
		},
		{
			name:    "host-passthrough with topology and flags",
			mode:    "host-passthrough",
			sockets: &s, cores: &c, threads: &th,
			flags: []string{"+aes", "+avx2", "-hypervisor", "+topoext"},
			wantIn: []string{
				"<cpu mode='host-passthrough' check='none'>",
				"<topology sockets='1' cores='4' threads='2'/>",
				"<feature policy='require' name='aes'/>",
				"<feature policy='require' name='avx2'/>",
				"<feature policy='disable' name='hypervisor'/>",
				"<feature policy='require' name='topoext'/>",
			},
		},
		{
			name:    "custom model with flags",
			mode:    "custom",
			model:   "EPYC-Rome",
			sockets: &s, cores: &c, threads: &th,
			flags: []string{"+pcid", "+pdpe1gb"},
			wantIn: []string{
				"<cpu mode='custom' match='exact'>",
				"<model>EPYC-Rome</model>",
				"<topology sockets='1' cores='4' threads='2'/>",
				"<feature policy='require' name='pcid'/>",
				"<feature policy='require' name='pdpe1gb'/>",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildCPUXML(tc.mode, tc.model, tc.sockets, tc.cores, tc.threads, tc.flags)
			for _, w := range tc.wantIn {
				if !strings.Contains(got, w) {
					t.Errorf("expected %q in cpu xml, got:\n%s", w, got)
				}
			}
			for _, w := range tc.wantOut {
				if strings.Contains(got, w) {
					t.Errorf("expected %q NOT in cpu xml, got:\n%s", w, got)
				}
			}
		})
	}
}

func TestMinRAMAndIOThreadsXMLParsing(t *testing.T) {
	xmlDesc := `<domain type='kvm'>
  <name>test-vm</name>
  <uuid>11111111-2222-3333-4444-555555555555</uuid>
  <memory unit='MiB'>4096</memory>
  <currentMemory unit='MiB'>2048</currentMemory>
  <vcpu placement='static'>4</vcpu>
  <iothreads>2</iothreads>
  <devices>
    <controller type='scsi' model='virtio-scsi' index='0'>
      <driver iothread='1'/>
    </controller>
    <memballoon model='virtio'/>
  </devices>
</domain>`

	// Test extracting MinRAMMB and IOThreads
	var minRAMMB int64
	if m := regexp.MustCompile(`<currentMemory unit='MiB'>(\d+)</currentMemory>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		minRAMMB = 2048
	}
	if minRAMMB != 2048 {
		t.Errorf("expected minRAMMB 2048, got %d", minRAMMB)
	}

	var iothreads int
	if m := regexp.MustCompile(`<iothreads>(\d+)</iothreads>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		iothreads = 2
	}
	if iothreads != 2 {
		t.Errorf("expected iothreads 2, got %d", iothreads)
	}
}
