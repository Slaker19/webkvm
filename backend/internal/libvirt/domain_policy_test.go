package libvirt

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const testDisk = "/var/lib/libvirt/images/vm1.qcow2"

func untrusted() DomainPolicy {
	return DomainPolicy{AllowedDiskFiles: map[string]bool{testDisk: true}}
}

// generatedXML assembles the XML exactly the way CreateDomain does, using the
// real template and builders.
func generatedXML(t *testing.T, firmware, chipset, graphics string) string {
	t.Helper()
	machine := chipset
	if machine == "i440fx" {
		machine = "pc"
	}
	osXML, featuresXML := buildOSFeaturesXML("vm1", firmware, chipset, machine, bootDeviceAttr(""), false, true)
	controllerXML := `<controller type='pci' model='pcie-root'/>
    <controller type='pci' model='pcie-root-port' index='1'/>`
	devicesExtra := `<tpm model='tpm-crb'>
      <backend type='emulator' version='2.0'/>
    </tpm>
    <rng model='virtio'>
      <backend model='random'>/dev/urandom</backend>
    </rng>
    <input type='tablet' bus='usb'/>
    <memballoon model='virtio'/>
    <sound model='ich9'/>
    <watchdog model='i6300esb' action='reset'/>
    `
	serialXML := `<serial type='pty'>
      <target port='0'/>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>
    `
	graphicsXML := ""
	switch graphics {
	case "vnc":
		graphicsXML = `<graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'>
      <listen type='address' address='0.0.0.0'/>
    </graphics>`
	case "both":
		graphicsXML = `<channel type='spicevmc'>
      <target type='virtio' name='com.redhat.spice.0'/>
    </channel>
    <graphics type='spice' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
      <image compression='auto_glz'/>
    </graphics>
    <graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'>
      <listen type='address' address='0.0.0.0'/>
    </graphics>`
	}
	return buildDomainXML("vm1", "11111111-2222-3333-4444-555555555555", "vm1 [OSType=linux]",
		"<memory unit='MiB'>2048</memory>\n  <currentMemory unit='MiB'>2048</currentMemory>", 2, "", "",
		osXML, featuresXML, buildCPUXML("host-passthrough", "", nil, nil, nil, nil),
		controllerXML, diskDriverXMLAttrs("qcow2", nil, nil), testDisk, "vda", "virtio", "", "",
		interfaceXMLWithVLAN("vmbr0", "virtio", nil), serialXML, graphicsXML,
		"<video><model type='virtio' heads='1' vram='16384'/></video>", devicesExtra)
}

func TestSanitize_RoundTripsWebKVMGeneratedXML(t *testing.T) {
	for _, tc := range []struct{ fw, chipset, gfx string }{
		{"uefi", "q35", "both"},
		{"seabios", "i440fx", "vnc"},
		{"uefi", "q35", "none"},
	} {
		in := generatedXML(t, tc.fw, tc.chipset, tc.gfx)
		out, err := SanitizeDomainXML(in, untrusted())
		if err != nil {
			t.Fatalf("%v: generated XML rejected: %v\n%s", tc, err, in)
		}
		for _, must := range []string{"<name>vm1</name>", "source file='" + testDisk + "'", "<rng model='virtio'>", "<tpm", "type='pty'"} {
			if tc.gfx == "none" && must == "<tpm" {
				continue
			}
			if !strings.Contains(out, must) && !(must == "<tpm" && tc.chipset != "q35") {
				t.Errorf("%v: output lost %q:\n%s", tc, must, out)
			}
		}
		if strings.Contains(out, "0.0.0.0") {
			t.Errorf("%v: VNC still listens on 0.0.0.0:\n%s", tc, out)
		}
		if strings.Contains(out, "<uuid>") || strings.Contains(out, "<emulator>") {
			t.Errorf("uuid/emulator should be dropped:\n%s", out)
		}
		if tc.gfx == "both" && (!strings.Contains(out, "type='vnc'") || !strings.Contains(out, "type='spice'")) {
			t.Errorf("graphics not regenerated:\n%s", out)
		}
		if _, err := parseStrict([]byte(out)); err != nil {
			t.Errorf("sanitized output is not valid XML: %v", err)
		}
	}
}

