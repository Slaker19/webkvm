package libvirt

import (
	"archive/tar"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"webkvm/internal/backupstore"
	"webkvm/internal/hostcaps"
	"webkvm/internal/imgsafe"
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

// CloneDomain duplicates a domain into a new one named req.Name. The
// source VM must be shut off — a running qcow2 file can't be safely
// read by qemu-img while QEMU holds the write lock, and a copy taken
// mid-write would be corrupt.
//
// Every <disk device='disk'> is cloned (not just the first one, the
// historical behavior): each gets its own new file in req.Pool (or
// the default disk pool), named "<newName>[-N].qcow2".
//
//   - req.Linked=true: the new disk is a qcow2 overlay with the
//     source disk as backing file (qemu-img create -b/-F). Near
//     instant, minimal extra space, but the clone depends on the
//     source disk continuing to exist — deleting/moving/resizing the
//     source breaks it. Only valid for qcow2 sources.
//   - req.Linked=false (default): a fully independent copy via
//     qemu-img convert. Slower, uses full disk space, no dependency.
func (c *Connector) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return models.VM{}, err
	}
	defer dom.Free()

	if state, _, serr := dom.GetState(); serr == nil && state != libvirt.DOMAIN_SHUTOFF {
		return models.VM{}, ErrDomainMustBeStoppedToClone
	}

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		xmlDesc, _ = dom.GetXMLDesc(0)
	}
	if xmlDesc == "" {
		return models.VM{}, fmt.Errorf("cannot get domain XML")
	}

	newUUID := uuid.New().String()
	newName := req.Name
	if newName == "" || strings.Contains(newName, "/") || strings.Contains(newName, "\\") || strings.Contains(newName, "..") {
		return models.VM{}, fmt.Errorf("invalid VM name %q", newName)
	}

	xmlDesc = regexp.MustCompile(`<name>[^<]+</name>`).ReplaceAllString(xmlDesc, "<name>"+xmlEscape(newName)+"</name>")
	xmlDesc = regexp.MustCompile(`<uuid>[^<]+</uuid>`).ReplaceAllString(xmlDesc, "<uuid>"+newUUID+"</uuid>")
	xmlDesc = regexp.MustCompile(`<title>[^<]*</title>`).ReplaceAllString(xmlDesc, "")
	// Remove MAC addresses so libvirt generates new ones
	xmlDesc = regexp.MustCompile(`<mac\b[^/]*/>\s*`).ReplaceAllString(xmlDesc, "")
	// Remove NVRAM path — libvirt regenerates it from the template on
	// first define of a domain that has none.
	xmlDesc = regexp.MustCompile(`<nvram>[^<]*</nvram>`).ReplaceAllString(xmlDesc, "")

	if req.Network != "" {
		xmlDesc = regexp.MustCompile(`(<source\s+network=)['"][^'"]+['"]`).
			ReplaceAllString(xmlDesc, "${1}'"+xmlEscape(req.Network)+"'")
	}

	poolName := req.Pool
	if poolName == "" {
		poolName = c.DiskPoolName()
	}
	poolPath, err := c.GetPoolPath(poolName)
	if err != nil {
		return models.VM{}, fmt.Errorf("get pool path: %w", err)
	}

	// Clone every <disk device='disk'> block, not just the first —
	// VMs with a secondary data disk used to silently lose it on
	// clone (the new domain referenced the *original*'s second disk
	// file, sharing writable state between two running domains).
	diskRe := regexp.MustCompile(`<disk\b[^>]+device='disk'[^>]*>[\s\S]*?<source\b[^>]*file='([^']+)'[^>]*/>[\s\S]*?</disk>`)
	matches := diskRe.FindAllStringSubmatch(xmlDesc, -1)

	clonedPaths := make([]string, 0, len(matches))
	cleanupAll := func() {
		for _, p := range clonedPaths {
			_ = os.Remove(p)
		}
	}

	for i, dm := range matches {
		fullMatch := dm[0]
		oldDiskPath := dm[1]

		suffix := ""
		if i > 0 {
			suffix = fmt.Sprintf("-%d", i+1)
		}
		newDiskName := newName + suffix + ".qcow2"
		newDiskPath := filepath.Join(poolPath, newDiskName)
		if rel, rerr := filepath.Rel(poolPath, newDiskPath); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			cleanupAll()
			return models.VM{}, fmt.Errorf("invalid disk path")
		}

		originalFormat := "qcow2"
		if fm := regexp.MustCompile(`<driver[^>]*type='([^']+)'`).FindStringSubmatch(fullMatch); len(fm) > 1 {
			originalFormat = fm[1]
		}

		if err := cloneDiskFile(oldDiskPath, newDiskPath, originalFormat, req.Linked); err != nil {
			cleanupAll()
			return models.VM{}, fmt.Errorf("clone disk %s: %w", filepath.Base(oldDiskPath), err)
		}
		clonedPaths = append(clonedPaths, newDiskPath)

		// Only the first occurrence of oldDiskPath is replaced so
		// disks that happen to share a source file (rare, but not
		// impossible with hand-edited XML) don't get double-replaced.
		xmlDesc = strings.Replace(xmlDesc, oldDiskPath, newDiskPath, 1)
	}

	if len(clonedPaths) > 0 {
		if pool, perr := c.conn.LookupStoragePoolByName(poolName); perr == nil {
			if rerr := pool.Refresh(0); rerr != nil {
				slog.Warn("clone_pool_refresh_failed", "pool", poolName, "err", rerr.Error())
			}
			pool.Free()
		}
	}

	newDom, err := c.defineDomain(xmlDesc, TrustedDomain)
	if err != nil {
		cleanupAll()
		return models.VM{}, fmt.Errorf("define cloned domain: %w", err)
	}
	defer newDom.Free()

	return c.domainToVM(newDom)
}

