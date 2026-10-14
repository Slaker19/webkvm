package libvirt

import (
	"context"
	"encoding/json"
	"encoding/xml"
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

	"github.com/google/uuid"
	"libvirt.org/go/libvirt"
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

func (c *Connector) ListDomains() ([]models.VM, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}

	doms, err := c.conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_ACTIVE | libvirt.CONNECT_LIST_DOMAINS_INACTIVE)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}

	vms := make([]models.VM, 0, len(doms))
	for i := range doms {
		vm, err := c.domainToVM(&doms[i])
		doms[i].Free()
		if err != nil {
			continue
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

func (c *Connector) GetDomain(id string) (models.VM, error) {
	if err := c.ensureConnected(); err != nil {
		return models.VM{}, err
	}

	dom, err := c.lookupDomain(id)
	if err != nil {
		return models.VM{}, err
	}
	defer dom.Free()
	return c.domainToVM(dom)
}

// GetDomainXML returns the raw libvirt <domain>...</domain>
// descriptor for a domain. The Phase II backup runner calls
// this once per in-scope VM to populate the domain.xml entry
// in the per-VM archive. The XML is libvirt's authoritative
// record of the VM's hardware config, so a restore can re-
// define the VM without losing anything (CPU topology, NIC
// model, firmware, etc.).
func (c *Connector) GetDomainXML(id string) (string, error) {
	if err := c.ensureConnected(); err != nil {
		return "", err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return "", err
	}
	defer dom.Free()
	return dom.GetXMLDesc(0)
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

func (c *Connector) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	if err := c.ensureConnected(); err != nil {
		return models.VM{}, err
	}

	usingExistingDisk := req.ExistingDiskPool != "" && req.ExistingDiskName != ""

	poolName := req.StoragePool
	if poolName == "" {
		poolName = c.DiskPoolName()
	}

	poolPath, err := c.GetPoolPath(poolName)
	if err != nil {
		return models.VM{}, fmt.Errorf("get pool path: %w", err)
	}

	diskFormat := req.DiskFormat
	if diskFormat == "" {
		diskFormat = "qcow2"
	}
	diskExt := ".qcow2"
	if diskFormat == "raw" {
		diskExt = ".img"
	}
	diskName := req.Name + diskExt
	diskFullPath := poolPath + "/" + diskName

	if usingExistingDisk {
		vol, err := c.GetStorageVolume(req.ExistingDiskPool, req.ExistingDiskName)
		if err != nil {
			return models.VM{}, fmt.Errorf("get existing disk: %w", err)
		}
		diskFullPath = vol.Path
		if diskFullPath == "" {
			return models.VM{}, fmt.Errorf("existing disk has no source path")
		}
		diskFormat = vol.Format
		if diskFormat == "" {
			diskFormat = "qcow2"
		}
	}

	diskBus := req.DiskBus
	if diskBus == "" {
		diskBus = "virtio"
	}
	if err := validateDiskBus(diskBus); err != nil {
		return models.VM{}, err
	}
	targetDev := "vda"
	if diskBus == "sata" || diskBus == "scsi" || diskBus == "ide" {
		targetDev = "sda"
	}

	cpuMode := req.CPUMode
	if cpuMode == "" {
		cpuMode = "host-passthrough"
	}
	if req.CPUSockets != nil || req.CPUCores != nil || req.CPUThreads != nil {
		if req.CPUSockets == nil || req.CPUCores == nil || req.CPUThreads == nil {
			return models.VM{}, fmt.Errorf("cpu_sockets, cpu_cores and cpu_threads must all be set together")
		}
		if *req.CPUSockets <= 0 || *req.CPUCores <= 0 || *req.CPUThreads <= 0 {
			return models.VM{}, fmt.Errorf("cpu_sockets, cpu_cores and cpu_threads must be positive")
		}
		if (*req.CPUSockets)*(*req.CPUCores)*(*req.CPUThreads) != req.VCPUs {
			return models.VM{}, fmt.Errorf("cpu_sockets * cpu_cores * cpu_threads (%d) must equal vcpus (%d)", (*req.CPUSockets)*(*req.CPUCores)*(*req.CPUThreads), req.VCPUs)
		}
	}

	if cpuMode == "custom" && req.CPUModel != "" {
		if err := validateCPUModel(req.CPUModel); err != nil {
			return models.VM{}, err
		}
	}
	if err := validateCPUFlags(req.CPUFlags); err != nil {
		return models.VM{}, err
	}

	cpuXML := buildCPUXML(cpuMode, req.CPUModel, req.CPUSockets, req.CPUCores, req.CPUThreads, req.CPUFlags)

	videoModel := req.VideoModel
	if videoModel == "" {
		videoModel = "virtio"
	}
	if err := validateVideoModel(videoModel); err != nil {
		return models.VM{}, err
	}
	var videoXML string
	if videoModel == "none" {
		videoXML = ""
	} else {
		videoXML = fmt.Sprintf("<video><model type='%s' heads='1' vram='16384'/></video>", xmlEscape(videoModel))
	}

	networkModel := req.NetworkModel
	if networkModel == "" {
		networkModel = "virtio"
	}
	if err := validateNetworkModel(networkModel); err != nil {
		return models.VM{}, err
	}

	// WebKVM v2.4 (Proxmox-style): KVM always attaches to a REAL OS-level
	// Linux bridge (vmbr0, vmbr1, …). A provided network must be an existing
	// host bridge; an empty selection lands on the main bridge. Never a
	// libvirt virtual/NAT network.
	if req.Network != "" && !isLinuxBridge(req.Network) {
		return models.VM{}, fmt.Errorf("network %q is not a host Linux bridge; WebKVM only uses real bridges (vmbr0, vmbr1, …)", req.Network)
	}
	if req.Network == "" && mainBridge() == "" {
		return models.VM{}, errNoPhysicalBridge
	}

	isoXML := ""
	if req.ISO != "" {
		resolvedISO, err := c.resolveMediaPath(req.ISO, req.Scope)
		if err != nil {
			return models.VM{}, fmt.Errorf("resolve ISO: %w", err)
		}
		req.ISO = resolvedISO
		if strings.HasSuffix(strings.ToLower(req.ISO), ".img") {
			// A .img is a raw disk image (Raspberry Pi/ZimaOS/cloud-image
			// style appliances), not optical media — it has no El Torito
			// boot catalog for the firmware to find, so attaching it as
			// a cdrom silently fails to boot. Attach it as a real,
			// writable disk instead so the guest actually starts from it.
			isoXML = fmt.Sprintf(`<disk type='file' device='disk'>
      <driver name='qemu' type='raw'/>
      <source file='%s'/>
      <target dev='sda' bus='sata'/>
    </disk>`, xmlEscape(req.ISO))
		} else {
			isoXML = fmt.Sprintf(`<disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='%s'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>`, xmlEscape(req.ISO))
		}
	}

	virtioISOXML := ""
	if req.VirtIOISO != "" {
		resolvedVirtIO, err := c.resolveMediaPath(req.VirtIOISO, req.Scope)
		if err != nil {
			return models.VM{}, fmt.Errorf("resolve virtio ISO: %w", err)
		}
		req.VirtIOISO = resolvedVirtIO
		virtioISOXML = fmt.Sprintf(`<disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='%s'/>
      <target dev='sdb' bus='sata'/>
      <readonly/>
    </disk>`, xmlEscape(req.VirtIOISO))
	}

	chipset := req.Chipset
	if chipset == "" {
		chipset = "q35"
	}

	machine := chipset
	if machine == "i440fx" {
		machine = "pc"
	}

	secureBoot := req.SecureBoot != nil && *req.SecureBoot
	tpmEnabled := req.TPMEnabled != nil && *req.TPMEnabled

	firmware := req.Firmware
	if firmware == "" {
		if chipset == "q35" {
			firmware = "uefi"
		} else {
			firmware = "seabios"
		}
	}

	var osXML string
	var featuresXML string
	var devicesExtra string

	boot := bootDeviceAttr(req.BootOrder)

	osXML, featuresXML = buildOSFeaturesXML(req.Name, firmware, chipset, machine, boot, secureBoot, req.KVMHidden != nil && *req.KVMHidden)

	var controllerXML string
	if chipset == "q35" {
		controllerXML = `<controller type='pci' model='pcie-root'/>`
		for i := 1; i <= 14; i++ {
			controllerXML += fmt.Sprintf("\n    <controller type='pci' model='pcie-root-port' index='%d'/>", i)
		}
	} else {
		controllerXML = `<controller type='pci' model='pci-root'/>
    <controller type='pci' model='pci-bridge'/>`
	}
	if diskBus == "scsi" {
		if req.IOThreads > 0 {
			controllerXML += "\n    <controller type='scsi' model='virtio-scsi' index='0'>\n      <driver iothread='1'/>\n    </controller>"
		} else {
			controllerXML += "\n    <controller type='scsi' model='virtio-scsi' index='0'/>"
		}
	}

	if chipset == "q35" && tpmEnabled {
		tpmVersion := "2.0"
		tpmModel := "tpm-crb"
		if req.TPMVersion == "1.2" {
			tpmVersion = "1.2"
			tpmModel = "tpm-tis"
		}
		devicesExtra += fmt.Sprintf(`<tpm model='%s'>
      <backend type='emulator' version='%s'/>
    </tpm>
    `, tpmModel, tpmVersion)
	}

	devicesExtra += `<rng model='virtio'>
      <backend model='random'>/dev/urandom</backend>
    </rng>
    <input type='tablet' bus='usb'/>
    <memballoon model='virtio'/>
    `

	if req.AudioModel != "" && req.AudioModel != "none" {
		if err := validateAudioModel(req.AudioModel); err != nil {
			return models.VM{}, err
		}
		devicesExtra += fmt.Sprintf(`<sound model='%s'/>
    `, xmlEscape(req.AudioModel))
	}

	// Watchdog: no chipset restriction (unlike TPM/SecureBoot, which are
	// UEFI/q35-only) — i6300esb works on both q35 and i440fx.
	if req.WatchdogEnabled != nil && *req.WatchdogEnabled {
		devicesExtra += `<watchdog model='i6300esb' action='reset'/>
    `
	}

	serialXML := ""
	if req.SerialPort == nil || *req.SerialPort {
		serialXML = `<serial type='pty'>
      <target port='0'/>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>
    `
	}

	graphicsXML := ""
	gType := resolveGraphicsType(req.GraphicsType)
	switch gType {
	case "vnc":
		graphicsXML = `<graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
    </graphics>`
	case "spice":
		graphicsXML = `<channel type='spicevmc'>
      <target type='virtio' name='com.redhat.spice.0'/>
    </channel>
    <graphics type='spice' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
      <image compression='auto_glz'/>
    </graphics>`
	case "both":
		graphicsXML = `<channel type='spicevmc'>
      <target type='virtio' name='com.redhat.spice.0'/>
    </channel>
    <graphics type='spice' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
      <image compression='auto_glz'/>
    </graphics>
    <graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
    </graphics>`
	case "none":
		// no graphics device
		graphicsXML = ""
	}

	diskDriverAttrs := diskDriverXMLAttrs(diskFormat, req.DiskCacheIO, req.DiskDiscard)

	uuidStr := uuid.New().String()
	title := xmlEscape(req.Name)
	if req.OSType != "" {
		title += " [OSType=" + xmlEscape(req.OSType)
		if req.OSVersion != "" {
			title += " OSVersion=" + xmlEscape(req.OSVersion)
		}
		title += "]"
	}
	var cputuneXML string
	if req.CPUUnits != nil && *req.CPUUnits > 0 {
		cputuneXML = fmt.Sprintf("\n  <cputune>\n    <shares>%d</shares>\n  </cputune>", *req.CPUUnits)
	}

	var iothreadsXML string
	if req.IOThreads > 0 {
		iothreadsXML = fmt.Sprintf("\n  <iothreads>%d</iothreads>", req.IOThreads)
	}

	memoryXML := fmt.Sprintf("<memory unit='MiB'>%d</memory>", req.RAMMB)
	if req.MinRAMMB > 0 && req.MinRAMMB <= req.RAMMB {
		memoryXML += fmt.Sprintf("\n  <currentMemory unit='MiB'>%d</currentMemory>", req.MinRAMMB)
	} else {
		memoryXML += fmt.Sprintf("\n  <currentMemory unit='MiB'>%d</currentMemory>", req.RAMMB)
	}

	xmlConfig := buildDomainXML(req.Name, uuidStr, title, memoryXML, req.VCPUs, iothreadsXML, cputuneXML, osXML, featuresXML, cpuXML, controllerXML, diskDriverAttrs, diskFullPath, targetDev, diskBus, isoXML, virtioISOXML, interfaceXMLWithVLAN(req.Network, networkModel, req.VLANTag), serialXML, graphicsXML, videoXML, devicesExtra)

	dom, err := c.defineDomain(xmlConfig, TrustedDomain)
	if err != nil {
		return models.VM{}, fmt.Errorf("define domain: %w", err)
	}
	defer dom.Free()

	// Fase 5: apply the create-time autostart flag (libvirtd autostart).
	if req.Autostart != nil {
		if err := dom.SetAutostart(*req.Autostart); err != nil {
			dom.Undefine()
			return models.VM{}, fmt.Errorf("set autostart: %w", err)
		}
	}

	if !usingExistingDisk && req.DiskGB > 0 {
		if err := c.createDiskInPool(poolName, diskName, req.DiskGB*1024, diskFormat); err != nil {
			dom.Undefine()
			return models.VM{}, fmt.Errorf("create disk: %w", err)
		}
	}

	return c.domainToVM(dom)
}

func (c *Connector) UpdateDomain(id string, req models.UpdateVMRequest) (models.VM, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return models.VM{}, err
	}

	// Renaming requires the domain to be inactive: patching the <name>
	// and calling DomainDefineXML with the same UUID fails with
	// "domain is already defined", and libvirt's virDomainRename only
	// accepts inactive domains. Resolve the rename here, against the
	// live handle, before we redefine the rest of the config.
	curName, nerr := dom.GetName()
	if nerr != nil {
		dom.Free()
		return models.VM{}, fmt.Errorf("get domain name: %w", nerr)
	}
	if req.Name != nil && *req.Name != "" && *req.Name != curName {
		state, _, serr := dom.GetState()
		if serr == nil && state != libvirt.DOMAIN_SHUTOFF {
			dom.Free()
			return models.VM{}, ErrDomainMustBeStoppedToRename
		}
		if rerr := dom.Rename(*req.Name, 0); rerr != nil {
			dom.Free()
			return models.VM{}, fmt.Errorf("rename VM: %w", rerr)
		}
	}

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	dom.Free()
	if err != nil {
		return models.VM{}, fmt.Errorf("get domain xml: %w", err)
	}

	if req.RAMMB != nil {
		newValMiB := fmt.Sprintf("<memory unit='MiB'>%d</memory>", *req.RAMMB)
		newValKiB := fmt.Sprintf("<memory unit='KiB'>%d</memory>", *req.RAMMB*1024)
		reMiB := regexp.MustCompile(`<memory unit='MiB'>\d+</memory>`)
		reKiB := regexp.MustCompile(`<memory unit='KiB'>\d+</memory>`)
		xmlDesc = reMiB.ReplaceAllString(xmlDesc, newValMiB)
		xmlDesc = reKiB.ReplaceAllString(xmlDesc, newValKiB)
		if req.MinRAMMB == nil {
			reCurrentMiB := regexp.MustCompile(`<currentMemory unit='MiB'>\d+</currentMemory>`)
			reCurrentKiB := regexp.MustCompile(`<currentMemory unit='KiB'>\d+</currentMemory>`)
			xmlDesc = reCurrentMiB.ReplaceAllString(xmlDesc, fmt.Sprintf("<currentMemory unit='MiB'>%d</currentMemory>", *req.RAMMB))
			xmlDesc = reCurrentKiB.ReplaceAllString(xmlDesc, fmt.Sprintf("<currentMemory unit='KiB'>%d</currentMemory>", *req.RAMMB*1024))
		}
	}
	if req.MinRAMMB != nil {
		if *req.MinRAMMB > 0 {
			reCurrentMiB := regexp.MustCompile(`<currentMemory unit='MiB'>\d+</currentMemory>`)
			reCurrentKiB := regexp.MustCompile(`<currentMemory unit='KiB'>\d+</currentMemory>`)
			if reCurrentMiB.MatchString(xmlDesc) {
				xmlDesc = reCurrentMiB.ReplaceAllString(xmlDesc, fmt.Sprintf("<currentMemory unit='MiB'>%d</currentMemory>", *req.MinRAMMB))
			} else if reCurrentKiB.MatchString(xmlDesc) {
				xmlDesc = reCurrentKiB.ReplaceAllString(xmlDesc, fmt.Sprintf("<currentMemory unit='KiB'>%d</currentMemory>", *req.MinRAMMB*1024))
			} else {
				xmlDesc = regexp.MustCompile(`(<memory[^>]*>[^<]*</memory>)`).ReplaceAllString(xmlDesc, "${1}\n  "+fmt.Sprintf("<currentMemory unit='MiB'>%d</currentMemory>", *req.MinRAMMB))
			}
		} else {
			var maxMem int64
			if m := regexp.MustCompile(`<memory unit='MiB'>(\d+)</memory>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
				maxMem, _ = strconv.ParseInt(m[1], 10, 64)
			} else if m := regexp.MustCompile(`<memory unit='KiB'>(\d+)</memory>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
				kib, _ := strconv.ParseInt(m[1], 10, 64)
				maxMem = kib / 1024
			}
			if maxMem > 0 {
				reCurrentMiB := regexp.MustCompile(`<currentMemory unit='MiB'>\d+</currentMemory>`)
				reCurrentKiB := regexp.MustCompile(`<currentMemory unit='KiB'>\d+</currentMemory>`)
				xmlDesc = reCurrentMiB.ReplaceAllString(xmlDesc, fmt.Sprintf("<currentMemory unit='MiB'>%d</currentMemory>", maxMem))
				xmlDesc = reCurrentKiB.ReplaceAllString(xmlDesc, fmt.Sprintf("<currentMemory unit='KiB'>%d</currentMemory>", maxMem*1024))
			}
		}
	}
	if req.IOThreads != nil {
		iothreadRE := regexp.MustCompile(`<iothreads>\d+</iothreads>\s*`)
		if *req.IOThreads > 0 {
			iothreadXML := fmt.Sprintf("<iothreads>%d</iothreads>", *req.IOThreads)
			if iothreadRE.MatchString(xmlDesc) {
				xmlDesc = iothreadRE.ReplaceAllString(xmlDesc, iothreadXML+"\n  ")
			} else {
				xmlDesc = regexp.MustCompile(`(<vcpu\b[^>]*>[^<]*</vcpu>)`).ReplaceAllString(xmlDesc, "${1}\n  "+iothreadXML)
			}
			scsiCtrlRE := regexp.MustCompile(`(?s)<controller type='scsi' model='virtio-scsi'[^>]*>[\s\S]*?</controller>|<controller type='scsi' model='virtio-scsi'[^>]*/>`)
			if scsiCtrlRE.MatchString(xmlDesc) {
				xmlDesc = scsiCtrlRE.ReplaceAllString(xmlDesc, "<controller type='scsi' model='virtio-scsi' index='0'>\n      <driver iothread='1'/>\n    </controller>")
			}
		} else {
			xmlDesc = iothreadRE.ReplaceAllString(xmlDesc, "")
			scsiCtrlRE := regexp.MustCompile(`(?s)<controller type='scsi' model='virtio-scsi'[^>]*>[\s\S]*?</controller>|<controller type='scsi' model='virtio-scsi'[^>]*/>`)
			if scsiCtrlRE.MatchString(xmlDesc) {
				xmlDesc = scsiCtrlRE.ReplaceAllString(xmlDesc, "<controller type='scsi' model='virtio-scsi' index='0'/>")
			}
		}
	}
	if req.VCPUs != nil {
		newVal := fmt.Sprintf("<vcpu placement='static'>%d</vcpu>", *req.VCPUs)
		re := regexp.MustCompile(`<vcpu placement='static'>\d+</vcpu>`)
		xmlDesc = re.ReplaceAllString(xmlDesc, newVal)
	}
	if req.CPUMode != nil || req.CPUModel != nil || req.CPUFlags != nil {
		currentMode := "host-passthrough"
		if m := regexp.MustCompile(`<cpu[^>]*mode='([^']*)'`).FindStringSubmatch(xmlDesc); len(m) > 1 {
			currentMode = m[1]
		} else if m := regexp.MustCompile(`<cpu[^>]*mode="([^"]*)"`).FindStringSubmatch(xmlDesc); len(m) > 1 {
			currentMode = m[1]
		}
		if req.CPUMode != nil {
			currentMode = *req.CPUMode
		}

		currentModel := ""
		if m := regexp.MustCompile(`<model[^>]*>([^<]+)</model>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
			currentModel = m[1]
		}
		if req.CPUModel != nil {
			currentModel = *req.CPUModel
		}

		var sockets, cores, threads *int
		if m := regexp.MustCompile(`<topology sockets='(\d+)' cores='(\d+)' threads='(\d+)'/>`).FindStringSubmatch(xmlDesc); len(m) == 4 {
			s, _ := strconv.Atoi(m[1])
			c, _ := strconv.Atoi(m[2])
			t, _ := strconv.Atoi(m[3])
			sockets, cores, threads = &s, &c, &t
		}

		flags := req.CPUFlags
		if flags == nil {
			cpuBlockMatch := regexp.MustCompile(`<cpu[^>]*>([\s\S]*?)</cpu>`).FindStringSubmatch(xmlDesc)
			if len(cpuBlockMatch) > 1 {
				featureMatches := regexp.MustCompile(`<feature\s+policy='([^']*)'\s+name='([^']*)'`).FindAllStringSubmatch(cpuBlockMatch[1], -1)
				for _, fm := range featureMatches {
					if fm[1] == "disable" {
						flags = append(flags, "-"+fm[2])
					} else {
						flags = append(flags, "+"+fm[2])
					}
				}
			}
		}

		if currentMode == "custom" && currentModel != "" {
			if err := validateCPUModel(currentModel); err != nil {
				return models.VM{}, err
			}
		}
		if err := validateCPUFlags(flags); err != nil {
			return models.VM{}, err
		}

		newCPUXML := buildCPUXML(currentMode, currentModel, sockets, cores, threads, flags)
		cpuBlockRE := regexp.MustCompile(`<cpu\b[^>]*>(?:[\s\S]*?</cpu>|/>)`)
		if cpuBlockRE.MatchString(xmlDesc) {
			xmlDesc = cpuBlockRE.ReplaceAllString(xmlDesc, newCPUXML)
		} else {
			xmlDesc = strings.Replace(xmlDesc, "</os>", "</os>\n  "+newCPUXML, 1)
		}
	}
	if req.CPUUnits != nil {
		cputuneRE := regexp.MustCompile(`<cputune>[\s\S]*?</cputune>\s*`)
		if *req.CPUUnits > 0 {
			cputuneXML := fmt.Sprintf("<cputune>\n    <shares>%d</shares>\n  </cputune>", *req.CPUUnits)
			if cputuneRE.MatchString(xmlDesc) {
				xmlDesc = cputuneRE.ReplaceAllString(xmlDesc, cputuneXML+"\n  ")
			} else {
				xmlDesc = regexp.MustCompile(`(<vcpu\b[^>]*>[^<]*</vcpu>)`).ReplaceAllString(xmlDesc, "${1}\n  "+cputuneXML)
			}
		} else {
			xmlDesc = cputuneRE.ReplaceAllString(xmlDesc, "")
		}
	}
	if req.KVMHidden != nil {
		kvmHiddenRE := regexp.MustCompile(`\s*<kvm>\s*<hidden\s+state='on'\s*/>\s*</kvm>\s*`)
		if *req.KVMHidden {
			if !strings.Contains(xmlDesc, "<hidden state='on'/>") && !strings.Contains(xmlDesc, `<hidden state="on"/>`) {
				xmlDesc = strings.Replace(xmlDesc, "<features>", "<features>\n    <kvm>\n      <hidden state='on'/>\n    </kvm>", 1)
			}
		} else {
			xmlDesc = kvmHiddenRE.ReplaceAllString(xmlDesc, "")
		}
	}
	if req.VideoModel != nil {
		if err := validateVideoModel(*req.VideoModel); err != nil {
			return models.VM{}, err
		}
		re := regexp.MustCompile(`<video>[^<]*<model type='[^']+'`)
		xmlDesc = re.ReplaceAllString(xmlDesc, "<video>\n      <model type='"+xmlEscape(*req.VideoModel)+"'")
	}
	if req.BootOrder != nil {
		switch *req.BootOrder {
		case "disk", "cdrom", "network", "":
		default:
			return models.VM{}, fmt.Errorf("boot_order must be one of: disk, cdrom, network")
		}
		xmlDesc = regexp.MustCompile(`<boot dev='[^']*'/>`).ReplaceAllString(xmlDesc, bootDeviceAttr(*req.BootOrder))
	}
	if req.Network != nil {
		// Rewrite the interface <source> element: a host Linux bridge
		// becomes <source bridge='…'/>, anything else stays a libvirt
		// <source network='…'/>.
		newSrc := fmt.Sprintf("<source network='%s'/>", xmlEscape(*req.Network))
		if isLinuxBridge(*req.Network) {
			newSrc = fmt.Sprintf("<source bridge='%s'/>", xmlEscape(*req.Network))
		}
		xmlDesc = regexp.MustCompile(`<source (network|bridge)='[^']*'/>`).ReplaceAllString(xmlDesc, newSrc)
	}
	if req.NetworkModel != nil {
		if err := validateNetworkModel(*req.NetworkModel); err != nil {
			return models.VM{}, err
		}
		re := regexp.MustCompile(`(<interface\b[^>]*>[\s\S]*?<model\s+type=')[^']+(')`)
		xmlDesc = re.ReplaceAllString(xmlDesc, "${1}"+xmlEscape(*req.NetworkModel)+"${2}")
	}
	if req.Name != nil {
		xmlDesc = regexp.MustCompile(`<name>[^<]+</name>`).ReplaceAllString(xmlDesc, "<name>"+xmlEscape(*req.Name)+"</name>")
	}
	if req.Chipset != nil {
		machine := *req.Chipset
		if machine == "i440fx" {
			machine = "pc"
		}
		xmlDesc = regexp.MustCompile(`machine='[^']+'`).ReplaceAllString(xmlDesc, "machine='"+xmlEscape(machine)+"'")
	}
	if req.SecureBoot != nil {
		if *req.SecureBoot {
			if !strings.Contains(xmlDesc, "secure='yes'") {
				xmlDesc = regexp.MustCompile(`<loader[^>]*/>`).ReplaceAllString(xmlDesc,
					fmt.Sprintf("<loader readonly='yes' secure='yes' type='pflash'>%s</loader>", ovmfCodeSecboot))
				// Add SMM feature if not present
				if !strings.Contains(xmlDesc, "<smm>") {
					xmlDesc = strings.Replace(xmlDesc, "<features>", "<features>\n    <smm/>", 1)
				}
				// Add NVRAM
				name := extractTagValue(xmlDesc, "name")
				if !strings.Contains(xmlDesc, "<nvram>") {
					xmlDesc = strings.Replace(xmlDesc, "</os>", fmt.Sprintf("\n    <nvram template='%s'>%s/%s_VARS.fd</nvram>\n  </os>", ovmfVarsTemplate, nvramDir, name), 1)
				}
			}
		} else {
			xmlDesc = strings.ReplaceAll(xmlDesc, " secure='yes'", "")
			xmlDesc = strings.ReplaceAll(xmlDesc, ` secure="yes"`, "")
			xmlDesc = regexp.MustCompile(`<smm/>\s*`).ReplaceAllString(xmlDesc, "")
		}
	}
	if req.Firmware != nil {
		if *req.Firmware == "seabios" {
			xmlDesc = regexp.MustCompile(`<loader\b[^>]*>.*?</loader>\s*`).ReplaceAllString(xmlDesc, "")
			xmlDesc = regexp.MustCompile(`<nvram>.*?</nvram>\s*`).ReplaceAllString(xmlDesc, "")
			xmlDesc = regexp.MustCompile(`<smm/>\s*`).ReplaceAllString(xmlDesc, "")
			xmlDesc = strings.ReplaceAll(xmlDesc, " secure='yes'", "")
		} else if *req.Firmware == "uefi" && !strings.Contains(xmlDesc, "<loader") {
			xmlDesc = strings.Replace(xmlDesc, "</type>", "</type>\n    "+fmt.Sprintf("<loader readonly='yes' type='pflash'>%s</loader>", ovmfCode), 1)
			name := extractTagValue(xmlDesc, "name")
			if !strings.Contains(xmlDesc, "<nvram>") {
				xmlDesc = strings.Replace(xmlDesc, "</os>", fmt.Sprintf("\n    <nvram template='%s'>%s/%s_VARS.fd</nvram>\n  </os>", ovmfVarsTemplate, nvramDir, name), 1)
			}
		}
	}
	if req.TPMEnabled != nil {
		if *req.TPMEnabled {
			model, version := tpmModelAndVersion(req.TPMVersion)
			tpmXML := fmt.Sprintf(`<tpm model='%s'>
      <backend type='emulator' version='%s'/>
    </tpm>`, model, version)
			if !strings.Contains(xmlDesc, "<tpm") {
				xmlDesc = strings.Replace(xmlDesc, "</devices>", "    "+tpmXML+"\n  </devices>", 1)
			} else {
				// Already has a TPM — replace it wholesale so a version
				// change (or a no-op re-enable) always reflects the
				// requested version.
				xmlDesc = tpmBlockRE.ReplaceAllString(xmlDesc, tpmXML)
			}
		} else {
			xmlDesc = tpmBlockWithTrailingWSRE.ReplaceAllString(xmlDesc, "")
		}
	} else if req.TPMVersion != nil && strings.Contains(xmlDesc, "<tpm") {
		// TPM already on; only its version is changing.
		model, version := tpmModelAndVersion(req.TPMVersion)
		tpmXML := fmt.Sprintf(`<tpm model='%s'>
      <backend type='emulator' version='%s'/>
    </tpm>`, model, version)
		xmlDesc = tpmBlockRE.ReplaceAllString(xmlDesc, tpmXML)
	}
	if req.WatchdogEnabled != nil {
		if *req.WatchdogEnabled {
			if !watchdogI6300esbRE.MatchString(xmlDesc) {
				xmlDesc = strings.Replace(xmlDesc, "</devices>", "    <watchdog model='i6300esb' action='reset'/>\n  </devices>", 1)
			}
		} else {
			// Only ever strip OUR explicit i6300esb device — never any
			// <watchdog>, since q35 domains always carry an intrinsic
			// <watchdog model='itco'> that libvirt itself auto-injects
			// (the ICH9 southbridge's built-in TCO watchdog) whether or
			// not this field was ever touched; blindly matching any
			// <watchdog> here would strip that chipset-intrinsic entry
			// too on every "disable" request.
			xmlDesc = watchdogI6300esbRE.ReplaceAllString(xmlDesc, "")
		}
	}
	if req.AudioModel != nil {
		if *req.AudioModel == "" || *req.AudioModel == "none" {
			xmlDesc = regexp.MustCompile(`<sound\b[^>]*/>\s*`).ReplaceAllString(xmlDesc, "")
		} else {
			if err := validateAudioModel(*req.AudioModel); err != nil {
				return models.VM{}, err
			}
			soundXML := fmt.Sprintf("<sound model='%s'/>", xmlEscape(*req.AudioModel))
			if strings.Contains(xmlDesc, "<sound") {
				xmlDesc = regexp.MustCompile(`<sound\b[^>]*/>`).ReplaceAllString(xmlDesc, soundXML)
			} else {
				xmlDesc = strings.Replace(xmlDesc, "</devices>", "    "+soundXML+"\n  </devices>", 1)
			}
		}
	}
	if req.SerialPort != nil {
		if *req.SerialPort {
			if !strings.Contains(xmlDesc, "<serial") {
				serialXML := `<serial type='pty'>
      <target port='0'/>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>`
				xmlDesc = strings.Replace(xmlDesc, "</devices>", "    "+serialXML+"\n  </devices>", 1)
			}
		} else {
			xmlDesc = regexp.MustCompile(`<serial\b[^>]*>[\s\S]*?</serial>\s*`).ReplaceAllString(xmlDesc, "")
			xmlDesc = regexp.MustCompile(`<console\b[^>]*>[\s\S]*?</console>\s*`).ReplaceAllString(xmlDesc, "")
		}
	}
	if req.GraphicsType != nil {
		gType := resolveGraphicsType(*req.GraphicsType)
		// Remove existing graphics & spicevmc channels
		xmlDesc = regexp.MustCompile(`<graphics\b[^>]*>[\s\S]*?</graphics>\s*`).ReplaceAllString(xmlDesc, "")
		xmlDesc = regexp.MustCompile(`<channel type='spicevmc'>[\s\S]*?</channel>\s*`).ReplaceAllString(xmlDesc, "")

		var newGraphics string
		switch gType {
		case "vnc":
			newGraphics = `<graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
    </graphics>`
		case "spice":
			newGraphics = `<channel type='spicevmc'>
      <target type='virtio' name='com.redhat.spice.0'/>
    </channel>
    <graphics type='spice' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
      <image compression='auto_glz'/>
    </graphics>`
		case "both":
			newGraphics = `<channel type='spicevmc'>
      <target type='virtio' name='com.redhat.spice.0'/>
    </channel>
    <graphics type='spice' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
      <image compression='auto_glz'/>
    </graphics>
    <graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
    </graphics>`
		case "none":
			// Leave graphics stripped
			newGraphics = ""
		}
		if newGraphics != "" {
			xmlDesc = strings.Replace(xmlDesc, "</devices>", "    "+newGraphics+"\n  </devices>", 1)
		}
	}
	if req.OSType != nil || req.OSVersion != nil {
		title := extractTagValue(xmlDesc, "title")
		if req.OSType != nil {
			title = updateOSTag(title, "OSType", *req.OSType)
		}
		if req.OSVersion != nil {
			title = updateOSTag(title, "OSVersion", *req.OSVersion)
		}
		xmlDesc = regexp.MustCompile(`<title>[^<]*</title>`).ReplaceAllString(xmlDesc, "<title>"+xmlEscape(title)+"</title>")
	}

	newDom, err := c.defineDomain(xmlDesc, TrustedDomain)
	if err != nil {
		return models.VM{}, fmt.Errorf("define domain: %w", err)
	}
	defer newDom.Free()

	// Live memory adjustment (ballooning) if domain is currently running
	if state, _, err := newDom.GetState(); err == nil && state == libvirt.DOMAIN_RUNNING {
		if req.MinRAMMB != nil && *req.MinRAMMB > 0 {
			_ = newDom.SetMemoryFlags(uint64(*req.MinRAMMB)*1024, libvirt.DOMAIN_MEM_LIVE)
		} else if req.RAMMB != nil && *req.RAMMB > 0 {
			_ = newDom.SetMemoryFlags(uint64(*req.RAMMB)*1024, libvirt.DOMAIN_MEM_LIVE)
		}
		if req.VCPUs != nil && *req.VCPUs > 0 {
			_ = newDom.SetVcpusFlags(uint(*req.VCPUs), libvirt.DOMAIN_VCPU_LIVE)
		}
	}

	// Autostart is not part of the domain XML — it is libvirtd's own
	// flag (virDomainSetAutostart), so it must be applied on the handle
	// after the redefine. Without this, PATCH {"autostart":false}
	// returned 200 while leaving the flag untouched.
	if req.Autostart != nil {
		if err := newDom.SetAutostart(*req.Autostart); err != nil {
			return models.VM{}, fmt.Errorf("set autostart: %w", err)
		}
	}

	return c.domainToVM(newDom)
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

func (c *Connector) lookupDomain(id string) (*libvirt.Domain, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}
	dom, err := c.conn.LookupDomainByUUIDString(id)
	if err != nil {
		dom, err = c.conn.LookupDomainByName(id)
		if err != nil {
			return nil, fmt.Errorf("domain not found: %w", err)
		}
	}
	return dom, nil
}

func (c *Connector) domainToVM(dom *libvirt.Domain) (models.VM, error) {
	name, err := dom.GetName()
	if err != nil {
		return models.VM{}, err
	}

	uuidStr, err := dom.GetUUIDString()
	if err != nil {
		return models.VM{}, err
	}

	state, reason, err := dom.GetState()
	if err != nil {
		return models.VM{}, err
	}

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		xmlDesc, _ = dom.GetXMLDesc(0)
	}

	info, err := dom.GetInfo()
	if err != nil {
		return models.VM{}, err
	}

	vmState := stateToVMState(state)

	var uptime int64
	if state == libvirt.DOMAIN_RUNNING {
		ctrlInfo, err := dom.GetControlInfo(0)
		if err == nil && ctrlInfo.StateTime > 0 {
			since := time.Since(time.UnixMicro(int64(ctrlInfo.StateTime)))
			uptime = int64(since.Seconds())
			if uptime < 0 {
				uptime = 0
			}
		}
		if uptime == 0 {
			uptime = int64(info.CpuTime / 1e9)
		}
	}

	machine := attrValue(xmlDesc, "machine")
	chipset := "q35"
	if machine == "pc" || strings.Contains(machine, "i440fx") {
		chipset = "i440fx"
	}

	secureBoot := strings.Contains(xmlDesc, "secure='yes'") || strings.Contains(xmlDesc, `secure="yes"`)
	tpmEnabled := strings.Contains(xmlDesc, "<tpm")
	tpmVersion := ""
	if tpmEnabled {
		if m := tpmBackendVersionRE.FindStringSubmatch(xmlDesc); len(m) > 1 {
			tpmVersion = m[1]
		}
	}
	watchdogEnabled := watchdogI6300esbRE.MatchString(xmlDesc)

	firmware := "seabios"
	if strings.Contains(xmlDesc, "<loader") {
		firmware = "uefi"
	}

	cpuMode := "host-passthrough"
	if m := regexp.MustCompile(`<cpu[^>]*mode='([^']*)'`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		cpuMode = m[1]
	} else if m := regexp.MustCompile(`<cpu[^>]*mode="([^"]*)"`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		cpuMode = m[1]
	}

	var cpuModel string
	if m := regexp.MustCompile(`<cpu[^>]*>[\s\S]*?<model[^>]*>([^<]+)</model>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		cpuModel = m[1]
	}

	var cpuFlags []string
	cpuBlockMatch := regexp.MustCompile(`<cpu[^>]*>([\s\S]*?)</cpu>`).FindStringSubmatch(xmlDesc)
	if len(cpuBlockMatch) > 1 {
		featureMatches := regexp.MustCompile(`<feature\s+policy='([^']*)'\s+name='([^']*)'`).FindAllStringSubmatch(cpuBlockMatch[1], -1)
		for _, fm := range featureMatches {
			if fm[1] == "disable" {
				cpuFlags = append(cpuFlags, "-"+fm[2])
			} else {
				cpuFlags = append(cpuFlags, "+"+fm[2])
			}
		}
	}

	var cpuUnits int
	if m := regexp.MustCompile(`<cputune>[\s\S]*?<shares>(\d+)</shares>[\s\S]*?</cputune>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			cpuUnits = v
		}
	}

	kvmHidden := strings.Contains(xmlDesc, "<hidden state='on'/>") || strings.Contains(xmlDesc, `<hidden state="on"/>`)

	videoModel := "virtio"
	if vb := regexp.MustCompile(`<video>([\s\S]*?)</video>`).FindString(xmlDesc); vb != "" {
		if m := regexp.MustCompile(`<model\s+type='([^']*)'`).FindStringSubmatch(vb); len(m) > 1 {
			videoModel = m[1]
		} else if m := regexp.MustCompile(`<model\s+type="([^"]*)"`).FindStringSubmatch(vb); len(m) > 1 {
			videoModel = m[1]
		}
	}

	audioModel := "none"
	if m := regexp.MustCompile(`<sound\s+model='([^']*)'`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		audioModel = m[1]
	} else if m := regexp.MustCompile(`<sound\s+model="([^"]*)"`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		audioModel = m[1]
	}

	serialPort := strings.Contains(xmlDesc, "<serial")

	var minRAMMB int64
	if m := regexp.MustCompile(`<currentMemory unit='MiB'>(\d+)</currentMemory>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			minRAMMB = v
		}
	} else if m := regexp.MustCompile(`<currentMemory unit='KiB'>(\d+)</currentMemory>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			minRAMMB = v / 1024
		}
	} else if m := regexp.MustCompile(`<currentMemory>(\d+)</currentMemory>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			minRAMMB = v / 1024
		}
	}

	var iothreads int
	if m := regexp.MustCompile(`<iothreads>(\d+)</iothreads>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			iothreads = v
		}
	}

	hasVNC := strings.Contains(xmlDesc, "<graphics type='vnc'") || strings.Contains(xmlDesc, `<graphics type="vnc"`)
	hasSPICE := strings.Contains(xmlDesc, "<graphics type='spice'") || strings.Contains(xmlDesc, `<graphics type="spice"`)
	graphicsType := "none"
	if hasVNC && hasSPICE {
		graphicsType = "both"
	} else if hasSPICE {
		graphicsType = "spice"
	} else if hasVNC {
		graphicsType = "vnc"
	}

	vm := models.VM{
		ID:              uuidStr,
		Name:            name,
		Type:            "vm",
		Hypervisor:      "kvm",
		State:           vmState,
		VCPUs:           int(info.NrVirtCpu),
		RAMMB:           int64(info.MaxMem) / 1024,
		MinRAMMB:        minRAMMB,
		IOThreads:       iothreads,
		UptimeSec:       uptime,
		CPUUsage:        calculateCPUUsage(dom, info),
		RAMUsedMB:       int64(info.Memory) / 1024,
		OSIcon:          detectOSIcon(name, xmlDesc),
		OSType:          detectOSType(name, xmlDesc),
		OSVersion:       detectOSVersion(xmlDesc),
		Chipset:         chipset,
		SecureBoot:      secureBoot,
		TPMEnabled:      tpmEnabled,
		TPMVersion:      tpmVersion,
		WatchdogEnabled: watchdogEnabled,
		// Autostart: queried separately because libvirt's
		// GetAutostart returns an error for transient states
		// (e.g. the domain is being shut off). Default to false
		// on error — a missing autostart flag in the UI is less
		// surprising than a domain that silently restarts on
		// every boot.
		Autostart:    autostartEnabledOrFalse(dom),
		Firmware:     firmware,
		CPUMode:      cpuMode,
		CPUModel:     cpuModel,
		CPUFlags:     cpuFlags,
		CPUUnits:     cpuUnits,
		KVMHidden:    kvmHidden,
		VideoModel:   videoModel,
		AudioModel:   audioModel,
		SerialPort:   serialPort,
		GraphicsType: graphicsType,
		BootOrder:    bootOrderFromXML(xmlDesc),
	}

	diskGB := extractDiskSize(xmlDesc)
	if diskGB > 0 {
		vm.DiskGB = diskGB
	}

	vm.Disks = c.parseDisks(xmlDesc)
	vm.Networks = parseNetworks(xmlDesc)
	vm.USBDevices = parseUSBHostdevs(xmlDesc)
	vm.PCIDevices = parsePCIHostdevs(xmlDesc)
	vm.SharedFolders = parseSharedFolders(xmlDesc)

	// Fallback: extractDiskSize silently returns 0 for disks that
	// don't have a <capacity unit=...> child (LVM, zvols, OVA imports
	// before first boot, etc.). In that case, take the largest disk
	// from the parsed list — the per-disk table on the UI is reliable
	// because parseDisksFiltered populates SizeGB via volPoolAndSize.
	// The user-facing "disk_gb" field shouldn't read 0 when the data
	// is right there.
	if vm.DiskGB == 0 && len(vm.Disks) > 0 {
		var max int64
		for i := range vm.Disks {
			d := &vm.Disks[i]
			if d.Type == "file" && d.SizeGB > max {
				max = d.SizeGB
			}
		}
		vm.DiskGB = max
	}

	// Load WebKVM metadata (alias, cover, groups). Failure is non-fatal;
	// the VM list still works without app-level metadata.
	// We parse it from the XML already fetched instead of making a second libvirt API call.
	var dxml struct {
		Metadata struct {
			Meta struct {
				Alias    string   `xml:"alias,omitempty"`
				Cover    string   `xml:"cover,omitempty"`
				Groups   []string `xml:"groups>group,omitempty"`
				Tags     []string `xml:"tags>tag,omitempty"`
				Owner    string   `xml:"owner,omitempty"`
				Template bool     `xml:"template,omitempty"`
				Shared   bool     `xml:"shared,omitempty"`
				CiUser   string   `xml:"ci_user,omitempty"`
			} `xml:"https://webvm.local/ns meta"`
		} `xml:"metadata"`
	}
	if err := xml.Unmarshal([]byte(xmlDesc), &dxml); err == nil {
		meta := dxml.Metadata.Meta
		vm.Alias = meta.Alias
		vm.Cover = meta.Cover
		vm.Groups = meta.Groups
		vm.Tags = meta.Tags
		vm.OwnerID = meta.Owner
		vm.Template = meta.Template
		vm.Shared = meta.Shared
		if meta.CiUser != "" {
			vm.ProvisionMethod = "cloud-init"
		}
	}

	if state == libvirt.DOMAIN_RUNNING {
		// macIPs collects each interface's OWN IPv4s, keyed by MAC
		// (lowercased) — libvirt already reports addresses grouped by
		// interface (iface.Hwaddr), but that association was previously
		// thrown away in favor of one flat vm.IPs list shared by every
		// NIC. With more than one interface, every row in the Network
		// Interfaces tab then displayed the SAME combined IP list instead
		// of its own.
		macIPs := map[string][]string{}
		seen := map[string]bool{}
		collect := func(src libvirt.DomainInterfaceAddressesSource) {
			ifaces, err := dom.ListAllInterfaceAddresses(src)
			if err != nil {
				return
			}
			for _, iface := range ifaces {
				mac := strings.ToLower(iface.Hwaddr)
				for _, a := range iface.Addrs {
					if a.Type == libvirt.IP_ADDR_TYPE_IPV4 && !strings.HasPrefix(a.Addr, "127.") && !strings.HasPrefix(a.Addr, "169.254.") {
						if mac != "" {
							macIPs[mac] = append(macIPs[mac], a.Addr)
						}
						if !seen[a.Addr] {
							seen[a.Addr] = true
							vm.IPs = append(vm.IPs, a.Addr)
						}
					}
				}
			}
		}
		// SRC_LEASE first (authoritative); fall back to SRC_AGENT then
		// SRC_ARP so bridged VMs with router DHCP (no libvirt network) still
		// surface their LAN IP. Stop at the first source that yields IPv4.
		collect(libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_LEASE)
		if len(vm.IPs) == 0 {
			collect(libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_AGENT)
		}
		if len(vm.IPs) == 0 {
			collect(libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_ARP)
		}
		if len(vm.IPs) > 0 {
			vm.IP = vm.IPs[0]
		}
		for i := range vm.Networks {
			vm.Networks[i].IPs = macIPs[strings.ToLower(vm.Networks[i].MAC)]
		}
	}

	_ = reason
	return vm, nil
}

