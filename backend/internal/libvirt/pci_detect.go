package libvirt

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"webkvm/internal/diskstate"
	"webkvm/internal/models"
)

// pciRestriction describes why a PCI address cannot be safely passed through.
type pciRestriction struct {
	reason       string
	hostCritical bool
}

// pciIsBridge reports whether addr points to a PCI/PCIe/Host bridge or system
// infrastructure device (class 0x06xxxx or 0x0806xx IOMMU). These devices cannot
// be passed through to guest virtual machines.
func pciIsBridge(addr string) bool {
	if !validPCIAddress(addr) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(pciSysfsDir(addr), "class"))
	if err != nil {
		return false
	}
	cls := strings.ToLower(strings.TrimSpace(string(data)))
	// 0x06xxxx: Bridge device (Host bridge, PCI-to-PCI bridge, ISA bridge, etc.)
	// 0x0806xx: IOMMU / System peripheral
	return strings.HasPrefix(cls, "0x06") || strings.HasPrefix(cls, "0x0806")
}

// pciExtractAddresses returns all valid PCI addresses found in a path or string.
func pciExtractAddresses(path string) []string {
	parts := strings.Split(path, "/")
	var res []string
	seen := make(map[string]bool)
	for _, part := range parts {
		if validPCIAddress(part) && !seen[part] {
			seen[part] = true
			res = append(res, part)
		}
	}
	return res
}

// pciHostCriticalStorage finds which PCI devices own storage holding
// critical host filesystems (root, boot, pools) or storage the host
// still claims.
//
// It resolves *declared* mount intent rather than only what the kernel
// has mounted at this instant. A storage controller whose filesystem is
// behind an idle systemd automount looks completely unmounted in
// /proc/mounts, so a check based on that alone would hand the whole
// NVMe to a guest via VFIO while the host is still configured to mount
// it — the host then remounts the device the moment anything touches
// the path, with the guest already writing to it.
func pciHostCriticalStorage() map[string]pciRestriction {
	res := make(map[string]pciRestriction)

	ctx, cancel := context.WithTimeout(context.Background(), diskstate.DefaultTimeout)
	defer cancel()

	for dev, state := range diskstate.New().Resolve(ctx) {
		devName := filepath.Base(dev)
		realPath, err := filepath.EvalSymlinks(filepath.Join("/sys/class/block", devName))
		if err != nil {
			continue
		}
		addrs := pciExtractAddresses(realPath)
		if len(addrs) == 0 {
			continue
		}

		isCritical := state.HasSystemMount()

		for _, addr := range addrs {
			if isCritical {
				res[addr] = pciRestriction{
					reason:       "host_root_disk",
					hostCritical: true,
				}
				continue
			}
			// Never downgrade an address already marked critical: one
			// controller can carry both the root disk and a data disk.
			if existing, exists := res[addr]; exists && existing.hostCritical {
				continue
			}
			if _, exists := res[addr]; !exists {
				res[addr] = pciRestriction{
					reason:       "mounted_storage",
					hostCritical: false,
				}
			}
		}
	}
	return res
}

// pciHostCriticalNetwork inspects the host's default route and bridges to find
// which PCI devices own the primary management network uplink.
func pciHostCriticalNetwork() map[string]pciRestriction {
	res := make(map[string]pciRestriction)
	uplink := defaultUplink()
	if uplink == "" {
		uplink = mainBridge()
	}
	if uplink == "" {
		return res
	}

	ifaces := []string{uplink}
	// If uplink is a Linux bridge, inspect its bridge slaves
	brifDir := filepath.Join("/sys/class/net", uplink, "brif")
	if entries, err := os.ReadDir(brifDir); err == nil {
		for _, e := range entries {
			ifaces = append(ifaces, e.Name())
		}
	}

	for _, iface := range ifaces {
		devLink := filepath.Join("/sys/class/net", iface, "device")
		realPath, err := filepath.EvalSymlinks(devLink)
		if err != nil {
			continue
		}
		for _, addr := range pciExtractAddresses(realPath) {
			res[addr] = pciRestriction{
				reason:       "host_uplink",
				hostCritical: true,
			}
		}
	}
	return res
}

// pciHostRestrictions aggregates host critical storage and network restrictions.
func pciHostRestrictions() map[string]pciRestriction {
	res := pciHostCriticalStorage()
	for addr, restr := range pciHostCriticalNetwork() {
		// host_root_disk takes priority over host_uplink if somehow both match
		if cur, ok := res[addr]; !ok || !cur.hostCritical {
			res[addr] = restr
		}
	}
	return res
}

