package libvirt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	libvirt "libvirt.org/go/libvirt"
	"webkvm/internal/models"
)

var (
	wwnRE    = regexp.MustCompile(`^[0-9a-fA-F]{16}$`)
	serialRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,36}$`)
	aliasRE  = regexp.MustCompile(`^ua-[A-Za-z0-9_.-]{1,60}$`)
)

func validateDiskIdentifiers(wwn, serial, alias string) error {
	if wwn != "" && !wwnRE.MatchString(wwn) {
		return fmt.Errorf("invalid disk WWN %q: must be exactly 16 hexadecimal characters", wwn)
	}
	if serial != "" && !serialRE.MatchString(serial) {
		return fmt.Errorf("invalid disk serial %q: must be 1-36 characters (letters, digits, '_', '-', '.')", serial)
	}
	if alias != "" && !aliasRE.MatchString(alias) {
		return fmt.Errorf("invalid disk alias %q: user aliases must start with 'ua-' prefix and be 4-63 characters", alias)
	}
	return nil
}

func (c *Connector) AttachDisk(id string, req models.AttachDiskRequest) error {
	if err := validateDiskIdentifiers(req.WWN, req.Serial, req.Alias); err != nil {
		return err
	}
	if req.ZVol != "" && req.Source != "" {
		return errors.New("cannot specify both 'source' and 'zvol'")
	}
	if req.ZVol != "" && req.Device == "cdrom" {
		return errors.New("cannot attach zvol as cdrom device")
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	format := req.Format
	if format == "" {
		format = "qcow2"
	}
	volExt := ".qcow2"
	if format == "raw" {
		volExt = ".img"
	}

	device := req.Device
	if device == "cdrom" && req.Source != "" {
		resolvedSource, err := c.resolveMediaPath(req.Source, req.Scope)
		if err != nil {
			return fmt.Errorf("resolve cdrom media: %w", err)
		}
		req.Source = resolvedSource
	}
	forceSATA := false
	if device == "cdrom" && req.Source != "" && strings.HasSuffix(strings.ToLower(req.Source), ".img") {
		// A .img is a raw disk image (Raspberry Pi/ZimaOS/cloud-image
		// style appliances), not optical media — it has no El Torito
		// boot catalog for the firmware to find, so attaching it as a
		// cdrom silently fails to boot. Attach as a real disk instead.
		//
		// Force SATA regardless of whatever bus the cdrom request asked
		// for (the UI's cdrom bus picker defaults to "scsi", i.e. the
		// lsilogic controller, which OVMF has no UEFI boot ROM for — a
		// disk attached there is invisible to the firmware's boot
		// manager even with an explicit <boot order>, confirmed live:
		// it silently fell through to PXE instead of the disk). SATA
		// (AHCI) has a native OVMF UEFI driver and needs no guest
		// drivers, unlike virtio.
		device = "disk"
		forceSATA = true
	}

	// Naming/bus scheme follows the ORIGINAL requested device, not the
	// possibly-overridden one above: a .img reattached as a disk still
	// keeps SCSI/SATA naming (sd*) rather than switching to virtio (vd*),
	// since only the device= attribute changes, not the bus.
	var devLetter string
	if req.Device == "cdrom" {
		devLetter = nextSCSIDev(dom, "sd")
	} else {
		devLetter = nextVirtioDev(dom, "vd")
	}

	var sourceXML string
	if req.Source != "" {
		sourceXML = fmt.Sprintf("<source file='%s'/>", xmlEscape(req.Source))
	}

	var driverXML string
	var diskType string
	// createdVol names a volume this call created, so a failed attach
	// below does not leave it orphaned (a retry would then fail with
	// "volume exists already").
	var createdPool, createdVol string
	if req.Device == "cdrom" {
		driverXML = "<driver name='qemu' type='raw'/>"
		diskType = "file"
	} else if req.ZVol != "" {
		cleanZVol := strings.TrimPrefix(strings.TrimSpace(req.ZVol), "/dev/zvol/")
		devPath := "/dev/zvol/" + cleanZVol
		diskType = "block"
		sourceXML = fmt.Sprintf("<source dev='%s'/>", xmlEscape(devPath))
		cacheIO := req.DiskCacheIO
		if cacheIO == nil {
			t := true
			cacheIO = &t
		}
		driverXML = fmt.Sprintf("<driver %s/>", diskDriverXMLAttrs("raw", cacheIO, req.DiskDiscard))
	} else if req.Source != "" {
		driverXML = fmt.Sprintf("<driver %s/>", diskDriverXMLAttrs(format, req.DiskCacheIO, req.DiskDiscard))
		diskType = "file"
	} else {
		// Create new volume
		poolName := req.Pool
		if poolName == "" {
			poolName = c.DiskPoolName()
		}
		poolPath, err := c.GetPoolPath(poolName)
		if err != nil {
			return fmt.Errorf("get pool path: %w", err)
		}
		domName, _ := dom.GetName()
		volName := domName + "-" + devLetter + volExt
		diskPath := poolPath + "/" + volName
		diskType = "file"
		sizeMB := req.SizeGB
		if sizeMB <= 0 {
			sizeMB = 10
		}
		if err := c.createDiskInPool(poolName, volName, sizeMB*1024, format); err != nil {
			// Do NOT dom.Undefine() here: this AttachDisk runs against an
			// already-defined, possibly in-use VM. Undefining on a mere
			// volume-creation failure would delete the whole VM definition
			// (the deferred dom.Free() is all the cleanup needed). This
			// mirrors CreateDomain, where Undefine is correct precisely
			// because the domain was just defined in this call.
			return fmt.Errorf("create disk: %w", err)
		}
		createdPool, createdVol = poolName, volName
		sourceXML = fmt.Sprintf("<source file='%s'/>", xmlEscape(diskPath))
		driverXML = fmt.Sprintf("<driver %s/>", diskDriverXMLAttrs(format, req.DiskCacheIO, req.DiskDiscard))
	}

	busType := req.Bus
	if busType == "" {
		if req.Device == "cdrom" {
			busType = "sata"
		} else {
			busType = "virtio"
		}
	}
	if forceSATA {
		busType = "sata"
	}

	// sata/scsi disks are addressed by controller/bus/target/unit
	// rather than PCI slot. Leaving this to libvirt's own
	// auto-assignment during an incremental AttachDeviceFlags call is
	// unreliable once another disk already sits on the same bus —
	// confirmed live: attaching a second sata disk failed with "Found
	// duplicate drive address ... unit='0'" because it picked the
	// same slot the first one already uses. Assign the next free unit
	// ourselves instead of leaving it to chance.
	var addressXML string
	if busType == "sata" || busType == "scsi" {
		addressXML = fmt.Sprintf("<address type='drive' controller='0' bus='0' target='0' unit='%d'/>", nextDriveUnit(dom, busType))
	}

	var identifiersXML string
	if req.WWN != "" {
		identifiersXML += fmt.Sprintf("\n  <wwn>%s</wwn>", xmlEscape(req.WWN))
	}
	if req.Serial != "" {
		identifiersXML += fmt.Sprintf("\n  <serial>%s</serial>", xmlEscape(req.Serial))
	}
	if req.Alias != "" {
		identifiersXML += fmt.Sprintf("\n  <alias name='%s'/>", xmlEscape(req.Alias))
	}

	devXML := fmt.Sprintf(`<disk type='%s' device='%s'>
  %s
  <target dev='%s' bus='%s'/>
  %s
  %s%s