// libvirt's own dump of a running/defined domain carries extra elements
// (seclabel, target dev, backingStore, source path of ptys...). They must be
// tolerated (stripped), not turned into import failures.
func TestSanitize_AcceptsLibvirtDump(t *testing.T) {
	in := `<domain type='kvm' id='7'>
  <name>vm1</name>
  <uuid>11111111-2222-3333-4444-555555555555</uuid>
  <metadata><x xmlns="urn:x">owner</x></metadata>
  <memory unit='KiB'>2097152</memory>
  <vcpu placement='static'>2</vcpu>
  <os><type arch='x86_64' machine='pc-q35-9.0'>hvm</type>
    <loader readonly='yes' type='pflash'>/usr/share/OVMF/OVMF_CODE_4M.fd</loader>
    <nvram template='/usr/share/OVMF/OVMF_VARS_4M.fd'>/var/lib/libvirt/qemu/nvram/vm1_VARS.fd</nvram>
    <boot dev='hd'/></os>
  <features><acpi/><apic/></features>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='` + testDisk + `' index='1'><seclabel model='dac' relabel='no'/></source>
      <backingStore/>
      <target dev='vda' bus='virtio'/>
      <alias name='virtio-disk0'/>
    </disk>
    <interface type='bridge'><mac address='52:54:00:aa:bb:cc'/><source bridge='vmbr0'/><target dev='vnet3'/><model type='virtio'/></interface>
    <serial type='pty'><source path='/dev/pts/3'/><target port='0'/></serial>
    <channel type='unix'><source mode='bind' path='/run/libvirt/qemu/channel/x'/><target type='virtio' name='org.qemu.guest_agent.0'/></channel>
    <graphics type='vnc' port='5900' autoport='yes' listen='0.0.0.0' passwd='x'><listen type='address' address='0.0.0.0'/></graphics>
    <audio id='1' type='none'/>
  </devices>
  <seclabel type='dynamic' model='apparmor' relabel='yes'/>
</domain>`
	out, err := SanitizeDomainXML(in, untrusted())
	if err != nil {
		t.Fatalf("libvirt dump rejected: %v", err)
	}
	for _, gone := range []string{"seclabel", "backingStore", "vnet3", "/dev/pts", "/run/libvirt", "metadata", "passwd", "0.0.0.0", "<emulator", "id='7'", "/var/lib/libvirt/qemu/nvram"} {
		if strings.Contains(out, gone) {
			t.Errorf("output still contains %q:\n%s", gone, out)
		}
	}
}

func TestSanitize_ModernUEFIDump(t *testing.T) {
	in := `<domain type='kvm'>
  <name>uefi-vm</name>
  <memory unit='KiB'>1048576</memory>
  <vcpu placement='static'>2</vcpu>
  <os firmware='efi'>
    <type arch='x86_64' machine='pc-q35-10.2'>hvm</type>
    <firmware>
      <feature enabled='no' name='enrolled-keys'/>
      <feature enabled='no' name='secure-boot'/>
    </firmware>
    <loader readonly='yes' type='pflash' format='raw'>/usr/share/OVMF/OVMF_CODE_4M.fd</loader>
    <nvram template='/usr/share/OVMF/OVMF_VARS_4M.fd' templateFormat='raw' format='raw'>/var/lib/libvirt/qemu/nvram/uefi-vm_VARS.fd</nvram>
    <boot dev='hd'/>
  </os>
  <features><acpi/><apic/></features>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='` + testDisk + `'/>
      <target dev='vda' bus='virtio'/>
    </disk>
  </devices>
</domain>`
	policy := DomainPolicy{
		AllowedDiskFiles: map[string]bool{
			testDisk: true,
		},
	}
	out, err := SanitizeDomainXML(in, policy)
	if err != nil {
		t.Fatalf("modern UEFI libvirt dump rejected: %v", err)
	}
	if !strings.Contains(out, "<firmware>") {
		t.Errorf("expected <firmware> in sanitized XML, got:\n%s", out)
	}
}

