package libvirt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"webkvm/internal/hostcaps"
	"webkvm/internal/models"
)

const (
	nvramDir = "/var/lib/libvirt/qemu/nvram"
)

var (
	ovmfCode         string
	ovmfCodeSecboot  string
	ovmfVarsTemplate string
)

func init() {
	ovmfCode, ovmfCodeSecboot, ovmfVarsTemplate = resolveOVMFFiles()
}

// bootDeviceAttr maps the API boot order ("disk"|"cdrom"|"network") to the
// libvirt <boot dev='…'/> value. Empty/invalid keeps the disk default.
func bootDeviceAttr(order string) string {
	switch order {
	case "cdrom":
		return "<boot dev='cdrom'/>"
	case "network":
		return "<boot dev='network'/>"
	default:
		return "<boot dev='hd'/>"
	}
}

// bootDeviceToAPI maps a libvirt <boot dev='…'/> value back to the API
// boot order exposed to clients.
func bootDeviceToAPI(dev string) string {
	switch dev {
	case "cdrom":
		return "cdrom"
	case "network":
		return "network"
	default:
		return "disk"
	}
}

// bootOrderFromXML extracts the <boot dev='…'/> value from a domain XML
// descriptor (absent element → "disk").
func bootOrderFromXML(xmlDesc string) string {
	if m := regexp.MustCompile(`<boot dev='([^']*)'/>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		return bootDeviceToAPI(m[1])
	}
	return "disk"
}

func resolveOVMFFiles() (code, codeSecboot, varsTpl string) {
	candidates := []struct {
		code, codeSecboot, varsTpl string
	}{
		// Ubuntu 24.04+ / 26.04 / Debian 13
		{
			code:        "/usr/share/OVMF/OVMF_CODE_4M.fd",
			codeSecboot: "/usr/share/OVMF/OVMF_CODE_4M.secboot.fd",
			varsTpl:     "/usr/share/OVMF/OVMF_VARS_4M.fd",
		},
		// Arch Linux / edk2-ovmf
		{
			code:        "/usr/share/edk2-ovmf/x64/OVMF_CODE.4m.fd",
			codeSecboot: "/usr/share/edk2-ovmf/x64/OVMF_CODE.secboot.4m.fd",
			varsTpl:     "/usr/share/edk2-ovmf/x64/OVMF_VARS.4m.fd",
		},
		// Fedora / RHEL / CentOS Stream (standard edk2-ovmf)
		{
			code:        "/usr/share/OVMF/OVMF_CODE.fd",
			codeSecboot: "/usr/share/OVMF/OVMF_CODE.secboot.fd",
			varsTpl:     "/usr/share/OVMF/OVMF_VARS.fd",
		},
		// Fedora / RHEL alternative location
		{
			code:        "/usr/share/edk2/ovmf/OVMF_CODE.fd",
			codeSecboot: "/usr/share/edk2/ovmf/OVMF_CODE.secboot.fd",
			varsTpl:     "/usr/share/edk2/ovmf/OVMF_VARS.fd",
		},
		// openSUSE / SLES / generic fallback
		{
			code:        "/usr/share/qemu/ovmf-x86_64-code.bin",
			codeSecboot: "/usr/share/qemu/ovmf-x86_64-code.bin",
			varsTpl:     "/usr/share/qemu/ovmf-x86_64-vars.bin",
		},
	}
	for _, c := range candidates {
		if _, err := os.Stat(c.code); err == nil {
			if _, err := os.Stat(c.varsTpl); err == nil {
				return c.code, c.codeSecboot, c.varsTpl
			}
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c.code); err == nil {
			return c.code, c.codeSecboot, c.varsTpl
		}
	}
	// Fallback: return the modern path so libvirt reports a clear error if missing.
	return candidates[0].code, candidates[0].codeSecboot, candidates[0].varsTpl
}


// tpmBackendVersionRE reads the version back off an existing
// <tpm><backend type='emulator' version='...'/></tpm> — used both when
// reporting a domain's current TPM version and, in UpdateDomain, when only
// the version changes and the model/backend need regenerating together.
var tpmBackendVersionRE = regexp.MustCompile(`<backend\s+type=['"]emulator['"]\s+version=['"]([^'"]+)['"]`)

// tpmBlockRE/tpmBlockWithTrailingWSRE match a domain's whole <tpm>...</tpm>
// element, used both to replace it (version change) and to remove it
// (disable). MUST carry the (?s) flag: Go's regexp "." does not match a
// newline by default, and the real element libvirt reports always spans
// several lines (e.g. "<tpm model='tpm-tis'>\n  <backend .../>\n</tpm>") —
// confirmed live: without (?s) neither MatchString nor ReplaceAllString
// ever matched the real multi-line XML at all, so a version change or a
// "disable TPM" request silently did nothing.
var (
	tpmBlockRE               = regexp.MustCompile(`(?s)<tpm\b[^>]*>.*?</tpm>`)
	tpmBlockWithTrailingWSRE = regexp.MustCompile(`(?s)<tpm\b[^>]*>.*?</tpm>\s*`)
)

// watchdogI6300esbRE matches only OUR explicit watchdog device, never the
// <watchdog model='itco'> libvirt auto-injects into every q35 domain's XML
// regardless of request (the ICH9 southbridge's intrinsic TCO watchdog —
// confirmed live: it appears even on a domain that never asked for any
// watchdog at all). Two <watchdog> elements coexist fine in the same
// domain (confirmed live: libvirt accepts it and QEMU boots with both).
//
// Must match BOTH shapes the element can take: self-closing right after
// CreateDomain writes it fresh (<watchdog .../>), and open/close with an
// <address> child once libvirt assigns it a PCI address — confirmed live
// this happens on the very first define/start, even while the domain is
// shut off again afterward (the persistent config keeps the assigned
// address). (?s) so "." spans the newline before that child.
var watchdogI6300esbRE = regexp.MustCompile(`(?s)<watchdog\b[^>]*model=['"]i6300esb['"][^>]*(?:/>|>.*?</watchdog>)\s*`)

// tpmModelAndVersion maps the requested TPM version ("1.2"/"2.0", or nil
// for the historical default) to the libvirt <tpm model='...'> value and
// the <backend version='...'> it pairs with.
func tpmModelAndVersion(reqVersion *string) (model, version string) {
	if reqVersion != nil && *reqVersion == "1.2" {
		return "tpm-tis", "1.2"
	}
	return "tpm-crb", "2.0"
}

func resolveGraphicsType(requested string) string {
	gType := strings.ToLower(strings.TrimSpace(requested))
	caps := hostcaps.Get()
	if caps.Parsed && !caps.SPICESupported {
		if gType == "both" || gType == "spice" || gType == "" {
			return "vnc"
		}
	}
	if gType == "" {
		return "both"
	}
	return gType
}

// buildOSFeaturesXML renders the <os> and <features> blocks of a domain
// created by WebKVM (split out of CreateDomain so tests can assemble the real
// generated XML).
func buildOSFeaturesXML(name, firmware, chipset, machine, boot string, secureBoot, kvmHidden bool) (osXML, featuresXML string) {
	kvmHiddenXML := ""
	if kvmHidden {
		kvmHiddenXML = "\n    <kvm>\n      <hidden state='on'/>\n    </kvm>"
	}
	if firmware == "uefi" && chipset == "q35" {
		nvramPath := nvramDir + "/" + name + "_VARS.fd"
		var loaderStr string
		var smmStr string
		if secureBoot {
			loaderStr = fmt.Sprintf("<loader readonly='yes' secure='yes' type='pflash'>%s</loader>", ovmfCodeSecboot)
			smmStr = "\n    <smm/>"
		} else {
			loaderStr = fmt.Sprintf("<loader readonly='yes' type='pflash'>%s</loader>", ovmfCode)
			smmStr = ""
		}
		osXML = fmt.Sprintf(`<os>
    <type arch='x86_64' machine='q35'>hvm</type>
    %s
    <nvram template='%s'>%s</nvram>
    %s
  </os>`, loaderStr, ovmfVarsTemplate, nvramPath, boot)
		featuresXML = fmt.Sprintf(`<features>
    <acpi/>
    <apic/>%s%s
  </features>`, smmStr, kvmHiddenXML)
		return osXML, featuresXML
	}
	osXML = fmt.Sprintf(`<os>
    <type arch='x86_64' machine='%s'>hvm</type>
    %s
  </os>`, xmlEscape(machine), boot)
	featuresXML = fmt.Sprintf(`<features>
    <acpi/>
    <apic/>%s
  </features>`, kvmHiddenXML)
	return osXML, featuresXML
}

// domainXMLTemplate is the libvirt XML WebKVM generates for a new VM. Every
// interpolated value is either escaped or comes from a fixed template.
const domainXMLTemplate = `<domain type='kvm'>
  <name>%s</name>
  <uuid>%s</uuid>
  <title>%s</title>
  %s
  <vcpu placement='static'>%d</vcpu>%s%s
  %s
  %s
  %s
  <clock offset='utc'>
    <timer name='rtc' tickpolicy='catchup'/>
    <timer name='pit' tickpolicy='delay'/>
    <timer name='hpet' present='no'/>
  </clock>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <controller type='usb' model='qemu-xhci'/>
    %s
    <disk type='file' device='disk'>
      <driver %s/>
      <source file='%s'/>
      <target dev='%s' bus='%s'/>
    </disk>
    %s
    %s
    %s
    %s<channel type='unix'>
      <target type='virtio' name='org.qemu.guest_agent.0'/>
    </channel>
    %s
    %s
    %s
  </devices>
</domain>`

func buildDomainXML(name, uuid, title, memoryXML string, vcpus int, iothreadsXML, cputuneXML, osXML, featuresXML, cpuXML, controllerXML, diskDriverAttrs, diskPath, targetDev, diskBus, isoXML, virtioISOXML, interfaceXML, serialXML, graphicsXML, videoXML, devicesExtra string) string {
	return fmt.Sprintf(domainXMLTemplate, xmlEscape(name), uuid, title, memoryXML, vcpus, iothreadsXML, cputuneXML, osXML, featuresXML, cpuXML, controllerXML, diskDriverAttrs, xmlEscape(diskPath), xmlEscape(targetDev), xmlEscape(diskBus), isoXML, virtioISOXML, interfaceXML, serialXML, graphicsXML, videoXML, devicesExtra)
}


func buildCPUXML(cpuMode, cpuModel string, sockets, cores, threads *int, flags []string) string {
	if cpuMode == "" {
		cpuMode = "host-passthrough"
	}
	var topologyXML string
	if sockets != nil && cores != nil && threads != nil && *sockets > 0 && *cores > 0 && *threads > 0 {
		topologyXML = fmt.Sprintf("<topology sockets='%d' cores='%d' threads='%d'/>", *sockets, *cores, *threads)
	}

	var featuresBuilder strings.Builder
	for _, f := range flags {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		policy := "require"
		name := f
		if strings.HasPrefix(f, "+") {
			policy = "require"
			name = strings.TrimPrefix(f, "+")
		} else if strings.HasPrefix(f, "-") {
			policy = "disable"
			name = strings.TrimPrefix(f, "-")
		}
		if name != "" {
			featuresBuilder.WriteString(fmt.Sprintf("\n    <feature policy='%s' name='%s'/>", xmlEscape(policy), xmlEscape(name)))
		}
	}
	featuresXML := featuresBuilder.String()

	if cpuMode == "custom" && cpuModel != "" {
		inner := fmt.Sprintf("<model>%s</model>%s%s", xmlEscape(cpuModel), topologyXML, featuresXML)
		return fmt.Sprintf("<cpu mode='custom' match='exact'>%s</cpu>", inner)
	}

	if topologyXML != "" || featuresXML != "" {
		return fmt.Sprintf("<cpu mode='%s' check='none'>%s%s</cpu>", xmlEscape(cpuMode), topologyXML, featuresXML)
	}
	return fmt.Sprintf("<cpu mode='%s' check='none'/>", xmlEscape(cpuMode))
}

func updateOSTag(title, tag, value string) string {
	re := regexp.MustCompile(`\[` + tag + `=[^\]]*\]`)
	result := re.FindString(title)
	if result != "" {
		// ReplaceAllStringFunc so a '$' in value (e.g. "$1") is
		// inserted literally instead of being treated as a capture
		// group reference. The whole title is xmlEscape'd upstream.
		return re.ReplaceAllStringFunc(title, func(string) string {
			return "[" + tag + "=" + value + "]"
		})
	}
	return title + " [" + tag + "=" + value + "]"
}


// linuxBridgeCheck reports whether a network name is a traditional Linux
// bridge on the host. Extracted so tests can exercise the bridge branch of
// interfaceXML without requiring a real bridge device.
var linuxBridgeCheck = isLinuxBridge

// mainBridgeCheck returns the host's primary Linux bridge (vmbr0/br0).
// Extracted so tests can exercise interfaceXML's empty-network default.
var mainBridgeCheck = mainBridge

// interfaceXML renders the guest <interface> element. WebKVM v2.4 uses ONE
// network model: real OS-level Linux bridges (vmbr0, vmbr1, …). KVM always
// attaches with <interface type='bridge'><source bridge='vmbrX'/> — there
// is no libvirt virtual-network (NAT) path anymore. When the requested
// network is empty or not a host bridge, the host's main bridge is used.
func interfaceXML(net, model string) string {
	return interfaceXMLWithVLAN(net, model, nil)
}

func interfaceXMLWithVLAN(net, model string, vlanTag *int) string {
	if net == "" || !linuxBridgeCheck(net) {
		net = mainBridgeCheck()
	}
	var vlanBlock string
	if vlanTag != nil && *vlanTag > 0 && *vlanTag <= 4094 {
		vlanBlock = fmt.Sprintf("\n      <vlan><tag id='%d'/></vlan>", *vlanTag)
	}
	return fmt.Sprintf(`<interface type='bridge'>
      <source bridge='%s'/>%s
      <model type='%s'/>
    </interface>`, xmlEscape(net), vlanBlock, xmlEscape(model))
}


func attrValue(xml, attr string) string {
	re := regexp.MustCompile(attr + `='([^']+)'`)
	m := re.FindStringSubmatch(xml)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}


func detectOSIcon(name, xmlDesc string) string {
	nameLower := strings.ToLower(name)
	switch {
	case strings.Contains(nameLower, "ubuntu"):
		return "ubuntu"
	case strings.Contains(nameLower, "debian"):
		return "debian"
	case strings.Contains(nameLower, "fedora"):
		return "fedora"
	case strings.Contains(nameLower, "centos"), strings.Contains(nameLower, "rhel"):
		return "centos"
	case strings.Contains(nameLower, "arch"):
		return "arch"
	case strings.Contains(nameLower, "windows"):
		return "windows"
	case strings.Contains(nameLower, "win"):
		return "windows"
	case strings.Contains(nameLower, "freebsd"):
		return "freebsd"
	case strings.Contains(nameLower, "opensuse"), strings.Contains(nameLower, "suse"):
		return "suse"
	case strings.Contains(nameLower, "alpine"):
		return "alpine"
	default:
		return "linux"
	}
}

func detectOSType(name, xmlDesc string) string {
	title := extractTagValue(xmlDesc, "title")
	if title != "" {
		re := regexp.MustCompile(`OSType=([^\s\]]+)`)
		m := re.FindStringSubmatch(title)
		if len(m) > 1 {
			return m[1]
		}
	}
	return detectOSIcon(name, xmlDesc)
}

func detectOSVersion(xmlDesc string) string {
	title := extractTagValue(xmlDesc, "title")
	if title != "" {
		re := regexp.MustCompile(`OSVersion=([^\s\]]+)`)
		m := re.FindStringSubmatch(title)
		if len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func extractDiskSize(xmlDesc string) int64 {
	// Simple extraction from domain XML
	lines := strings.Split(xmlDesc, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "<capacity unit=") {
			capStr := extractTagValue(line, "capacity")
			unit := extractTagAttr(line, "unit")
			val, err := strconv.ParseInt(capStr, 10, 64)
			if err != nil {
				continue
			}
			switch strings.ToLower(unit) {
			case "bytes", "b":
				return val / (1024 * 1024 * 1024)
			case "kb", "k":
				return val / (1024 * 1024)
			case "mb", "m":
				return val / 1024
			case "gb", "g":
				return val
			}
			return val
		}
	}
	return 0
}

func extractTagValue(xml, tag string) string {
	re := regexp.MustCompile(`<` + tag + `[^>]*>(.*?)</` + tag + `>`)
	m := re.FindStringSubmatch(xml)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}


func (c *Connector) parseDisks(xmlDesc string) []models.DiskInfo {
	return c.parseDisksFiltered(xmlDesc, false)
}

// parseDisksFiltered returns the list of <disk> elements. When
// disksOnly is true, CD-ROMs (device='cdrom') are excluded; the rest
// of the codebase passes false because the UI shows the VM's full
// disk list (including the optical drive), but exports and disk
// resize must skip CD-ROMs because they aren't real disks.

// deepestSourcePath extracts the root (deepest backing file) source path from a
// disk XML fragment when libvirt includes the <backingStore> chain.
func deepestSourcePath(diskXML string) string {
	re := regexp.MustCompile(`<source\b[^>]*file='([^']+)'`)
	matches := re.FindAllStringSubmatch(diskXML, -1)
	// A single source means there is no backing chain in the XML; rely on qemu-img.
	if len(matches) <= 1 {
		return ""
	}
	// The last <source file> in the fragment is the root of the backing chain.
	return matches[len(matches)-1][1]
}

// rootDiskName returns the base file name of a disk image, walking the backing
// chain with qemu-img when the XML does not expose it. This gives stable names
// (e.g. "ubuntu-1.qcow2") even when the active source is a snapshot overlay.
func rootDiskName(path string) string {
	if path == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", "--backing-chain", path).Output()
	if err != nil {
		return filepath.Base(path)
	}
	var chain []struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(out, &chain); err != nil || len(chain) == 0 {
		return filepath.Base(path)
	}
	return filepath.Base(chain[len(chain)-1].Filename)
}

func (c *Connector) parseDisksFiltered(xmlDesc string, disksOnly bool) []models.DiskInfo {
	disks := make([]models.DiskInfo, 0)
	// Use [\s\S]*? to match multiline XML (Go regex default is DOTALL=false)
	re := regexp.MustCompile(`<disk\b([^>]*)>[\s\S]*?</disk>`)
	matches := re.FindAllString(xmlDesc, -1)
	for _, d := range matches {
		device := attrValue(d, "device")
		if disksOnly && device == "cdrom" {
			continue
		}
		dType := attrValue(d, "type")
		bus := ""
		var targetDev string
		targetRe := regexp.MustCompile(`<target\b[^>]*dev='([^']+)'[^>]*bus='([^']+)'`)
		tm := targetRe.FindStringSubmatch(d)
		if len(tm) > 2 && tm[2] != "" {
			targetDev = tm[1]
			bus = tm[2]
		} else if len(tm) > 1 {
			targetDev = tm[1]
			switch {
			case strings.HasPrefix(targetDev, "vd"):
				bus = "virtio"
			case strings.HasPrefix(targetDev, "hd"):
				bus = "ide"
			case strings.HasPrefix(targetDev, "sd"):
				bus = "sata"
			}
		}
		source := ""
		sourceRe := regexp.MustCompile(`<source\b[^>]*file='([^']+)'`)
		sm := sourceRe.FindStringSubmatch(d)
		if len(sm) > 1 {
			source = sm[1]
		}
		blockDev := ""
		blockDevRe := regexp.MustCompile(`<source\b[^>]*dev='([^']+)'`)
		bm := blockDevRe.FindStringSubmatch(d)
		if len(bm) > 1 {
			blockDev = bm[1]
		}
		var zvolName string
		if strings.HasPrefix(blockDev, "/dev/zvol/") {
			zvolName = strings.TrimPrefix(blockDev, "/dev/zvol/")
		}
		readOnly := strings.Contains(d, "<readonly/>") || strings.Contains(d, "<readonly>")
		pool, sizeGB := c.volPoolAndSize(source)
		displaySource := source
		if ds := deepestSourcePath(d); ds != "" {
			displaySource = ds
		} else if source != "" {
			displaySource = rootDiskName(source)
		} else if zvolName != "" {
			displaySource = zvolName
		} else if blockDev != "" {
			displaySource = filepath.Base(blockDev)
		}
		wwn := ""
		if wm := regexp.MustCompile(`<wwn>([^<]+)</wwn>`).FindStringSubmatch(d); len(wm) > 1 {
			wwn = wm[1]
		}
		serial := ""
		if sm := regexp.MustCompile(`<serial>([^<]+)</serial>`).FindStringSubmatch(d); len(sm) > 1 {
			serial = sm[1]
		}
		alias := ""
		if am := regexp.MustCompile(`<alias\b[^>]*name='([^']+)'`).FindStringSubmatch(d); len(am) > 1 {
			alias = am[1]
		}
		disks = append(disks, models.DiskInfo{
			Device:   device,
			Bus:      bus,
			Target:   targetDev,
			Source:   source,
			BlockDev: blockDev,
			ZVol:     zvolName,
			Name:     filepath.Base(displaySource),
			Pool:     pool,
			SizeGB:   sizeGB,
			ReadOnly: readOnly,
			Type:     dType,
			WWN:      wwn,
			Serial:   serial,
			Alias:    alias,
		})
	}
	return disks
}

func parseNetworks(xmlDesc string) []models.NetIface {
	ifaces := make([]models.NetIface, 0)
	// Use [\s\S]*? to match multiline XML (Go regex default is DOTALL=false)
	re := regexp.MustCompile(`<interface\b([^>]*)>[\s\S]*?</interface>`)
	matches := re.FindAllString(xmlDesc, -1)
	for _, i := range matches {
		iface := models.NetIface{
			Type:    attrValue(i, "type"),
			MAC:     attrValue(i, "address"),
			Model:   attrValue(i, "type"),
			Network: attrValue(i, "network"),
			Source:  attrValue(i, "bridge"),
		}
		// also check <source network='xxx'>
		if iface.Network == "" {
			iface.Network = extractTagAttr(i, "network")
		}
		if iface.Source == "" {
			iface.Source = extractTagAttr(i, "bridge")
		}
		// model from <model type='xxx'>
		modelRe := regexp.MustCompile(`<model\b[^>]*type='([^']+)'`)
		mm := modelRe.FindStringSubmatch(i)
		if len(mm) > 1 {
			iface.Model = mm[1]
		}
		// vlan from <vlan><tag id='X'/>
		vlanRe := regexp.MustCompile(`<vlan>\s*<tag\s+id='(\d+)'`)
		if vm := vlanRe.FindStringSubmatch(i); len(vm) > 1 {
			if tagVal, err := strconv.Atoi(vm[1]); err == nil {
				iface.VLANTag = &tagVal
			}
		}
		// link state from <link state='up|down'/>
		linkRe := regexp.MustCompile(`<link\b[^>]*state='([^']+)'`)
		if lm := linkRe.FindStringSubmatch(i); len(lm) > 1 {
			iface.LinkState = lm[1]
		} else {
			iface.LinkState = "up"
		}
		ifaces = append(ifaces, iface)
	}
	return ifaces
}