</disk>`, xmlEscape(diskType), xmlEscape(device), driverXML, xmlEscape(devLetter), xmlEscape(busType), sourceXML, addressXML, identifiersXML)

	if device == "cdrom" {
		devXML = fmt.Sprintf(`<disk type='%s' device='cdrom'>
  %s
  %s
  <target dev='%s' bus='%s'/>
  %s
  <readonly/>
</disk>`, xmlEscape(diskType), driverXML, sourceXML, xmlEscape(devLetter), xmlEscape(busType), addressXML)
	}

	flags := libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	domState, _, err := dom.GetState()
	if err == nil && (domState == libvirt.DOMAIN_RUNNING || domState == libvirt.DOMAIN_PAUSED) {
		flags |= libvirt.DOMAIN_DEVICE_MODIFY_LIVE
	}
	if err := dom.AttachDeviceFlags(devXML, flags); err != nil {
		if createdVol != "" {
			if derr := c.DeleteStorageVolume(createdPool, createdVol); derr != nil {
				slog.Warn("attach_disk_cleanup_failed", "pool", createdPool, "vol", createdVol, "err", derr)
			}
		}
		return err
	}
	return nil
}

func (c *Connector) DetachDisk(id, target string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	domState, _, err := dom.GetState()
	isRunning := err == nil && (domState == libvirt.DOMAIN_RUNNING || domState == libvirt.DOMAIN_PAUSED)

	flags := libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	if isRunning {
		flags |= libvirt.DOMAIN_DEVICE_MODIFY_LIVE
	}

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}

	diskStartRe := regexp.MustCompile(`<disk\b`)
	targetRe := regexp.MustCompile(`<target\b[^>]*dev=['"]` + regexp.QuoteMeta(target) + `['"][^>]*/>`)

	loc := targetRe.FindStringIndex(xmlDesc)
	if loc == nil && isRunning {
		// Fallback to inactive XML if domain is running but disk was only defined in persistent config
		if inactiveXML, ierr := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE); ierr == nil {
			xmlDesc = inactiveXML
			loc = targetRe.FindStringIndex(xmlDesc)
			flags = libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
		}
	}
	if loc == nil {
		return fmt.Errorf("disk with target '%s' not found", target)
	}

	allStarts := diskStartRe.FindAllStringIndex(xmlDesc[:loc[0]], -1)
	if len(allStarts) == 0 {
		return fmt.Errorf("could not find disk start for target '%s'", target)
	}
	start := allStarts[len(allStarts)-1][0]

	end := strings.Index(xmlDesc[loc[1]:], "</disk>")
	if end == -1 {
		return fmt.Errorf("could not find disk end for target '%s'", target)
	}
	end = loc[1] + end + 7

	diskXML := xmlDesc[start:end]
	if err := dom.DetachDeviceFlags(diskXML, flags); err != nil {
		// If detaching with LIVE|CONFIG failed because device wasn't live, retry CONFIG only
		if (flags & libvirt.DOMAIN_DEVICE_MODIFY_LIVE) != 0 {
			if cerr := dom.DetachDeviceFlags(diskXML, libvirt.DOMAIN_DEVICE_MODIFY_CONFIG); cerr == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

// ChangeDiskBus switches an existing disk or cdrom to a different bus
// (virtio/sata/scsi/ide). Bus is baked into libvirt's device topology
// (target dev naming, PCI/drive address) — there's no in-place "update"
// the way UpdateDiskSource swaps just the <source> file, so this reads
// the current device/source/format, detaches it, and reattaches it
// fresh on the requested bus.
func (c *Connector) ChangeDiskBus(id, target, newBus string) error {
	if err := validateDiskBus(newBus); err != nil {
		return err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}

	xmlDesc, err := dom.GetXMLDesc(0)
	dom.Free()
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}

	diskStartRe := regexp.MustCompile(`<disk\b`)
	targetRe := regexp.MustCompile(`<target\b[^>]*dev='` + regexp.QuoteMeta(target) + `'[^>]*/>`)
	loc := targetRe.FindStringIndex(xmlDesc)
	if loc == nil {
		return fmt.Errorf("disk with target '%s' not found", target)
	}
	allStarts := diskStartRe.FindAllStringIndex(xmlDesc[:loc[0]], -1)
	if len(allStarts) == 0 {
		return fmt.Errorf("could not find disk start for target '%s'", target)
	}
	start := allStarts[len(allStarts)-1][0]
	end := strings.Index(xmlDesc[loc[1]:], "</disk>")
	if end == -1 {
		return fmt.Errorf("could not find disk end for target '%s'", target)
	}
	end = loc[1] + end + 7
	diskXML := xmlDesc[start:end]

	deviceRe := regexp.MustCompile(`<disk\b[^>]*\bdevice='([^']*)'`)
	dm := deviceRe.FindStringSubmatch(diskXML)
	if len(dm) < 2 {
		return fmt.Errorf("could not determine device type for target %q", target)
	}
	device := dm[1]

	sourceFileRe := regexp.MustCompile(`<source\b[^>]*\bfile='([^']*)'`)
	sm := sourceFileRe.FindStringSubmatch(diskXML)
	if len(sm) < 2 || sm[1] == "" {
		return fmt.Errorf("disk %q has no source file (an empty drive can't change bus)", target)
	}
	source := sm[1]

	format := "qcow2"
	if fm := regexp.MustCompile(`<driver\b[^>]*\btype='([^']*)'`).FindStringSubmatch(diskXML); len(fm) > 1 && fm[1] != "" {
		format = fm[1]
	}

	oldBus := ""
	if bm := regexp.MustCompile(`<target\b[^>]*\bbus='([^']*)'`).FindStringSubmatch(diskXML); len(bm) > 1 {
		oldBus = bm[1]
	}

	if err := c.DetachDisk(id, target); err != nil {
		return fmt.Errorf("detach disk %q: %w", target, err)
	}

	attachErr := c.AttachDisk(id, models.AttachDiskRequest{
		Device: device,
		Bus:    newBus,
		Source: source,
		Format: format,
		Scope:  models.MediaScope{Admin: true}, // re-attaching what the domain already referenced
	})
	if attachErr == nil {
		return nil
	}

	// The new bus didn't work (e.g. "ide" on a Q35 machine, which has
	// no IDE controller at all) — restore the disk on its previous bus
	// rather than leaving it detached with no easy way back.
	if oldBus == "" {
		return fmt.Errorf("bus %q failed (%v) and the disk is now detached (no original bus recorded to restore it) — reattach %q manually via Add Disk", newBus, attachErr, source)
	}
	if rollbackErr := c.AttachDisk(id, models.AttachDiskRequest{
		Device: device,
		Bus:    oldBus,
		Source: source,
		Format: format,
		Scope:  models.MediaScope{Admin: true}, // re-attaching what the domain already referenced
	}); rollbackErr != nil {
		return fmt.Errorf("bus %q failed (%v), and restoring the original bus %q also failed (%v) — the disk is detached; reattach %q manually via Add Disk", newBus, attachErr, oldBus, rollbackErr, source)
	}
	return fmt.Errorf("bus %q is not supported on this VM (%v) — the disk has been restored to its original bus (%s)", newBus, attachErr, oldBus)
}

// USB host device passthrough implementation is located in domain_usb.go.
// PCI host device & VFIO passthrough implementation is located in domain_pci.go.
// 9p shared folders implementation is located in domain_shares.go.


func (c *Connector) UpdateDiskSource(id, target, source string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	flags := libvirt.DOMAIN_DEVICE_MODIFY_LIVE | libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	domState, _, err := dom.GetState()
	if err == nil && domState != libvirt.DOMAIN_RUNNING {
		flags = libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	}

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}

	diskStartRe := regexp.MustCompile(`<disk\b`)
	targetRe := regexp.MustCompile(`<target\b[^>]*dev='` + regexp.QuoteMeta(target) + `'[^>]*/>`)

	loc := targetRe.FindStringIndex(xmlDesc)
	if loc == nil {
		return fmt.Errorf("disk with target '%s' not found", target)
	}

	allStarts := diskStartRe.FindAllStringIndex(xmlDesc[:loc[0]], -1)
	if len(allStarts) == 0 {
		return fmt.Errorf("could not find disk start for target '%s'", target)
	}
	start := allStarts[len(allStarts)-1][0]

	end := strings.Index(xmlDesc[loc[1]:], "</disk>")
	if end == -1 {
		return fmt.Errorf("could not find disk end for target '%s'", target)
	}
	end = loc[1] + end + 7

	diskXML := xmlDesc[start:end]

	// A .img is a raw disk image, not optical media — swapping it into
	// an existing cdrom slot keeps that slot's device='cdrom'/readonly
	// semantics (this only replaces <source>, not the device type), so
	// it would look "attached" but never boot. There's no in-place way
	// to turn a cdrom slot into a real disk; the caller must attach it
	// fresh as a disk instead (see AttachDisk).
	if source != "" && strings.HasSuffix(strings.ToLower(source), ".img") && strings.Contains(diskXML, "device='cdrom'") {
		return fmt.Errorf("%q is a raw disk image, not optical media — it can't boot from a CD-ROM slot; detach this drive and use \"Add Disk\" to attach it as a real disk instead", filepath.Base(source))
	}

	sourceRe := regexp.MustCompile(`<source\b[^>]*/>`)
	if source == "" {
		diskXML = regexp.MustCompile(`\s*<source\b[^>]*/>`).ReplaceAllString(diskXML, "")
	} else {
		newSource := fmt.Sprintf("<source file='%s'/>", xmlEscape(source))
		if sourceRe.MatchString(diskXML) {
			diskXML = sourceRe.ReplaceAllString(diskXML, newSource)
		} else {
			diskXML = regexp.MustCompile(`(<target\b)`).ReplaceAllString(diskXML, "  "+newSource+"\n$1")
		}
	}

	return dom.UpdateDeviceFlags(diskXML, flags)
}