func TestSanitize_Negative(t *testing.T) {
	wrap := func(devices string) string {
		return `<domain type='kvm'><name>x</name><memory unit='KiB'>1</memory><vcpu>1</vcpu><os><type>hvm</type></os><devices>` + devices + `</devices></domain>`
	}
	goodDisk := `<disk type='file' device='disk'><source file='` + testDisk + `'/><target dev='vda' bus='virtio'/></disk>`
	cases := map[string]string{
		"qemu:commandline": `<domain type='kvm' xmlns:qemu='http://libvirt.org/schemas/domain/qemu/1.0'><name>x</name><devices>` + goodDisk + `</devices><qemu:commandline><qemu:arg value='-S'/></qemu:commandline></domain>`,
		"qemu undeclared":  `<domain type='kvm'><name>x</name><qemu:commandline/></domain>`,
		"xmlns attr":       `<domain type='kvm' xmlns='urn:evil'><name>x</name></domain>`,
		"doctype":          `<!DOCTYPE domain [<!ENTITY x SYSTEM "file:///etc/passwd">]><domain type='kvm'><name>&x;</name></domain>`,
		"processing instr": `<?evil foo?><domain type='kvm'><name>x</name></domain>`,
		"block disk":       wrap(`<disk type='block' device='disk'><source dev='/dev/sda'/><target dev='vda'/></disk>`),
		"dir disk":         wrap(`<disk type='dir' device='disk'><source dir='/'/><target dev='vda'/></disk>`),
		"network disk":     wrap(`<disk type='network' device='disk'><source protocol='nbd' name='x'><host name='evil'/></source><target dev='vda'/></disk>`),
		"floppy":           wrap(`<disk type='file' device='floppy'><source file='` + testDisk + `'/><target dev='fda'/></disk>`),
		"foreign source":   wrap(`<disk type='file' device='disk'><source file='/etc/shadow'/><target dev='vda'/></disk>`),
		"cdrom foreign":    wrap(`<disk type='file' device='cdrom'><source file='/opt/webkvm/jwt.key'/><target dev='sda'/></disk>`),
		"disk encryption":  wrap(`<disk type='file' device='disk'><source file='` + testDisk + `'/><encryption format='luks'><secret type='passphrase' uuid='x'/></encryption><target dev='vda'/></disk>`),
		"filesystem":       wrap(`<filesystem type='mount'><source dir='/'/><target dir='root'/></filesystem>`),
		"hostdev":          wrap(`<hostdev mode='subsystem' type='pci'><source><address domain='0' bus='1' slot='0' function='0'/></source></hostdev>`),
		"iface hostdev":    wrap(`<interface type='hostdev'><source><address type='pci'/></source></interface>`),
		"iface ethernet":   wrap(`<interface type='ethernet'><script path='/tmp/x'/></interface>`),
		"iface script":     wrap(`<interface type='bridge'><source bridge='b'/><script path='/tmp/x'/></interface>`),
		"iface rom":        wrap(`<interface type='bridge'><source bridge='b'/><rom file='/etc/shadow'/></interface>`),
		"serial file":      wrap(`<serial type='file'><source path='/etc/cron.d/x'/></serial>`),
		"serial dev":       wrap(`<serial type='dev'><source path='/dev/ttyS0'/></serial>`),
		"serial tcp":       wrap(`<serial type='tcp'><source mode='bind' host='0.0.0.0' service='4555'/></serial>`),
		"console unix":     wrap(`<console type='unix'><source mode='bind' path='/tmp/s'/></console>`),
		"channel file":     wrap(`<channel type='file'><source path='/tmp/x'/></channel>`),
		"rng egd":          wrap(`<rng model='virtio'><backend model='egd' type='tcp'/></rng>`),
		"rng file":         wrap(`<rng model='virtio'><backend model='random'>/etc/shadow</backend></rng>`),
		"tpm passthrough":  wrap(`<tpm model='tpm-tis'><backend type='passthrough'><device path='/dev/tpm0'/></backend></tpm>`),
		"audio file":       wrap(`<audio id='1' type='file' path='/tmp/x'/>`),
		"redirdev":         wrap(`<redirdev bus='usb' type='tcp'/>`),
		"smartcard":        wrap(`<smartcard mode='host'/>`),
		"shmem":            wrap(`<shmem name='x'/>`),
		"unknown device":   wrap(`<evil/>`),
		"kernel":           `<domain type='kvm'><name>x</name><os><type>hvm</type><kernel>/boot/vmlinuz</kernel><initrd>/boot/initrd</initrd><cmdline>init=/bin/sh</cmdline></os></domain>`,
		"os init":          `<domain type='kvm'><name>x</name><os><type>exe</type><init>/bin/sh</init></os></domain>`,
		"loader outside":   `<domain type='kvm'><name>x</name><os><type>hvm</type><loader type='pflash'>/etc/shadow</loader></os></domain>`,
		"nvram template":   `<domain type='kvm'><name>x</name><os><type>hvm</type><nvram template='/etc/shadow'/></os></domain>`,
		"sysinfo":          `<domain type='kvm'><name>x</name><sysinfo type='fwcfg'><entry name='x' file='/etc/shadow'/></sysinfo></domain>`,
		"unknown toplevel": `<domain type='kvm'><name>x</name><launchSecurity type='sev'/></domain>`,
		"bad type":         `<domain type='lxc'><name>x</name></domain>`,
		"not a domain":     `<evil type='kvm'/>`,
		"memory file":      `<domain type='kvm'><name>x</name><memoryBacking><source type='file'/><access mode='shared'/></memoryBacking><cpu><numa><cell id='0' memAccess='shared' file='/etc/x'/></numa></cpu></domain>`,
		"mixed text":       `<domain type='kvm'>evil<name>x</name></domain>`,
		"malformed":        `<domain type='kvm'><name>x</domain>`,
		"two roots":        `<domain type='kvm'/><domain type='kvm'/>`,
		"too big":          `<domain type='kvm'><name>` + strings.Repeat("a", 2<<20) + `</name></domain>`,
	}
	for name, in := range cases {
		if out, err := SanitizeDomainXML(in, untrusted()); err == nil {
			t.Errorf("%s: accepted:\n%s", name, out)
		}
	}
}

