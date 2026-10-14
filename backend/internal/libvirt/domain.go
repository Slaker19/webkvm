package libvirt

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"webkvm/internal/models"

	"github.com/google/uuid"
	"libvirt.org/go/libvirt"
)

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