func (c *Connector) AttachNetworkIface(id string, req models.AttachNetRequest) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	model := req.Model
	if model == "" {
		model = "virtio"
	}
	if req.Network == "" || !isLinuxBridge(req.Network) {
		return fmt.Errorf("network %q is not a host Linux bridge; WebKVM only attaches to real bridges (vmbr0, vmbr1, …)", req.Network)
	}

	ifaceXML := interfaceXMLWithVLAN(req.Network, model, req.VLANTag)

	flags := libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	domState, _, err := dom.GetState()
	if err == nil && (domState == libvirt.DOMAIN_RUNNING || domState == libvirt.DOMAIN_PAUSED) {
		flags |= libvirt.DOMAIN_DEVICE_MODIFY_LIVE
	}
	return dom.AttachDeviceFlags(ifaceXML, flags)
}

// findInterfaceBlockByMAC returns the single <interface>...</interface>
// block whose <mac address='...'/> matches mac, or "" if none does.
//
// A domain can have several <interface> elements. A naive regex of the
// shape `<interface\b[^>]*>[\s\S]*?<mac\b[^>]*address='MAC'[^>]*/>[\s\S]*?</interface>`
// is unsafe here: when the target MAC belongs to the 2nd (or later)
// interface, the lazy [\s\S]*? before the <mac> happily spans across the
// FIRST interface's own opening AND closing tags (regexes don't track XML
// nesting), so the match ends up starting at the first interface's '<interface'
// and ending at the SECOND interface's '</interface>' — a string containing
// two top-level <interface> elements concatenated, which
// AttachDeviceFlags/DetachDeviceFlags/UpdateDeviceFlags then reject with a
// real libvirt parse error ("Extra content at the end of the document").
// Splitting into non-overlapping single-interface blocks first (the same
// safe technique parseNetworks already uses) and then filtering by MAC
// avoids this entirely.
func findInterfaceBlockByMAC(xmlDesc, mac string) string {
	blockRe := regexp.MustCompile(`<interface\b[^>]*>[\s\S]*?</interface>`)
	macRe := regexp.MustCompile(`<mac\b[^>]*address='` + regexp.QuoteMeta(mac) + `'[^>]*/>`)
	for _, block := range blockRe.FindAllString(xmlDesc, -1) {
		if macRe.MatchString(block) {
			return block
		}
	}
	return ""
}