func TestSanitize_AcceptsOnlyExtractedDisks(t *testing.T) {
	in := `<domain type='kvm'><name>x</name><devices><disk type='file' device='disk'><source file='` + testDisk + `'/><target dev='vda'/></disk></devices></domain>`
	if _, err := SanitizeDomainXML(in, untrusted()); err != nil {
		t.Fatal(err)
	}
	if _, err := SanitizeDomainXML(in, DomainPolicy{}); err == nil {
		t.Fatal("disk accepted with empty allow set")
	}
}

func TestSanitizeSnapshotXML(t *testing.T) {
	good := `<domainsnapshot><name>s1</name><state>shutoff</state><creationTime>1</creationTime>
<memory snapshot='external' file='/etc/shadow'/>
<disks><disk name='vda' snapshot='external' type='file'><driver type='qcow2'/><source file='/etc/shadow'/></disk></disks>
<domain type='kvm'><name>x</name><qemu:commandline/></domain></domainsnapshot>`
	out, err := SanitizeSnapshotXML(good, "/pool/vm.s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"/etc/shadow", "<domain ", "<domain>", "memory", "qemu"} {
		if strings.Contains(out, gone) {
			t.Errorf("snapshot XML still has %q:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "/pool/vm.s1") {
		t.Errorf("overlay path not set:\n%s", out)
	}
	for name, bad := range map[string]string{
		"doctype": `<!DOCTYPE x [<!ENTITY e "x">]><domainsnapshot><name>s</name></domainsnapshot>`,
		"root":    `<domain type='kvm'/>`,
		"block":   `<domainsnapshot><name>s</name><disks><disk name='vda' type='block'><source dev='/dev/sda'/></disk></disks></domainsnapshot>`,
		"extra":   `<domainsnapshot><name>s</name><evil/></domainsnapshot>`,
	} {
		if _, err := SanitizeSnapshotXML(bad, "/pool/x"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// Whatever the validator accepts must not contain any of the dangerous
// constructs after re-serialisation.
func FuzzSanitizeDomainXML(f *testing.F) {
	f.Add(`<domain type='kvm'><name>x</name><devices><disk type='file' device='disk'><source file='` + testDisk + `'/></disk></devices></domain>`)
	f.Add(`<domain type='kvm'><name>x</name><hostdev/></domain>`)
	f.Fuzz(func(t *testing.T, in string) {
		out, err := SanitizeDomainXML(in, untrusted())
		if err != nil {
			return
		}
		for _, bad := range []string{"qemu:", "hostdev", "<filesystem", "type='block'", "<kernel", "DOCTYPE", "xmlns", "0.0.0.0"} {
			if strings.Contains(out, bad) {
				t.Fatalf("accepted output contains %q:\n%s\ninput:\n%s", bad, out, in)
			}
		}
	})
}

// DomainDefineXML must only ever be called from the defineDomain wrapper, so
// no code path can bypass the policy.
func TestDomainDefineXMLOnlyInWrapper(t *testing.T) {
	callRe := regexp.MustCompile(`\.DomainDefineXML(Flags)?\(`)
	root := filepath.Join("..", "..") // backend/
	var offenders []string
	count := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") || !callRe.MatchString(line) {
				continue
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			switch rel {
			case "internal/libvirt/domain_policy.go":
				count++
			case "cmd/migrate-disk-names/main.go":
				// Offline admin CLI that rewrites disk paths of EXISTING
				// domains from libvirt's own XML; it never ingests archives.
			default:
				offenders = append(offenders, rel+":"+strings.TrimSpace(line))
				_ = i
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("DomainDefineXML called outside the defineDomain wrapper:\n%s", strings.Join(offenders, "\n"))
	}
	if count != 1 {
		t.Fatalf("expected exactly one DomainDefineXML call in domain_policy.go, found %d", count)
	}
}

func TestReadAllLimited(t *testing.T) {
	if _, err := readAllLimited(strings.NewReader(strings.Repeat("a", 11)), 10); err == nil {
		t.Fatal("oversize accepted")
	}
	if b, err := readAllLimited(strings.NewReader(strings.Repeat("a", 10)), 10); err != nil || len(b) != 10 {
		t.Fatalf("exact size rejected: %v", err)
	}
	var sb strings.Builder
	if _, err := copyLimited(&sb, strings.NewReader("0123456789ab"), 10); err == nil {
		t.Fatal("copyLimited accepted oversize")
	}
	b := &diskBudget{remaining: 100}
	if err := b.take(60); err != nil {
		t.Fatal(err)
	}
	if err := b.take(60); err == nil {
		t.Fatal("budget exceeded without error")
	}
}