func stateToVMState(state libvirt.DomainState) models.VMState {
	switch state {
	case libvirt.DOMAIN_RUNNING:
		return models.VMStateRunning
	case libvirt.DOMAIN_SHUTOFF, libvirt.DOMAIN_SHUTDOWN:
		return models.VMStateShutoff
	case libvirt.DOMAIN_PAUSED:
		return models.VMStatePaused
	case libvirt.DOMAIN_CRASHED:
		return models.VMStateCrashed
	default:
		return models.VMStateUnknown
	}
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

type GraphicsInfo struct {
	Type      string `json:"type"`
	Port      int    `json:"port"`
	WebSocket int    `json:"websocket,omitempty"`
	Host      string `json:"host"`
}

func (c *Connector) GetDomainIP(id string) string {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return ""
	}
	defer dom.Free()

	for _, src := range []libvirt.DomainInterfaceAddressesSource{
		libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_LEASE,
		libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_AGENT,
		libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_ARP,
	} {
		ifaces, err := dom.ListAllInterfaceAddresses(src)
		if err != nil {
			continue
		}
		for _, iface := range ifaces {
			for _, a := range iface.Addrs {
				if a.Type == libvirt.IP_ADDR_TYPE_IPV4 && !strings.HasPrefix(a.Addr, "127.") {
					return a.Addr
				}
			}
		}
	}
	return ""
}