func (c *Connector) DetachNetworkIface(id, mac string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	domState, _, err := dom.GetState()
	isRunning := err == nil && (domState == libvirt.DOMAIN_RUNNING || domState == libvirt.DOMAIN_PAUSED)
	flags := libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	if isRunning {
		flags |= libvirt.DOMAIN_DEVICE_MODIFY_LIVE
	}

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}

	ifaceXML := findInterfaceBlockByMAC(xmlDesc, mac)
	if ifaceXML == "" && isRunning {
		// Fallback to inactive XML
		if inactiveXML, ierr := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE); ierr == nil {
			ifaceXML = findInterfaceBlockByMAC(inactiveXML, mac)
			flags = libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
		}
	}
	if ifaceXML == "" {
		return fmt.Errorf("network interface with mac '%s' not found", mac)
	}

	if err := dom.DetachDeviceFlags(ifaceXML, flags); err != nil {
		if (flags & libvirt.DOMAIN_DEVICE_MODIFY_LIVE) != 0 {
			if cerr := dom.DetachDeviceFlags(ifaceXML, libvirt.DOMAIN_DEVICE_MODIFY_CONFIG); cerr == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

// UpdateNetworkIface updates an existing network interface on a domain.
// When running, link state changes and network/bridge updates are applied in live.
// MAC address modification requires VM shutoff.
func (c *Connector) UpdateNetworkIface(id, oldMAC string, req models.UpdateNetIfaceRequest) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	domState, _, _ := dom.GetState()
	isRunning := domState == libvirt.DOMAIN_RUNNING || domState == libvirt.DOMAIN_PAUSED

	// Enforce shutoff only if changing MAC address.
	if isRunning && req.MAC != nil && *req.MAC != "" && *req.MAC != oldMAC {
		return fmt.Errorf("VM must be shut off to change MAC address")
	}

	// Fleet-wide MAC collision check (skipped when not changing MAC).
	if req.MAC != nil && *req.MAC != "" && *req.MAC != oldMAC {
		if err := c.assertMACUnique(*req.MAC, oldMAC); err != nil {
			return err
		}
	}

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}
	ifaceXML := findInterfaceBlockByMAC(xmlDesc, oldMAC)
	if ifaceXML == "" {
		return fmt.Errorf("network interface with mac '%s' not found", oldMAC)
	}

	updated := ifaceXML

	// Patch <mac> address if requested.
	if req.MAC != nil && *req.MAC != "" && *req.MAC != oldMAC {
		updated = regexp.MustCompile(`<mac\b[^>]*address='[^']*'[^>]*/>`).
			ReplaceAllString(updated, "<mac address='"+xmlEscape(*req.MAC)+"'/>")
	}

	// Patch the <source> element (libvirt network or host Linux bridge).
	if req.Network != nil && *req.Network != "" {
		newSrc := fmt.Sprintf("<source network='%s'/>", xmlEscape(*req.Network))
		if isLinuxBridge(*req.Network) {
			newSrc = fmt.Sprintf("<source bridge='%s'/>", xmlEscape(*req.Network))
		}
		srcRe := regexp.MustCompile(`<source\s+(network|bridge)='[^']*'\s*/>`)
		if srcRe.MatchString(updated) {
			updated = srcRe.ReplaceAllString(updated, newSrc)
		} else {
			return fmt.Errorf("no source element found on the interface")
		}
	}

	// Patch VLAN: 0 means remove; positive means add/replace; nil means leave.
	if req.VLANTag != nil {
		// Strip any existing <vlan>...</vlan> block.
		updated = regexp.MustCompile(`<vlan>[\s\S]*?</vlan>\s*`).ReplaceAllString(updated, "")
		if *req.VLANTag > 0 {
			vlanBlock := fmt.Sprintf("<vlan><tag id='%d'/></vlan>\n  ", *req.VLANTag)
			// Insert before closing </interface>.
			updated = strings.Replace(updated, "</interface>", vlanBlock+"</interface>", 1)
		}
	}

	// Patch Link state: "up" or "down"
	if req.LinkState != nil && *req.LinkState != "" {
		ls := strings.ToLower(strings.TrimSpace(*req.LinkState))
		if ls != "up" && ls != "down" {
			return fmt.Errorf("invalid link state %q: must be 'up' or 'down'", *req.LinkState)
		}
		linkRe := regexp.MustCompile(`<link\b[^>]*state='[^']*'[^>]*/>`)
		linkXML := fmt.Sprintf("<link state='%s'/>", ls)
		if linkRe.MatchString(updated) {
			updated = linkRe.ReplaceAllString(updated, linkXML)
		} else {
			updated = strings.Replace(updated, "</interface>", linkXML+"\n  </interface>", 1)
		}
	}

	if isRunning {
		flags := libvirt.DOMAIN_DEVICE_MODIFY_LIVE | libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
		if err := dom.UpdateDeviceFlags(updated, flags); err != nil {
			// If live update failed in-place, hot-swap transparently (detach then re-attach)
			_ = dom.DetachDeviceFlags(ifaceXML, flags)
			if aerr := dom.AttachDeviceFlags(updated, flags); aerr != nil {
				_ = dom.AttachDeviceFlags(ifaceXML, flags)
				return fmt.Errorf("live interface update failed: %w", aerr)
			}
		}
		return nil
	}

	return dom.UpdateDeviceFlags(updated, libvirt.DOMAIN_DEVICE_MODIFY_CONFIG)
}