// cloneDiskFile creates dstPath as a copy of srcPath. When linked is
// true and format is qcow2, it creates a copy-on-write overlay
// backed by srcPath (near-instant); otherwise it makes a fully
// independent copy with qemu-img convert (linked is silently ignored
// for non-qcow2 sources, since raw disks have no backing-file
// support).
func cloneDiskFile(srcPath, dstPath, format string, linked bool) error {
	if strings.Contains(srcPath, "..") || strings.Contains(dstPath, "..") {
		return fmt.Errorf("invalid disk path: traversal not allowed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if linked && format == "qcow2" {
		cmd := exec.CommandContext(ctx, "qemu-img", "create",
			"-f", "qcow2", "-F", "qcow2", "-b", srcPath, dstPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("qemu-img create (linked): %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	cmd := exec.CommandContext(ctx, "qemu-img", "convert", "-O", format, "-q", srcPath, dstPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(dstPath)
		return fmt.Errorf("qemu-img convert: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// autostartEnabledOrFalse is the best-effort autostart probe used
// inside domainToVM. libvirt's GetAutostart returns an error for
// transient states (e.g. the domain is in the middle of shutting
// off), and the rest of domainToVM already plows through the
// domain's data with the same resilience — a missing autostart
// field on a one-frame stale listing is far less disruptive than
// failing the entire VM list because one VM is mid-transition.
// Operators who care about the live value can still hit
// GET /api/vms/{id}/autostart, which uses GetDomainAutostart and
// surfaces the error verbatim.
func autostartEnabledOrFalse(dom *libvirt.Domain) bool {
	enabled, err := dom.GetAutostart()
	if err != nil {
		return false
	}
	return enabled
}

// ExportBackupOptions controls the existing WebKVM backup format
// (tar with domain.xml + disk files). The legacy on-disk format is
// preserved when Compress is "gzip"; new exports default to zstd for
// better ratio and faster throughput.
type ExportBackupOptions struct {
	Compress    string // "gzip" (legacy) or "zstd" (default for new exports)
	ZstdLevel   int    // 1..22, default 19
	RepackDisks bool   // if true, run qemu-img convert -c on each disk first
}

// ExportDomain streams a backup of the domain (XML + disk files) to w.
// By default it uses zstd compression (much faster + better ratio than
// gzip); pass opts.Compress="gzip" for backward compatibility with
// archives produced by older versions of WebKVM.
//
// The output is a tar containing domain.xml, optional re-packed
// qcow2 files in disks/, and a manifest.txt. When opts.RepackDisks
// is true, each disk is re-packed with `qemu-img convert -c -O qcow2`
// (cluster packing) which usually shrinks the backup by 20-50%.
// qemu-img writes to stdout and the bytes are piped straight into the
// tar — no temporary file is ever created on disk.
//
// Phase II: this function is now a thin shim over the shared
// producer in internal/backupstore. The bytes it produces are
// byte-for-byte identical to what the backup runner stores
// for a per-VM tar (the runner and the browser export use the
// same ProduceVMArchive). The libvirt-specific glue — domain
// lookup, XML fetch, disk validation, repack-via-qemu-img —
// stays here; only the tar+compress step moved.
//
// The VM must be shut off before exporting.
func (c *Connector) ExportDomain(ctx context.Context, id string, opts ExportBackupOptions, w io.Writer) (backupstore.ProducerResult, error) {
	if err := c.ensureConnected(); err != nil {
		return backupstore.ProducerResult{}, err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return backupstore.ProducerResult{}, err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return backupstore.ProducerResult{}, fmt.Errorf("get xml: %w", err)
	}

	if err := c.validateDisksReadable(xmlDesc); err != nil {
		return backupstore.ProducerResult{}, err
	}

	disks := c.parseDisksFiltered(xmlDesc, true)
	vmBackup := backupstore.VMBackup{
		ID:        id,
		DomainXML: xmlDesc,
		Disks:     disks,
	}
	producerOpts := backupstore.ProducerOpts{
		Compression:   opts.Compress,
		ZstdLevel:     opts.ZstdLevel,
		RepackDisks:   opts.RepackDisks,
		DiskSizeLimit: 0, // export has no size cap (the request body can be huge)
	}
	return backupstore.ProduceVMArchive(ctx, vmBackup, producerOpts, w)
}

// resolveUniqueDomainName returns name if no existing domain has it, or
// "<name>-N" (incrementing N from 1) until a free name is found. Used by
// the import paths so a re-imported VM never collides with the original.
func (c *Connector) resolveUniqueDomainName(name string) (string, error) {
	if err := c.ensureConnected(); err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("empty name")
	}
	// Every successful lookup returns a virDomainPtr that holds a
	// reference in libvirtd until it is freed. Discarding it with `_`
	// leaked one object per probe — and this loop probes up to 1000
	// times, so importing repeatedly into a namespace with many
	// same-prefixed VMs grew the backend's RSS and libvirtd's
	// reference table without bound.
	taken := func(n string) bool {
		dom, err := c.conn.LookupDomainByName(n)
		if err != nil {
			return false
		}
		dom.Free()
		return true
	}
	if !taken(name) {
		// Not found: original is fine.
		return name, nil
	}
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if !taken(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a free name after 1000 attempts starting at %s", name)
}

// DomainExists reports whether a domain with exactly this name is already
// defined, regardless of power state. Same lookup semantics as
// resolveUniqueDomainName: a failed LookupDomainByName means the name is
// free. Used by the appliance deploy pre-flight so a deployment is
// rejected BEFORE any image I/O can overwrite an existing disk.
func (c *Connector) DomainExists(name string) (bool, error) {
	if err := c.ensureConnected(); err != nil {
		return false, err
	}
	if name == "" {
		return false, fmt.Errorf("empty name")
	}
	dom, err := c.conn.LookupDomainByName(name)
	if err == nil {
		// Free the reference: the caller only wants the boolean, and
		// leaking a virDomainPtr per call kept the domain object (and
		// its XML) pinned in libvirtd for the life of the backend.
		dom.Free()
		return true, nil
	}
	return false, nil
}

// normalizeMachineType rewrites versioned machine attributes (e.g.
// "pc-q35-10.2", "pc-i440fx-7.1") to the short canonical form
// ("q35", "pc") so libvirt resolves them to whatever the destination
// qemu actually supports. Without this, a VM exported from a host with
// a newer qemu (e.g. 10.2.x) will fail to import on a host with an
// older qemu (e.g. 10.0.x) because the destination qemu does not know
// the versioned machine type. Idempotent: re-applying it is a no-op.
//
// Handles both single-quoted ('machine=...') and double-quoted
// ("machine=...") attribute syntax, and both major chipsets.
func normalizeMachineType(xmlStr string) string {
	xmlStr = regexp.MustCompile(`machine='pc-q35-[0-9]+(\.[0-9]+)*'`).ReplaceAllString(xmlStr, "machine='q35'")
	xmlStr = regexp.MustCompile(`machine='pc-i440fx-[0-9]+(\.[0-9]+)*'`).ReplaceAllString(xmlStr, "machine='pc'")
	xmlStr = regexp.MustCompile(`machine="pc-q35-[0-9]+(\.[0-9]+)*"`).ReplaceAllString(xmlStr, `machine="q35"`)
	xmlStr = regexp.MustCompile(`machine="pc-i440fx-[0-9]+(\.[0-9]+)*"`).ReplaceAllString(xmlStr, `machine="pc"`)
	return xmlStr
}

// ImportDomain restores a VM from a WebKVM backup produced by ExportDomain.
// The archive may be gzip (.tar.gz) or zstd (.tar.zst) compressed, or a
// plain tar. Disk files from the archive are placed into the requested
// pool (or "default").
//
// Returns the new VM's uuid, resolved name, and a list of non-fatal
// warnings (e.g. CDROM source files that do not exist on the
// destination — the VM can be defined but will fail to start until
// the user uploads the missing ISO). An empty warning slice means a
// clean import.
// ImportOpts carries optional overrides applied while importing a
// VM from a backup archive (or OVA). Zero values mean "keep whatever
// is inside the archive" — the behaviour before these options
// existed. Autostart is a pointer so callers can distinguish
// "don't touch" (nil) from "explicitly set false" (*false).
type ImportOpts struct {
	// Network rewrites every <source network='...'> on the VM's
	// interfaces. Empty string keeps the archive's network.
	Network string
	// VCPUs overrides the <vcpu> element. 0 keeps the archive's.
	VCPUs int
	// RAMMB overrides <memory> (KiB in the XML). 0 keeps it.
	RAMMB int
	// Autostart sets libvirt's domain autostart flag after the
	// VM is defined. nil leaves the libvirt default untouched.
	Autostart *bool
	// SourceSize is the compressed archive size in bytes. Used as
	// the denominator when reporting disk-copy progress.
	SourceSize int64
	// Admin is true when the caller is an administrator. Only then are the
	// RBAC-relevant metadata fields (tags, groups) of the archive carried over.
	Admin bool
	// OnProgress is invoked with a 0-100 percentage, a stable stage
	// code (restore_copying, restore_define, done...) and its
	// interpolation vars as the import progresses. Optional.
	OnProgress func(pct int, stage string, vars map[string]any)
}

func (o ImportOpts) report(pct int, stage string, vars map[string]any) {
	if o.OnProgress != nil {
		o.OnProgress(pct, stage, vars)
	}
}

// countingReader wraps an io.Reader and reports the cumulative
// percentage of bytes read against a known total. Used to give the
// UI a live bar while the (multi-GB) disk bodies stream out of the
// archive into the pool. Reporting is rate-limited to whole
// percentage points so UpdateJob isn't hammered thousands of times.
type countingReader struct {
	r     io.Reader
	total int64
	seen  int64
	base  int
	span  int
	last  int
	on    func(pct int)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.seen += int64(n)
	if c.total > 0 {
		pct := c.base + int(float64(c.seen)/float64(c.total)*float64(c.span))
		if pct > c.base+c.span {
			pct = c.base + c.span
		}
		if pct > c.last {
			c.last = pct
			c.on(pct)
		}
	}
	return n, err
}

func (c *Connector) ImportDomain(tarPath, newName, poolName string, opts ImportOpts) (string, string, []string, error) {
	if err := c.ensureConnected(); err != nil {
		return "", "", nil, err
	}
	if poolName == "" {
		poolName = c.DiskPoolName()
	}
	poolPath, err := c.GetPoolPath(poolName)
	if err != nil {
		return "", "", nil, fmt.Errorf("get pool path: %w", err)
	}

	// Auto-detect gzip/zstd/plain tar. Reuse the OVA stream opener
	// — it does the same magic-byte sniffing we need here.
	raw, cleanup, err := openOVAStream(tarPath)
	if err != nil {
		return "", "", nil, err
	}
	defer cleanup()
	tr := tar.NewReader(raw)

	var xmlBytes []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", nil, err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		// We only need domain.xml from the first pass. Disk bodies are
		// streamed straight to the pool in a second pass; loading them
		// into a []byte would OOM the process on multi-GB imports.
		if hdr.Name == "domain.xml" {
			xmlBytes, err = readAllLimited(tr, maxDomainXMLBytes)
			if err != nil {
				return "", "", nil, err
			}
		}
	}
	if len(xmlBytes) == 0 {
		return "", "", nil, fmt.Errorf("archive missing domain.xml")
	}
	opts.report(5, "restore_copying", nil)

	xmlStr := string(xmlBytes)
	// Strip the original UUID and NVRAM path so libvirt generates
	// fresh ones for the imported VM. Otherwise DomainDefineXML fails
	// when the original VM still exists, and the new VM would share
	// the original's NVRAM file.
	xmlStr = regexp.MustCompile(`(?s)<uuid>[^<]*</uuid>\n?`).ReplaceAllString(xmlStr, "")
	xmlStr = regexp.MustCompile(`<nvram\s+([^>]*)>[^<]*</nvram>`).ReplaceAllString(xmlStr, "<nvram $1/>")
	if newName != "" {
		xmlStr = regexp.MustCompile(`<mac\s+address=['"][^'"]*['"]\s*/>`).ReplaceAllString(xmlStr, "")
	}

	// Determine the effective name: caller-supplied, else <name> in the archive.
	effectiveName := newName
	if effectiveName == "" {
		if m := regexp.MustCompile(`<name>([^<]*)</name>`).FindStringSubmatch(xmlStr); len(m) > 1 {
			effectiveName = m[1]
		}
	}
	if effectiveName == "" {
		return "", "", nil, fmt.Errorf("archive missing domain name")
	}
	// The name may come from the uploaded archive's <name> element, not
	// just from the (already validated) caller-supplied newName. It is
	// later used in filesystem paths (the NVRAM "/<name>_VARS.fd"
	// rewrite below), so reject anything that could escape the pool
	// directory or inject XML before it is used.
	if !nameRE.MatchString(effectiveName) {
		return "", "", nil, fmt.Errorf("invalid domain name in archive %q (allowed: A-Z a-z 0-9 . _ -, max 63 chars)", effectiveName)
	}
	resolvedName, err := c.resolveUniqueDomainName(effectiveName)
	if err != nil {
		return "", "", nil, err
	}
	// resolveUniqueDomainName may append "-N"; re-validate the final value
	// so nothing that skips nameRE (e.g. a caller-supplied name) reaches
	// the NVRAM path rewrite.
	if !nameRE.MatchString(resolvedName) {
		return "", "", nil, fmt.Errorf("invalid resolved domain name %q", resolvedName)
	}
	// Always rewrite the <name> in the XML to the resolved value (handles
	// both caller-supplied and archive-supplied names, and the rename case).
	// xmlEscape guards against a caller- or archive-supplied name breaking
	// out of the <name> element (XML/QEMU-arg injection).
	re := regexp.MustCompile(`<name>[^<]*</name>`)
	if loc := re.FindStringIndex(xmlStr); loc != nil {
		xmlStr = xmlStr[:loc[0]] + "<name>" + xmlEscape(resolvedName) + "</name>" + xmlStr[loc[1]:]
	}
	// Also update the title if present.
	titleRe := regexp.MustCompile(`<title>[^<]*</title>`)
	if loc := titleRe.FindStringIndex(xmlStr); loc != nil {
		xmlStr = xmlStr[:loc[0]] + "<title>" + xmlEscape(resolvedName) + "</title>" + xmlStr[loc[1]:]
	}
	// Update NVRAM file hint if the name appears there.
	xmlStr = regexp.MustCompile(`<nvram[^>]*template[^>]*/>`).ReplaceAllStringFunc(xmlStr, func(m string) string {
		return regexp.MustCompile(`/[^/]+_VARS\.fd`).ReplaceAllString(m, "/"+resolvedName+"_VARS.fd")
	})

	// Normalize the machine type to the short canonical form so libvirt
	// resolves it to whatever the destination's qemu actually supports.
	// The exporter may have been a host with a newer qemu (e.g. pc-q35-10.2
	// on a 10.2.x build) and the importer's qemu (e.g. 10.0.x) does not
	// know that version. "q35" / "pc" are the short forms that libvirt
	// always resolves to the latest available machine type for the chipset.
	xmlStr = normalizeMachineType(xmlStr)

	// Optional overrides: rewiring the NICs to a different libvirt
	// network and/or overriding the vCPU / RAM allocation. Each is a
	// no-op when the caller leaves the corresponding field zeroed.
	// The regexes are safe: `<source network=` only ever appears on
	// <interface> elements (disk sources use `file=`), and the
	// vcpu/memory tags are single-element in libvirt XML.
	if opts.Network != "" {
		xmlStr = regexp.MustCompile(`<source\s+network=['"][^'"]*['"]\s*/>`).
			ReplaceAllString(xmlStr, "<source network='"+xmlEscape(opts.Network)+"'/>")
	}
	if opts.VCPUs > 0 {
		xmlStr = regexp.MustCompile(`<vcpu[^>]*>[\s\S]*?</vcpu>`).
			ReplaceAllString(xmlStr, fmt.Sprintf("<vcpu placement='static'>%d</vcpu>", opts.VCPUs))
	}
	if opts.RAMMB > 0 {
		xmlStr = regexp.MustCompile(`<memory[^>]*>[\s\S]*?</memory>`).
			ReplaceAllString(xmlStr, fmt.Sprintf("<memory unit='KiB'>%d</memory>", opts.RAMMB*1024))
	}

	// Build the set of disk basenames the XML actually references, so
	// the second pass only writes disks that will be used (avoids
	// orphan files in the pool when an archive carries extras).
	diskRe := regexp.MustCompile(`<source\s+file=(["'])([^"']+)(["'])([^/>]*)/>`)
	referenced := map[string]bool{}
	for _, m := range diskRe.FindAllStringSubmatch(xmlStr, -1) {
		if len(m) >= 3 {
			referenced[filepath.Base(m[2])] = true
		}
	}

	// Second pass: re-open the archive and stream every referenced disk
	// straight from the tar body to its destination in the pool. This
	// avoids the multi-GB []byte allocation that used to OOM-kill the
	// backend on large imports.
	raw2, cleanup2, err := openOVAStream(tarPath)
	if err != nil {
		return "", "", nil, err
	}
	defer cleanup2()
	// Wrap the stream so the UI gets a live percentage while the
	// disk bodies stream into the pool. total = compressed size.
	if opts.SourceSize > 0 && opts.OnProgress != nil {
		raw2 = &countingReader{
			r:     raw2,
			total: opts.SourceSize,
			base:  5,
			span:  85,
			on: func(pct int) {
				opts.report(pct, "restore_copying", nil)
			},
		}
	}
	tr2 := tar.NewReader(raw2)
	diskWritten := map[string]string{} // arcName -> dst path

	// The destination basename is the new VM name, with a
	// counter suffix on collision. The first disk lands as
	// "<newName>.qcow2"; subsequent disks (or re-imports) land
	// as "<newName>-2.qcow2", "<newName>-3.qcow2", etc.
	// The arcName is preserved in diskWritten so the post-loop
	// XML rewrite can map each <source file='...'> reference
	// to its new on-disk path.
	diskCounter := 0
	budget := newDiskBudget(poolPath)

	for {
		hdr, err := tr2.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", nil, err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		if !strings.HasPrefix(hdr.Name, "disks/") {
			continue
		}
		base := filepath.Base(hdr.Name)
		if !referenced[base] {
			// Skip disk bodies not referenced by the XML.
			continue
		}
		var dst string
		var derr error
		// Use resolvedName (not newName) so the disk file
		// matches the actual VM name libvirt will register.
		// If the caller asked for "myvm" and a "myvm"
		// already exists, resolveUniqueDomainName returns
		// "myvm-1"; the disk should also be "myvm-1.qcow2"
		// to keep the VM and its disk in sync.
		if diskCounter == 0 {
			dst, derr = VMDiskPath(poolPath, resolvedName, ".qcow2")
		} else {
			// Subsequent disks of the same VM: use a counter
			// suffix derived from the import order. The
			// collision counter is global within this import
			// (it doesn't re-scan the pool between disks), so
			// the second disk becomes "<resolvedName>-2.qcow2",
			// the third "-3", etc.
			probe := fmt.Sprintf("%s-%d", resolvedName, diskCounter+1)
			dst, derr = VMDiskPath(poolPath, probe, ".qcow2")
		}
		if derr != nil {
			return "", "", nil, fmt.Errorf("resolve disk path: %w", derr)
		}
		if !strings.HasPrefix(filepath.Clean(dst), filepath.Clean(poolPath)+string(os.PathSeparator)) {
			return "", "", nil, fmt.Errorf("invalid disk path %q", dst)
		}
		if err := budget.take(hdr.Size); err != nil {
			return "", "", nil, err
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return "", "", nil, fmt.Errorf("create disk %s: %w", dst, err)
		}
		if _, err := copyLimited(f, tr2, hdr.Size); err != nil {
			f.Close()
			return "", "", nil, fmt.Errorf("write disk %s: %w", dst, err)
		}
		if err := f.Close(); err != nil {
			return "", "", nil, fmt.Errorf("close disk %s: %w", dst, err)
		}
		diskWritten[hdr.Name] = dst
		diskCounter++
	}

	// Re-register the new files with libvirt. We wrote them directly
	// to the pool directory (to avoid a multi-GB []byte allocation),
	// which bypasses the volume registry. Without a refresh, the
	// files are "ghost" disks — visible on disk, referenced by the
	// domain XML, but invisible to `virsh vol-list` and to
	// /api/storage/volumes. One refresh at the end of the write
	// loop picks up every disk we just wrote; subsequent calls
	// within the same import would be no-ops.
	if len(diskWritten) > 0 {
		if pool, err := c.conn.LookupStoragePoolByName(poolName); err == nil {
			if rerr := pool.Refresh(0); rerr != nil {
				// Not fatal: the domain definition below will
				// still succeed (libvirt does not require a
				// volume entry to use a file in a dir pool).
				// Log so the operator can manually refresh
				// if the vol listing stays empty.
				slog.Warn("import_pool_refresh_failed",
					"pool", poolName, "err", rerr.Error())
			}
			pool.Free()
		} else {
			slog.Warn("import_pool_lookup_failed",
				"pool", poolName, "err", err.Error())
		}
	}

	xmlStr = diskRe.ReplaceAllStringFunc(xmlStr, func(match string) string {
		sm := diskRe.FindStringSubmatch(match)
		if len(sm) < 5 {
			return match
		}
		base := filepath.Base(sm[2])
		dst, ok := diskWritten["disks/"+base]
		if !ok {
			return match
		}
		return "<source file=" + sm[1] + dst + sm[3] + sm[4] + "/>"
	})

	// Strip every <disk type='file' device='cdrom'>...</disk>
	// block from the XML before defining the domain. The exporter
	// captured the CDROM source path verbatim, but that path is
	// almost certainly wrong on the destination host (different
	// pool, different basename because of upload-time dedup, or
	// the user simply never re-uploaded the ISO). Leaving it in
	// would make the import succeed but the VM refuse to start
	// with a hard-to-diagnose "Cannot access storage file" error.
	//
	// Stripping is a stronger guarantee than warning: the VM is
	// always startable after a clean import, and the user can
	// attach any ISO via the Storage page + Add Disk dialog.
	// We match the full <disk ...>...</disk> block (with `(?s)`
	// so the dot matches newlines) and only when the device
	// attribute is 'cdrom' — disk devices and any other device
	// types are left alone.
	cdromBlockRe := regexp.MustCompile(`(?s)<disk\s+type='file'\s+device='cdrom'\s*>\s*.*?</disk>`)
	stripped := cdromBlockRe.FindAllString(xmlStr, -1)
	xmlStr = cdromBlockRe.ReplaceAllString(xmlStr, "")

	var warnings []string
	if len(stripped) > 0 {
		// Build a short, single warning that lists the paths we
		// dropped, so the operator knows what they need to
		// re-attach if they want boot-from-ISO to work.
		pathRe := regexp.MustCompile(`<source\s+file='([^']+)'`)
		var paths []string
		for _, blk := range stripped {
			if m := pathRe.FindStringSubmatch(blk); len(m) >= 2 {
				paths = append(paths, filepath.Base(m[1]))
			}
		}
		switch {
		case len(paths) == 1:
			warnings = append(warnings, fmt.Sprintf("CDROM device was discarded on import (source: %s). Attach an ISO via the Storage page → Add Disk if you want boot-from-ISO.", paths[0]))
		default:
			warnings = append(warnings, fmt.Sprintf("%d CDROM devices were discarded on import (%s). Attach ISOs via the Storage page → Add Disk if you want boot-from-ISO.", len(paths), strings.Join(paths, ", ")))
		}
	}

	opts.report(90, "restore_define", nil)
	// The XML comes from the archive: it is untrusted for EVERY role. It is
	// validated, re-serialised and only disks extracted above may be
	// referenced (audit #2).
	allowedDisks := make(map[string]bool, len(diskWritten))
	for _, dst := range diskWritten {
		allowedDisks[dst] = true
	}
	dom, err := c.defineDomain(xmlStr, DomainPolicy{AllowedDiskFiles: allowedDisks})
	if err != nil {
		// Do not leave extracted disks behind for a definition we refused.
		for _, dst := range diskWritten {
			_ = os.Remove(dst)
		}
		return "", "", nil, fmt.Errorf("define domain: %w", err)
	}
	defer dom.Free()
	uuid, err := dom.GetUUIDString()
	if err != nil {
		return "", "", nil, err
	}

	// The sanitiser dropped <metadata>; carry over only the descriptive
	// fields (alias, notes...). Ownership and template/shared flags are never
	// taken from an archive. Best effort.
	if meta, ok := archiveMeta(xmlBytes, opts.Admin); ok {
		if merr := c.SetVMMeta(uuid, meta); merr != nil {
			slog.Warn("import_meta_restore_failed", "uuid", uuid, "err", merr)
		}
	}

	// Optional autostart override. The VM is already defined, so a
	// failure here is non-fatal — log it but keep the import result.
	if opts.Autostart != nil {
		if err := dom.SetAutostart(*opts.Autostart); err != nil {
			slog.Warn("import_autostart_failed",
				"uuid", uuid, "autostart", *opts.Autostart, "err", err.Error())
		}
	}

	// Third pass: restore snapshot metadata and overlay volumes from
	// the archive. This is best-effort — failures produce a warning
	// but do not abort the import (the VM is already defined and the
	// active disk is usable without the snapshot history).
	opts.report(95, "restore_define", nil)
	snapWarn := c.restoreSnapshots(tarPath, resolvedName, poolPath, dom)
	if snapWarn != "" {
		warnings = append(warnings, snapWarn)
	}
	// Gate C (audit #6): the extracted disks, and the snapshot chain just
	// rebuilt behind them, must not reference files outside the pool
	// (qcow2 backing file / data-file pointing at host files).
	if cerr := verifyImportedDisks(dom, poolPath, diskWritten); cerr != nil {
		_ = dom.Undefine()
		for _, dst := range diskWritten {
			_ = os.Remove(dst)
		}
		return "", "", nil, fmt.Errorf("imported disk rejected: %w", cerr)
	}
	opts.report(100, "done", nil)

	return uuid, resolvedName, warnings, nil
}

// verifyImportedDisks checks every disk this import wrote: qcow2 images must
// have a backing chain that stays inside poolPath and no external data-file;
// other formats are inspected for extents pointing outside the image.
func verifyImportedDisks(dom *libvirt.Domain, poolPath string, written map[string]string) error {
	if len(written) == 0 || !imgsafe.Available() {
		return nil
	}
	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return fmt.Errorf("read back definition: %w", err)
	}
	root, err := parseStrict([]byte(xmlDesc))
	if err != nil {
		// libvirt's own output may carry namespaced metadata; fall back to
		// checking every written file as qcow2-or-probed.
		root = nil
	}
	formats := map[string]string{}
	if root != nil {
		if devs := root.child("devices"); devs != nil {
			for _, d := range devs.Kids {
				if d.Name != "disk" {
					continue
				}
				src, drv := d.child("source"), d.child("driver")
				if src == nil || drv == nil {
					continue
				}
				f, _ := src.attr("file")
				formats[f] = mustAttr(drv, "type")
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, dst := range written {
		switch f := formats[dst]; f {
		case "raw":
			continue
		case "", "qcow2":
			if err := imgsafe.CheckChainInside(ctx, dst, f, poolPath); err != nil {
				return fmt.Errorf("%s: %w", filepath.Base(dst), err)
			}
		default:
			if _, err := imgsafe.Inspect(ctx, dst, f); err != nil {
				return fmt.Errorf("%s: %w", filepath.Base(dst), err)
			}
		}
	}
	return nil
}

// writeArchiveBody streams an archive entry (r) straight to path
// without buffering it in memory. Snapshot overlays / base disks can
// be gigabytes; io.ReadAll on them would OOM the backend.
func writeArchiveBody(r io.Reader, path string, max int64) error {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) || strings.Contains(cleaned, "..") {
		return fmt.Errorf("invalid archive path %q", path)
	}
	f, err := os.Create(cleaned)
	if err != nil {
		return err
	}
	if _, cerr := copyLimited(f, r, max); cerr != nil {
		_ = f.Close()
		_ = os.Remove(cleaned)
		return cerr
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(cleaned)
		return cerr
	}
	return nil
}

// restoreSnapshots reads snapshot entries from a backup archive,
// copies overlay volumes to the pool, fixes the qcow2 backing chain,
// and recreates each snapshot via DomainSnapshotCreateXML with the
// REDEFINE flag. Returns a non-fatal warning string, or empty string
// on success.
func (c *Connector) restoreSnapshots(tarPath, resolvedName, poolPath string, dom *libvirt.Domain) string {
	raw3, cleanup3, err := openOVAStream(tarPath)
	if err != nil {
		return fmt.Sprintf("restore snapshots: open archive: %v", err)
	}
	defer cleanup3()

	tr3 := tar.NewReader(raw3)
	snapBudget := newDiskBudget(poolPath)

	// Collect snapshot data from the archive.
	type snapRestore struct {
		xmlBytes      []byte
		volumeArcName string // archive entry name for the overlay file
		snapName      string // snapshot name (e.g. "snap1")
	}
	snapshots := map[string]*snapRestore{} // snapName → restore data
	var baseVolumeArc string               // "_base" volume archive entry
	var restoreOrder []string              // snapshot names in archive order

	for {
		hdr, err := tr3.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Sprintf("restore snapshots: read tar: %v", err)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		if !strings.HasPrefix(hdr.Name, "snapshots/") || hdr.Name == "snapshots/" {
			continue
		}
		rest := strings.TrimPrefix(hdr.Name, "snapshots/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) < 1 || parts[0] == "" {
			continue
		}
		snapName := parts[0]
		if snapName != "_base" && !nameRE.MatchString(strings.TrimSuffix(snapName, ".xml")) {
			continue // never let an archive path component shape a pool filename
		}

		if snapName == "_base" {
			// Root base disk of the backing chain.
			if len(parts) == 2 {
				basePath := filepath.Join(poolPath, resolvedName+".base")
				if werr := snapBudget.take(hdr.Size); werr != nil {
					return fmt.Sprintf("restore snapshots: %v", werr)
				}
				if werr := writeArchiveBody(tr3, basePath, hdr.Size); werr != nil {
					return fmt.Sprintf("restore snapshots: write _base: %v", werr)
				}
				baseVolumeArc = basePath
			}
			continue
		}

		if len(parts) == 1 && strings.HasSuffix(parts[0], ".xml") {
			// Snapshot metadata XML.
			body, rerr := readAllLimited(tr3, maxDomainXMLBytes)
			if rerr != nil {
				return fmt.Sprintf("restore snapshots: read %s: %v", hdr.Name, rerr)
			}
			name := strings.TrimSuffix(snapName, ".xml")
			if _, ok := snapshots[name]; !ok {
				snapshots[name] = &snapRestore{snapName: name}
				restoreOrder = append(restoreOrder, name)
			}
			snapshots[name].xmlBytes = body
		} else if len(parts) == 2 {
			// Snapshot overlay volume. Stream it straight to the
			// pool — io.ReadAll here OOMs the backend on VMs with
			// multi-GB snapshots (the overlay bodies are GBs).
			if _, ok := snapshots[snapName]; !ok {
				snapshots[snapName] = &snapRestore{snapName: snapName}
				restoreOrder = append(restoreOrder, snapName)
			}
			snapshots[snapName].volumeArcName = hdr.Name
			// Write the overlay to the pool.
			volPath := filepath.Join(poolPath, resolvedName+"."+snapName)
			if werr := snapBudget.take(hdr.Size); werr != nil {
				return fmt.Sprintf("restore snapshots: %v", werr)
			}
			if werr := writeArchiveBody(tr3, volPath, hdr.Size); werr != nil {
				return fmt.Sprintf("restore snapshots: write %s: %v", snapName, werr)
			}
		}
	}

	if len(snapshots) == 0 && baseVolumeArc == "" {
		return "" // no snapshot data in archive
	}

	// Fix the qcow2 backing chain. The archive chain order is:
	//   _base/<root>  ←  snap1/<overlay1>  ←  ...  ←  disks/<active>
	//
	// Each overlay's internal backing-file path points to the
	// original source, which doesn't exist on this machine. We
	// rebase each overlay to point to the correct file in the pool.
	//
	// The active disk (from disks/) is already at
	// pool/<resolvedName>.qcow2 — its backing file needs to point
	// to the last snapshot overlay (or the base if no snapshots).
	qemuImg, qerr := exec.LookPath("qemu-img")
	if qerr != nil {
		return "restore snapshots: qemu-img not found, snapshots not restored"
	}

	// Walk in archive order (oldest → newest). Each overlay's
	// backing file should be the previous file in the chain.
	var prevPath string
	if baseVolumeArc != "" {
		prevPath = baseVolumeArc // pool/<resolvedName>.base
	}
	for _, name := range restoreOrder {
		sr := snapshots[name]
		if sr.volumeArcName == "" {
			continue // no overlay volume for this snapshot
		}
		volPath := filepath.Join(poolPath, resolvedName+"."+name)
		if prevPath != "" {
			// Fix the backing file reference.
			cmd := exec.Command(qemuImg, "rebase", "-u", "-b", prevPath, volPath)
			if output, rerr := cmd.CombinedOutput(); rerr != nil {
				return fmt.Sprintf("restore snapshots: rebase %s: %v: %s", name, rerr, string(output))
			}
		} else {
			// No backing file reference — make it standalone.
			cmd := exec.Command(qemuImg, "rebase", "-u", "-b", "", volPath)
			_ = cmd.Run()
		}
		prevPath = volPath
	}

	// Rebase the active disk (pool/<resolvedName>.qcow2) to point
	// to the last overlay in the chain (or the base if no overlays).
	activePath := filepath.Join(poolPath, resolvedName+".qcow2")
	if prevPath != "" {
		cmd := exec.Command(qemuImg, "rebase", "-u", "-b", prevPath, activePath)
		if output, rerr := cmd.CombinedOutput(); rerr != nil {
			return fmt.Sprintf("restore snapshots: rebase active disk: %v: %s", rerr, string(output))
		}
	} else if baseVolumeArc != "" {
		// Only the base exists — rebase active to base.
		cmd := exec.Command(qemuImg, "rebase", "-u", "-b", baseVolumeArc, activePath)
		if output, rerr := cmd.CombinedOutput(); rerr != nil {
			return fmt.Sprintf("restore snapshots: rebase active to base: %v: %s", rerr, string(output))
		}
	}

	// Recreate each snapshot via VIR_DOMAIN_SNAPSHOT_CREATE_REDEFINE.
	// The overlay volumes are already in place with correct backing
	// chain; REDEFINE just registers the metadata with libvirt.
	for _, name := range restoreOrder {
		sr := snapshots[name]
		if len(sr.xmlBytes) == 0 {
			continue
		}
		overlay := ""
		if sr.volumeArcName != "" {
			overlay = filepath.Join(poolPath, resolvedName+"."+name)
		}
		// Untrusted metadata: validated and re-serialised; the embedded
		// <domain> is dropped so a later revert cannot install a
		// definition chosen by the archive (audit #2).
		cleanXML, verr := SanitizeSnapshotXML(string(sr.xmlBytes), overlay)
		if verr != nil {
			return fmt.Sprintf("restore snapshots: snapshot %s rejected: %v", name, verr)
		}
		_, serr := dom.CreateSnapshotXML(cleanXML, 1 /* REDEFINE */)
		if serr != nil {
			return fmt.Sprintf("restore snapshots: redefine %s: %v", name, serr)
		}
	}

	// Refresh the pool so libvirt discovers the new snapshot volumes.
	if pool, perr := c.conn.LookupStoragePoolByName(c.DiskPoolName()); perr == nil {
		_ = pool.Refresh(0)
		pool.Free()
	}

	return ""
}

func writeTarEntry(tw *tar.Writer, name string, data []byte, mode int64) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	return nil
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