func (c *Connector) GetVNCInfo(id string) (GraphicsInfo, error) {
	return c.GetGraphicsInfo(id, "vnc")
}

func (c *Connector) GetSPICEInfo(id string) (GraphicsInfo, error) {
	return c.GetGraphicsInfo(id, "spice")
}

func (c *Connector) GetGraphicsInfo(id, preferredType string) (GraphicsInfo, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return GraphicsInfo{}, err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return GraphicsInfo{}, fmt.Errorf("get xml: %w", err)
	}

	graphicsRe := regexp.MustCompile(`<graphics[^>]+>`)
	matches := graphicsRe.FindAllString(xmlDesc, -1)
	if len(matches) == 0 {
		return GraphicsInfo{}, fmt.Errorf("no graphics device found")
	}

	var targetMatch string
	for _, m := range matches {
		if preferredType != "" && attrValue(m, "type") == preferredType {
			targetMatch = m
			break
		}
	}
	if targetMatch == "" {
		if preferredType != "" {
			return GraphicsInfo{}, fmt.Errorf("graphics device of type %s not configured on VM", preferredType)
		}
		targetMatch = matches[0]
	}

	gType := attrValue(targetMatch, "type")
	portStr := attrValue(targetMatch, "port")
	host := attrValue(targetMatch, "listen")

	port := 5900
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err == nil && p > 0 {
			port = p
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}

	return GraphicsInfo{
		Type: gType,
		Port: port,
		Host: host,
	}, nil
}