// assertMACUnique returns nil if `mac` is not used by any interface of any
// other domain. The `selfMAC` is the current MAC of the iface being edited
// (so it doesn't conflict with itself).
func (c *Connector) assertMACUnique(mac, selfMAC string) error {
	if err := c.ensureConnected(); err != nil {
		return err
	}
	doms, err := c.conn.ListAllDomains(0)
	if err != nil {
		return err
	}
	defer func() {
		for i := range doms {
			doms[i].Free()
		}
	}()
	macLower := strings.ToLower(mac)
	for i := range doms {
		xmlDesc, err := doms[i].GetXMLDesc(0)
		if err != nil {
			continue
		}
		for _, m := range extractAllMACs(xmlDesc) {
			if strings.ToLower(m) == macLower && !strings.EqualFold(m, selfMAC) {
				// Find the other domain's name for the error.
				otherName, _ := doms[i].GetName()
				return fmt.Errorf("MAC %s already in use by VM '%s'", mac, otherName)
			}
		}
	}
	return nil
}

// extractAllMACs returns every <mac address='...'/> value in a domain XML.
func extractAllMACs(xmlDesc string) []string {
	re := regexp.MustCompile(`<mac\s+address='([^']*)'`)
	matches := re.FindAllStringSubmatch(xmlDesc, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// CheckVLANSupport reports whether VLAN tagging is supported for the
// given libvirt network on this host. VLAN on a plain bridge requires
// libvirt ≥ 11.0.0 with macTableManager='libvirt' (or an OVS bridge).
func (c *Connector) CheckVLANSupport(networkName string) (models.VlanSupport, error) {
	if err := c.ensureConnected(); err != nil {
		return models.VlanSupport{}, err
	}

	// Libvirt version check.
	libVer, err := c.conn.GetLibVersion()
	if err != nil {
		return models.VlanSupport{}, fmt.Errorf("get libvirt version: %w", err)
	}
	major := int(libVer / 1_000_000)
	if major < 11 {
		return models.VlanSupport{
			Supported: false,
			Reason:    fmt.Sprintf("libvirt %d.%d.%d detected; VLAN tagging on plain bridges requires libvirt ≥ 11.0.0", major, (libVer/1000)%1000, libVer%1000),
		}, nil
	}

	// Network must exist.
	net, err := c.conn.LookupNetworkByName(networkName)
	if err != nil {
		return models.VlanSupport{Supported: false, Reason: "network not found"}, nil
	}
	defer net.Free()
	xmlDesc, err := net.GetXMLDesc(0)
	if err != nil {
		return models.VlanSupport{Supported: false, Reason: "cannot read network XML"}, nil
	}

	// Check for macTableManager='libvirt' or OVS.
	hasMacTableLibvirt := strings.Contains(xmlDesc, `macTableManager='libvirt'`)
	hasOVS := strings.Contains(xmlDesc, `type='openvswitch'`) || strings.Contains(xmlDesc, `type="openvswitch"`)
	if hasMacTableLibvirt || hasOVS {
		return models.VlanSupport{Supported: true}, nil
	}
	return models.VlanSupport{
		Supported: false,
		Reason:    "this bridge does not have macTableManager='libvirt' (or an Open vSwitch); VLAN tagging would be silently dropped by the kernel bridge",
	}, nil
}


// ResizeDomainDisk grows (or shrinks) the backing file of a VM disk
// identified by its target device (e.g. "vda", "sdb").
//
// The VM must be shut off — qemu-img can't take a write lock on a
// running VM's qcow2 file. After the resize, the domain XML's
// <capacity> is updated on next start (libvirt re-derives it from
// the file size).
//
// Returns the new size in bytes as reported by `qemu-img info`.
func (c *Connector) ResizeDomainDisk(ctx context.Context, id, target string, newSizeGB int64) (int64, error) {
	if newSizeGB <= 0 {
		return 0, fmt.Errorf("size must be positive")
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return 0, err
	}
	defer dom.Free()

	disks := c.parseDisksFiltered(domGetXMLDescOrEmpty(dom), false)
	var disk *models.DiskInfo
	for i := range disks {
		if disks[i].Target == target {
			disk = &disks[i]
			break
		}
	}
	if disk == nil {
		return 0, fmt.Errorf("disk %q not found on VM %q", target, id)
	}
	if disk.Source == "" {
		return 0, fmt.Errorf("disk %q has no source file (probably a block device or empty cdrom)", target)
	}

	newBytes := uint64(newSizeGB) * 1024 * 1024 * 1024

	state, _, _ := dom.GetState()
	if state == libvirt.DOMAIN_RUNNING || state == libvirt.DOMAIN_PAUSED {
		// Online resize only supports growing, and relies on QEMU's
		// own block_resize (libvirt's BlockResize) to grow the
		// qcow2 header/metadata in place — no host file truncation
		// needed for qcow2. Raw disks have no such header and are
		// not safe to grow this way while running.
		if newSizeGB < disk.SizeGB {
			return 0, fmt.Errorf("cannot shrink a disk while the VM is running; shut it down first")
		}
		if err := dom.BlockResize(target, newBytes, libvirt.DOMAIN_BLOCK_RESIZE_BYTES); err != nil {
			return 0, fmt.Errorf("live resize failed (only qcow2 disks can grow while running): %w", err)
		}
		return int64(newBytes), nil
	}

	cmd := exec.CommandContext(ctx, "qemu-img", "resize", disk.Source, fmt.Sprintf("%d", newBytes))
	if out, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("qemu-img resize failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return int64(newBytes), nil
}

// domGetXMLDescOrEmpty returns the inactive XML or "" on error.
func domGetXMLDescOrEmpty(dom *libvirt.Domain) string {
	out, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		out, _ = dom.GetXMLDesc(0)
	}
	return out
}

func nextVirtioDev(dom *libvirt.Domain, prefix string) string {
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return prefix + "b"
	}
	re := regexp.MustCompile(`<target\b[^>]*dev='(` + prefix + `[a-z]+)'`)
	matches := re.FindAllStringSubmatch(xmlDesc, -1)
	used := make(map[byte]bool)
	for _, m := range matches {
		if len(m[1]) > 2 {
			used[m[1][len(prefix)]] = true
		}
	}
	for c := byte('a'); c <= 'z'; c++ {
		if !used[c] {
			return prefix + string(c)
		}
	}
	return prefix + "z"
}

func nextSCSIDev(dom *libvirt.Domain, prefix string) string {
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return prefix + "a"
	}
	re := regexp.MustCompile(`<target\b[^>]*dev='(` + prefix + `[a-z]+)'`)
	matches := re.FindAllStringSubmatch(xmlDesc, -1)
	used := make(map[byte]bool)
	for _, m := range matches {
		if len(m[1]) > 2 {
			used[m[1][len(prefix)]] = true
		}
	}
	for c := byte('a'); c <= 'z'; c++ {
		if !used[c] {
			return prefix + string(c)
		}
	}
	return prefix + "z"
}