// evaluatePCIDevice determines whether a PCI device is assignable and assigns reason.
func evaluatePCIDevice(dev *models.PCIDevice, restrictions map[string]pciRestriction) {
	// Rule 1: Already attached to a VM
	if dev.InUse {
		dev.Assignable = false
		dev.BlockReason = "in_use"
		return
	}

	// Rule 2: Host console / boot VGA
	if dev.BootVGA {
		dev.Assignable = false
		dev.BlockReason = "boot_vga"
		dev.HostCritical = true
		return
	}

	// Rule 3: Host root disk / management uplink / mounted storage
	if restr, found := restrictions[dev.Address]; found {
		dev.Assignable = false
		dev.BlockReason = restr.reason
		dev.HostCritical = restr.hostCritical
		return
	}

	// Rule 4: PCI/PCIe/Host bridge or system IOMMU
	if pciIsBridge(dev.Address) {
		dev.Assignable = false
		dev.BlockReason = "pci_bridge"
		return
	}

	dev.Assignable = true
	dev.BlockReason = ""
	dev.HostCritical = false
}

// evaluatePCIGroup determines whether an entire IOMMU group can be attached.
func evaluatePCIGroup(group *models.PCIIOMMUGroup) {
	hasCritical := false
	hasBridge := false
	hasInUse := false
	hasMounted := false
	var criticalReason string

	for _, dev := range group.Devices {
		if dev.HostCritical {
			hasCritical = true
			if criticalReason == "" {
				criticalReason = dev.BlockReason
			}
		}
		if dev.BlockReason == "pci_bridge" {
			hasBridge = true
		}
		if dev.InUse {
			hasInUse = true
		}
		if dev.BlockReason == "mounted_storage" {
			hasMounted = true
		}
	}

	group.HostCritical = hasCritical

	if hasCritical {
		group.Assignable = false
		group.BlockReason = criticalReason
		return
	}

	if hasBridge {
		group.Assignable = false
		group.BlockReason = "pci_bridge"
		return
	}

	if hasInUse {
		group.Assignable = false
		group.BlockReason = "in_use"
		return
	}

	if hasMounted {
		group.Assignable = false
		group.BlockReason = "mounted_storage"
		return
	}

	// Group contains multiple devices, some are bridges or mixed
	for _, dev := range group.Devices {
		if !dev.Assignable {
			group.Assignable = false
			group.BlockReason = dev.BlockReason
			return
		}
	}

	group.Assignable = true
	group.BlockReason = ""
}

// GetHostPCIPreflight inspects host IOMMU, VFIO and group capabilities.
func (c *Connector) GetHostPCIPreflight() (models.PCIPreflightInfo, error) {
	entries, err := os.ReadDir("/sys/kernel/iommu_groups")
	enabled := err == nil && len(entries) > 0

	vendor := "unknown"
	// Check /sys/class/iommu or CPU info
	if _, err := os.Stat("/sys/class/iommu"); err == nil {
		if iommuEntries, err := os.ReadDir("/sys/class/iommu"); err == nil {
			for _, e := range iommuEntries {
				name := strings.ToLower(e.Name())
				if strings.HasPrefix(name, "dmar") {
					vendor = "intel"
					break
				}
				if strings.HasPrefix(name, "ivhd") || strings.HasPrefix(name, "amd") {
					vendor = "amd"
					break
				}
			}
		}
	}
	if vendor == "unknown" {
		if cpuData, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			str := string(cpuData)
			if strings.Contains(str, "AuthenticAMD") {
				vendor = "amd"
			} else if strings.Contains(str, "GenuineIntel") {
				vendor = "intel"
			}
		}
	}

	mode := "disabled"
	if enabled {
		mode = "translated"
		// Check /sys/kernel/iommu_groups/0/type
		if typeData, err := os.ReadFile("/sys/kernel/iommu_groups/0/type"); err == nil {
			t := strings.TrimSpace(string(typeData))
			if strings.EqualFold(t, "identity") || strings.EqualFold(t, "passthrough") {
				mode = "passthrough"
			} else if t != "" {
				mode = strings.ToLower(t)
			}
		}
	}

	vfioLoaded := false
	if _, err := os.Stat("/sys/module/vfio"); err == nil {
		vfioLoaded = true
	}
	if !vfioLoaded {
		if _, err := os.Stat("/sys/module/vfio_pci"); err == nil {
			vfioLoaded = true
		}
	}

	vfioAvail := false
	if _, err := os.Stat("/dev/vfio"); err == nil {
		vfioAvail = true
	} else if vfioLoaded {
		vfioAvail = true
	}

	info := models.PCIPreflightInfo{
		IOMMUEnabled:     enabled,
		IOMMUVendor:      vendor,
		IOMMUMode:        mode,
		VFIOModuleLoaded: vfioLoaded,
		VFIOAvailable:    vfioAvail,
		GroupsTotal:      len(entries),
	}

	if enabled {
		groups, err := c.ListHostPCIDevices()
		if err == nil {
			for _, g := range groups {
				if g.HostCritical {
					info.GroupsHostCritical++
				} else if g.BlockReason == "pci_bridge" {
					info.GroupsBridgeOnly++
				} else if g.BlockReason == "in_use" {
					info.GroupsInUse++
				} else if g.Assignable {
					info.GroupsAssignable++
				}
			}
		}
	}

	return info, nil
}