func attrValue(xml, attr string) string {
	re := regexp.MustCompile(attr + `='([^']+)'`)
	m := re.FindStringSubmatch(xml)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func (c *Connector) createDiskInPool(poolName, volName string, sizeMB int64, format string) error {
	// Defense in depth: volName ends up both in the volume XML below and
	// as a backing file path, so reject anything outside the safe charset
	// regardless of what upstream validation did.
	if !nameRE.MatchString(volName) {
		return fmt.Errorf("invalid volume name %q (allowed: A-Z a-z 0-9 . _ -)", volName)
	}
	switch format {
	case "qcow2", "raw":
	default:
		return fmt.Errorf("unsupported volume format %q (allowed: qcow2, raw)", format)
	}
	pool, err := c.conn.LookupStoragePoolByName(poolName)
	if err != nil {
		return fmt.Errorf("lookup pool: %w", err)
	}
	defer pool.Free()

	volXML := fmt.Sprintf(`<volume>
        <name>%s</name>
        <capacity unit='M'>%d</capacity>
        <target>
            <format type='%s'/>
        </target>
    </volume>`, volName, sizeMB, format)

	_, err = pool.StorageVolCreateXML(volXML, libvirt.STORAGE_VOL_CREATE_PREALLOC_METADATA)
	return err
}

// calculateCPUUsage intentionally reports nothing.
//
// info.CpuTime is the domain's CUMULATIVE CPU nanoseconds since boot,
// not a delta. The previous implementation divided it by a hardcoded
// 1 second, i.e. it computed "seconds of CPU burned since boot, as a
// percentage", and clamped the result with math.Min(usage, 100). Any
// VM that had ever accumulated NrVirtCpu seconds of CPU — that is,
// every VM up for more than a few seconds — therefore reported a
// constant 100%. An idle 4-vCPU guest with 60s of lifetime CPU showed
// "100.0% used" on its detail page.
//
// A correct instantaneous figure needs two samples over a known
// interval, which is exactly what MetricsCollector.collectOne already
// does (see metrics.go): the SSE "vm.metrics" stream and
// GET /api/vms/{id}/metrics carry the real series. models.VM.CPUUsage
// is `omitempty` and the UI guards with `!= null`, so returning 0 here
// omits the field and the detail page falls back to the live metrics
// instead of rendering a fabricated number.
func calculateCPUUsage(dom *libvirt.Domain, info *libvirt.DomainInfo) float64 {
	return 0
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

func extractSnapshotCreation(xmlDesc string) string {
	for _, line := range strings.Split(xmlDesc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "<creationTime>") {
			ts := strings.TrimSuffix(strings.TrimPrefix(line, "<creationTime>"), "</creationTime>")
			sec, err := strconv.ParseInt(ts, 10, 64)
			if err != nil {
				return ts
			}
			return time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// extractSnapshotEpoch returns the <creationTime> as a raw epoch-seconds
// int64 (0 if missing/unparseable). Used by the tree UI for sortable
// timestamps.
func extractSnapshotEpoch(xmlDesc string) int64 {
	for _, line := range strings.Split(xmlDesc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "<creationTime>") {
			ts := strings.TrimSuffix(strings.TrimPrefix(line, "<creationTime>"), "</creationTime>")
			sec, err := strconv.ParseInt(ts, 10, 64)
			if err == nil {
				return sec
			}
			return 0
		}
	}
	return 0
}

// extractSnapshotParent returns the <parent><name>...</name></parent>
// value, or "" if there is no parent (i.e. this is a root snapshot).
func extractSnapshotParent(xmlDesc string) string {
	s := strings.Index(xmlDesc, "<parent>")
	if s < 0 {
		s = strings.Index(xmlDesc, "<parent ")
		if s < 0 {
			return ""
		}
	}
	block := xmlDesc[s:]
	e := strings.Index(block, "</parent>")
	if e < 0 {
		return ""
	}
	block = block[:e]
	ns := strings.Index(block, "<name>")
	if ns < 0 {
		ns = strings.Index(block, "<name ")
		if ns < 0 {
			return ""
		}
	}
	block = block[ns:]
	ne := strings.Index(block, "</name>")
	if ne < 0 {
		return ""
	}
	name := strings.TrimPrefix(block[:ne], "<name>")
	name = strings.TrimSpace(name)
	// Handle <name attr='x'>name</name> form.
	if strings.HasPrefix(name, " ") {
		// The "<name " form: extract just the inner text.
		closeTag := strings.Index(name, ">")
		if closeTag >= 0 {
			name = name[closeTag+1:]
		}
	}
	return strings.TrimSpace(name)
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

// volPoolAndSize resolves the storage pool name and capacity (in GB) for a
// given volume file path using libvirt's canonical LookupStorageVolByPath API.
func (c *Connector) volPoolAndSize(volPath string) (pool string, sizeGB int64) {
	if volPath == "" {
		return "", 0
	}
	// c.conn is nil between Close() and a successful Open(), which is
	// exactly the window a libvirtd restart opens up. The go-libvirt
	// binding dereferences the receiver without a guard, so calling
	// through a nil connection is a SIGSEGV that takes the whole
	// backend down — not a returned error. Pool name and size are
	// decorative here, so degrade to "unknown" instead.
	if c == nil || c.conn == nil {
		return "", 0
	}
	vol, err := c.conn.LookupStorageVolByPath(volPath)
	if err != nil {
		return "", 0
	}
	defer vol.Free()
	poolObj, err := vol.LookupPoolByVolume()
	if err != nil {
		return "", 0
	}
	defer poolObj.Free()
	pool, _ = poolObj.GetName()
	info, err := vol.GetInfo()
	if err == nil && info.Capacity > 0 {
		sizeGB = int64(info.Capacity / (1024 * 1024 * 1024))
	}
	return pool, sizeGB
}

// diskPoolOfDomain inspects the domain's attached disks to discover the storage pool
// holding the instance's active disk image. If the pool cannot be determined, it
// falls back to the host's default disk pool (c.DiskPoolName()).
func (c *Connector) diskPoolOfDomain(dom *libvirt.Domain) string {
	fallback := c.DiskPoolName()
	if dom == nil || c == nil || c.conn == nil {
		return fallback
	}
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fallback
	}
	disks := c.parseDisksFiltered(xmlDesc, true)
	pools, _ := c.ListStoragePools()
	for _, d := range disks {
		if d.Pool != "" {
			return d.Pool
		}
		if d.Source != "" {
			if pool, _ := c.volPoolAndSize(d.Source); pool != "" {
				return pool
			}
			// Fallback: match by pool filesystem directory if libvirt's in-memory
			// volume cache hasn't indexed the newly-created overlay file yet.
			cleanSrc := filepath.Clean(d.Source)
			for _, p := range pools {
				if p.Path != "" {
					cleanPool := filepath.Clean(p.Path)
					if cleanSrc == cleanPool || strings.HasPrefix(cleanSrc, cleanPool+"/") {
						return p.Name
					}
				}
			}
		}
	}
	return fallback
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

// GetDomainLog reads the tail of the domain's QEMU hypervisor log
// from /var/log/libvirt/qemu/<name>.log. Lines specifies how many
// trailing lines to return (default 200, capped at 2000).
func (c *Connector) GetDomainLog(id string, lines int) (string, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return "", err
	}
	name, err := dom.GetName()
	dom.Free()
	if err != nil {
		return "", fmt.Errorf("get domain name: %w", err)
	}

	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}

	logPath := filepath.Join("/var/log/libvirt/qemu", name+".log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("(No hypervisor log file found at %s)", logPath), nil
		}
		return "", fmt.Errorf("read domain log: %w", err)
	}

	allLines := strings.Split(string(data), "\n")
	if len(allLines) > lines {
		allLines = allLines[len(allLines)-lines:]
	}
	return strings.Join(allLines, "\n"), nil
}

// The Phase II cleanup removed the sparse-aware tar helpers
// (writeTarFile, writeTarFileSparse, writeDiskToTar, seekData,
// seekHole, lseekWhence, writeTarRemainder, whenceData, whenceHole).
// They were used only by the old inline ExportDomain producer.
// The new ExportDomain is a thin shim over
// internal/backupstore.ProduceVMArchive, which has its own
// (sparse-aware) writeTarFileSparse in producer.go. The OVA
// path still uses writeTarEntry above.