// nextDriveUnit returns the next free 'unit' for a controller/bus/
// target/unit-addressed disk bus (sata, scsi) — the highest unit
// already used by an existing disk on that same bus, plus one. See
// the call site in AttachDisk for why this can't be left to
// libvirt's own auto-assignment during an incremental attach.
func nextDriveUnit(dom *libvirt.Domain, bus string) int {
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return 0
	}
	diskRe := regexp.MustCompile(`(?s)<disk\b.*?</disk>`)
	busRe := regexp.MustCompile(`<target\b[^>]*\bbus='` + regexp.QuoteMeta(bus) + `'`)
	unitRe := regexp.MustCompile(`<address\s+type='drive'[^>]*\bunit='(\d+)'`)
	maxUnit := -1
	for _, d := range diskRe.FindAllString(xmlDesc, -1) {
		if !busRe.MatchString(d) {
			continue
		}
		if um := unitRe.FindStringSubmatch(d); len(um) > 1 {
			if u, err := strconv.Atoi(um[1]); err == nil && u > maxUnit {
				maxUnit = u
			}
		}
	}
	return maxUnit + 1
}

func extractTagAttr(xml, attr string) string {
	search := attr + "='"
	s := strings.Index(xml, search)
	if s < 0 {
		search = attr + "=\""
		s = strings.Index(xml, search)
		if s < 0 {
			return ""
		}
	}
	s += len(search)
	e := strings.Index(xml[s:], "'")
	if e < 0 {
		e = strings.Index(xml[s:], "\"")
		if e < 0 {
			return ""
		}
	}
	return xml[s : s+e]
}

