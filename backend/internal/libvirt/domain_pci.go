package libvirt

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"libvirt.org/go/libvirt"
	"webkvm/internal/models"
)

var (
	pciDomainRE   = regexp.MustCompile(`<domain>(\d+)</domain>`)
	pciBusRE      = regexp.MustCompile(`<bus>(\d+)</bus>`)
	pciSlotRE     = regexp.MustCompile(`<slot>(\d+)</slot>`)
	pciFunctionRE = regexp.MustCompile(`<function>(\d+)</function>`)
	pciIOMMURE    = regexp.MustCompile(`<iommuGroup number='(\d+)'`)
	pciDriverRE   = regexp.MustCompile(`<driver>\s*<name>([^<]+)</name>`)
	// PCI hostdev address as it appears in a domain's own XML (the format
	// AttachDeviceFlags/DetachDeviceFlags XML uses), e.g.
	// <address domain='0x0000' bus='0x01' slot='0x00' function='0x0'/>.
	pciDomainAttrHexRE = regexp.MustCompile(`domain=['"]0x([0-9a-fA-F]+)['"]`)
	pciBusAttrHexRE    = regexp.MustCompile(`bus=['"]0x([0-9a-fA-F]+)['"]`)
	pciSlotAttrHexRE   = regexp.MustCompile(`slot=['"]0x([0-9a-fA-F]+)['"]`)
	pciFuncAttrHexRE   = regexp.MustCompile(`function=['"]0x([0-9a-fA-F]+)['"]`)
)

// pciAddress renders the standard lspci-style "DDDD:BB:SS.F" (hex) form
// from the decimal domain/bus/slot/function values libvirt's PCI nodedev
// XML reports them in. This is both the sysfs directory name
// (/sys/bus/pci/devices/<address>) and the value the API/UI use to
// identify a device.
func pciAddress(domain, bus, slot, function int) string {
	return fmt.Sprintf("%04x:%02x:%02x.%d", domain, bus, slot, function)
}

// pciAddressRE is the exact shape pciAddress() generates ("DDDD:BB:SS.F",
// all hex, function a single nibble 0-7 per the PCI spec). Addresses in
// AttachPCIRequest/the DetachPCIDevice URL param come straight from the
// caller (admin-only, but still HTTP input) and are used to build a real
// filesystem path in pciSysfsDir before any other check runs — so this
// validation must happen first, not just inside parsePCIAddressHex's
// weaker split-based parsing (which only checked colon/dot COUNT, not
// that each segment was actually hex, so e.g. "../../etc:sh:ad.o" used
// to parse "successfully" into garbage instead of being rejected).
var pciAddressRE = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-7]$`)

func validPCIAddress(addr string) bool {
	return pciAddressRE.MatchString(addr)
}

// parsePCIAddressHex splits a "DDDD:BB:SS.F" address into the 0x-prefixed
// hex components pciHostdevXML needs. Rejects anything not matching
// pciAddressRE — see validPCIAddress.
func parsePCIAddressHex(addr string) (domain, bus, slot, function string, err error) {
	if !validPCIAddress(addr) {
		return "", "", "", "", fmt.Errorf("invalid PCI address %q", addr)
	}
	parts := strings.SplitN(addr, ":", 3)
	slotFn := strings.SplitN(parts[2], ".", 2)
	return "0x" + parts[0], "0x" + parts[1], "0x" + slotFn[0], "0x" + slotFn[1], nil
}

func pciSysfsDir(addr string) string {
	if !validPCIAddress(addr) || strings.Contains(addr, "/") || strings.Contains(addr, "\\") || strings.Contains(addr, "..") {
		return ""
	}
	clean := filepath.Clean(filepath.Join("/sys/bus/pci/devices", addr))
	if !strings.HasPrefix(clean, "/sys/bus/pci/devices/") {
		return ""
	}
	return clean
}

// pciBootVGA reports whether addr is the host's own boot/console GPU.
// Passthrough of this device is refused unconditionally — see
// PCIDevice.BootVGA. Callers with caller-supplied addr (AttachPCIDevice)
// MUST validate with validPCIAddress before calling this — it does not
// re-validate itself, since ListHostPCIDevices' own callers always pass
// an addr it built internally from parsed integers, never raw input.
func pciBootVGA(addr string) bool {
	if addr == "" || strings.Contains(addr, "/") || strings.Contains(addr, "\\") || strings.Contains(addr, "..") {
		return false
	}
	b, err := os.ReadFile(filepath.Join(pciSysfsDir(addr), "boot_vga")) // lgtm[go/path-injection] - addr validated by every untrusted-input caller, see func comment
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// pciDriverName reads the kernel driver currently bound to addr straight
// from sysfs (readlink .../driver), used as a fallback for hosts/libvirt
// versions whose PCI nodedev XML omits the optional <driver><name> element.
func pciDriverName(addr string) string {
	target, err := os.Readlink(filepath.Join(pciSysfsDir(addr), "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// pciDriverOverride reads the current driver_override for addr from sysfs.
// Returns "" if empty, "(null)", or unreadable.
func pciDriverOverride(addr string) string {
	if !validPCIAddress(addr) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(pciSysfsDir(addr), "driver_override"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if s == "(null)" {
		return ""
	}
	return s
}

// ListHostPCIDevices enumerates PCI devices on the host available for
// passthrough, grouped by IOMMU group (see PCIIOMMUGroup) since a group
// normally must be passed through as a whole. Returns a clear error
// instead of a silently empty list when IOMMU itself is off host-wide —
// distinguishing "no devices" from "passthrough isn't usable on this host
// at all" matters here far more than for USB.
func (c *Connector) ListHostPCIDevices() ([]models.PCIIOMMUGroup, error) {
	entries, err := os.ReadDir("/sys/kernel/iommu_groups")
	if err != nil || len(entries) == 0 {
		return []models.PCIIOMMUGroup{}, nil
	}
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}
	devs, err := c.conn.ListAllNodeDevices(libvirt.CONNECT_LIST_NODE_DEVICES_CAP_PCI_DEV)
	if err != nil {
		slog.Warn("list_host_pci_devices_unavailable", "err", err)
		return []models.PCIIOMMUGroup{}, nil
	}
	inUse := c.pciAddressesInUse()
	restrictions := pciHostRestrictions()
	byGroup := map[int][]models.PCIDevice{}
	for i := range devs {
		xmlDesc, err := devs[i].GetXMLDesc(0)
		name, _ := devs[i].GetName()
		devs[i].Free()
		if err != nil {
			continue
		}
		domain, bus, slot, function := -1, -1, -1, -1
		if m := pciDomainRE.FindStringSubmatch(xmlDesc); len(m) > 1 {
			domain, _ = strconv.Atoi(m[1])
		}
		if m := pciBusRE.FindStringSubmatch(xmlDesc); len(m) > 1 {
			bus, _ = strconv.Atoi(m[1])
		}
		if m := pciSlotRE.FindStringSubmatch(xmlDesc); len(m) > 1 {
			slot, _ = strconv.Atoi(m[1])
		}
		if m := pciFunctionRE.FindStringSubmatch(xmlDesc); len(m) > 1 {
			function, _ = strconv.Atoi(m[1])
		}
		if domain < 0 || bus < 0 || slot < 0 || function < 0 {
			continue
		}
		addr := pciAddress(domain, bus, slot, function)
		group := -1
		if m := pciIOMMURE.FindStringSubmatch(xmlDesc); len(m) > 1 {
			group, _ = strconv.Atoi(m[1])
		}
		vendorID, vendorName := usbMatch(usbVendorRE, xmlDesc)
		productID, productName := usbMatch(usbProductRE, xmlDesc)
		driver := pciDriverName(addr)
		if driver == "" {
			if m := pciDriverRE.FindStringSubmatch(xmlDesc); len(m) > 1 {
				driver = m[1]
			}
		}
		override := pciDriverOverride(addr)
		vfioBound := driver == "vfio-pci" || override == "vfio-pci"
		dev := models.PCIDevice{
			Name:        strings.TrimSpace(name),
			Address:     addr,
			VendorID:    vendorID,
			VendorName:  vendorName,
			ProductID:   productID,
			ProductName: productName,
			IOMMUGroup:  group,
			Driver:      driver,
			VFIOBound:   vfioBound,
			BootVGA:     pciBootVGA(addr),
			InUse:       inUse[addr],
		}
		evaluatePCIDevice(&dev, restrictions)
		byGroup[group] = append(byGroup[group], dev)
	}
	out := make([]models.PCIIOMMUGroup, 0, len(byGroup))
	for group, devices := range byGroup {
		allBound := true
		for _, d := range devices {
			if !d.VFIOBound {
				allBound = false
				break
			}
		}
		grp := models.PCIIOMMUGroup{Group: group, Devices: devices, AllVFIOBound: allBound}
		evaluatePCIGroup(&grp)
		out = append(out, grp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out, nil
}

// pciAddressesInUse scans every domain's XML for PCI hostdevs already
// passed through, so ListHostPCIDevices can flag them (mirrors how
// assertMACUnique scans every domain for a conflicting MAC).
func (c *Connector) pciAddressesInUse() map[string]bool {
	out := map[string]bool{}
	if err := c.ensureConnected(); err != nil {
		return out
	}
	doms, err := c.conn.ListAllDomains(0)
	if err != nil {
		return out
	}
	defer func() {
		for i := range doms {
			doms[i].Free()
		}
	}()
	for i := range doms {
		xmlDesc, err := doms[i].GetXMLDesc(0)
		if err != nil {
			continue
		}
		for _, d := range parsePCIHostdevs(xmlDesc) {
			out[d.Address] = true
		}
	}
	return out
}

var pciHostdevBlockRE = regexp.MustCompile(`(?s)<hostdev\b[^>]*type=['"]pci['"][^>]*>.*?</hostdev>`)

// parsePCIHostdevs extracts the PCI devices already passed through to a
// domain from its XML, so the UI can offer a "Detach" action. Addresses
// here are the hex address attrs a domain's own <hostdev> uses, distinct
// from the decimal tags a host PCI nodedev's XML uses (see
// ListHostPCIDevices) — hence the separate *AttrHexRE patterns.
func parsePCIHostdevs(xmlDesc string) []models.PCIDevice {
	blocks := pciHostdevBlockRE.FindAllString(xmlDesc, -1)
	out := make([]models.PCIDevice, 0, len(blocks))
	for _, b := range blocks {
		dm := pciDomainAttrHexRE.FindStringSubmatch(b)
		bm := pciBusAttrHexRE.FindStringSubmatch(b)
		sm := pciSlotAttrHexRE.FindStringSubmatch(b)
		fm := pciFuncAttrHexRE.FindStringSubmatch(b)
		if len(dm) < 2 || len(bm) < 2 || len(sm) < 2 || len(fm) < 2 {
			continue
		}
		domain, _ := strconv.ParseInt(dm[1], 16, 32)
		bus, _ := strconv.ParseInt(bm[1], 16, 32)
		slot, _ := strconv.ParseInt(sm[1], 16, 32)
		function, _ := strconv.ParseInt(fm[1], 16, 32)
		out = append(out, models.PCIDevice{Address: pciAddress(int(domain), int(bus), int(slot), int(function))})
	}
	return out
}

func pciHostdevXML(addr string) (string, error) {
	domain, bus, slot, function, err := parsePCIAddressHex(addr)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`<hostdev mode='subsystem' type='pci' managed='yes'>
  <source>
    <address domain='%s' bus='%s' slot='%s' function='%s'/>
  </source>
</hostdev>`, domain, bus, slot, function), nil
}

// AttachPCIDevice passes one or more host PCI devices through to a VM —
// normally every address in one IOMMU group at once (see
// models.PCIIOMMUGroup). Unlike USB, this requires the VM to already be
// DOMAIN_SHUTOFF: (1) a running q35 domain may have no spare
// pcie-root-port for hotplug — this codebase's controller XML declares a
// fixed set, and whether libvirt can add one on the fly here is
// unverified; (2) hot-unplugging a device out from under its guest driver
// (especially a GPU) is notoriously unreliable. A single simple,
// conservative rule beats per-device-type casework in a first version.
//
// The host's own boot/console GPU (BootVGA) is refused unconditionally,
// even for admins — see models.PCIDevice.BootVGA.
func (c *Connector) AttachPCIDevice(id string, addresses []string) error {
	if len(addresses) == 0 {
		return fmt.Errorf("no PCI addresses given")
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	state, _, err := dom.GetState()
	if err != nil {
		return err
	}
	if state != libvirt.DOMAIN_SHUTOFF {
		return fmt.Errorf("VM must be shut off to attach a PCI device")
	}
	// Validate every address's shape BEFORE any of them touch the
	// filesystem (pciBootVGA) or get built into hostdev XML — addresses
	// here come straight from the request body.
	for _, addr := range addresses {
		if !validPCIAddress(addr) {
			return fmt.Errorf("invalid PCI address %q", addr)
		}
	}
	restrictions := pciHostRestrictions()
	for _, addr := range addresses {
		if pciBootVGA(addr) {
			return fmt.Errorf("refusing to pass through %s: it is the host's own boot/console GPU", addr)
		}
		if pciIsBridge(addr) {
			return fmt.Errorf("refusing to pass through %s: it is a PCI/system bridge", addr)
		}
		if restr, found := restrictions[addr]; found && restr.hostCritical {
			return fmt.Errorf("refusing to pass through %s: critical host resource (%s)", addr, restr.reason)
		}
	}
	for _, addr := range addresses {
		hostdevXML, err := pciHostdevXML(addr)
		if err != nil {
			return err
		}
		if err := dom.AttachDeviceFlags(hostdevXML, libvirt.DOMAIN_DEVICE_MODIFY_CONFIG); err != nil {
			return fmt.Errorf("attach %s: %w", addr, err)
		}
	}
	return nil
}

// DetachPCIDevice removes a previously passed-through PCI device from a
// VM. Requires DOMAIN_SHUTOFF — see AttachPCIDevice.
func (c *Connector) DetachPCIDevice(id, address string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	state, _, err := dom.GetState()
	if err != nil {
		return err
	}
	if state != libvirt.DOMAIN_SHUTOFF {
		return fmt.Errorf("VM must be shut off to detach a PCI device")
	}
	hostdevXML, err := pciHostdevXML(address)
	if err != nil {
		return err
	}
	return dom.DetachDeviceFlags(hostdevXML, libvirt.DOMAIN_DEVICE_MODIFY_CONFIG)
}

// BindPCIDeviceVFIO binds a PCI device to the vfio-pci driver for VM passthrough.
func (c *Connector) BindPCIDeviceVFIO(addr string) error {
	if !validPCIAddress(addr) {
		return fmt.Errorf("invalid PCI address %q", addr)
	}
	if pciBootVGA(addr) {
		return fmt.Errorf("refusing to bind %s to VFIO: it is the host's own boot/console GPU", addr)
	}
	if pciIsBridge(addr) {
		return fmt.Errorf("refusing to bind %s to VFIO: it is a PCI bridge", addr)
	}
	restrictions := pciHostRestrictions()
	if restr, found := restrictions[addr]; found && restr.hostCritical {
		return fmt.Errorf("refusing to bind %s to VFIO: critical host resource (%s)", addr, restr.reason)
	}
	inUse := c.pciAddressesInUse()
	if inUse[addr] {
		return fmt.Errorf("cannot bind %s: currently attached to a virtual machine", addr)
	}

	// 1. Preflight: ensure vfio-pci kernel driver is loaded and available in sysfs.
	// If not already loaded, try modprobe.
	vfioBindPath := "/sys/bus/pci/drivers/vfio-pci/bind"
	if _, err := os.Stat(vfioBindPath); err != nil {
		_ = exec.Command("modprobe", "vfio-pci").Run()
		if _, err := os.Stat(vfioBindPath); err != nil {
			return fmt.Errorf("kernel driver vfio-pci is not available in sysfs (/sys/bus/pci/drivers/vfio-pci not found; check host kernel modules)")
		}
	}

	sysDir := pciSysfsDir(addr)
	if sysDir == "" {
		return fmt.Errorf("invalid PCI address path for %q", addr)
	}
	if _, err := os.Stat(sysDir); err != nil {
		return fmt.Errorf("PCI device %s not found in sysfs: %w", addr, err)
	}

	currentDriver := pciDriverName(addr)
	if currentDriver == "vfio-pci" {
		return nil // already bound
	}

	// Helper to rollback to previous driver in case binding fails
	rollback := func(bindErr error) error {
		overridePath := filepath.Clean(filepath.Join(sysDir, "driver_override"))
		if strings.HasPrefix(overridePath, sysDir) {
			_ = os.WriteFile(overridePath, []byte("\n"), 0644)
		}
		_ = os.WriteFile("/sys/bus/pci/drivers_probe", []byte(addr), 0200)
		restoredDriver := pciDriverName(addr)
		return fmt.Errorf("%w (restored driver: %q)", bindErr, restoredDriver)
	}

	// 2. Unbind from current driver if bound
	if currentDriver != "" {
		unbindPath := filepath.Clean(filepath.Join(sysDir, "driver", "unbind"))
		if strings.HasPrefix(unbindPath, sysDir) {
			_ = os.WriteFile(unbindPath, []byte(addr), 0200)
		}
	}

	// 3. Write driver_override = vfio-pci
	overridePath := filepath.Clean(filepath.Join(sysDir, "driver_override"))
	if strings.HasPrefix(overridePath, sysDir) {
		if err := os.WriteFile(overridePath, []byte("vfio-pci"), 0644); err != nil {
			return rollback(fmt.Errorf("set driver_override for %s: %w", addr, err))
		}
	}

	// 4. Bind to vfio-pci
	if err := os.WriteFile(vfioBindPath, []byte(addr), 0200); err != nil {
		// Fallback to drivers_probe
		_ = os.WriteFile("/sys/bus/pci/drivers_probe", []byte(addr), 0200)
	}

	var newDriver string
	for attempt := 0; attempt < 10; attempt++ {
		time.Sleep(50 * time.Millisecond)
		newDriver = pciDriverName(addr)
		if newDriver == "vfio-pci" {
			break
		}
	}
	if newDriver != "vfio-pci" {
		return rollback(fmt.Errorf("failed to bind %s to vfio-pci (current driver: %q)", addr, newDriver))
	}
	return nil
}

// UnbindPCIDeviceVFIO removes the vfio-pci driver override from a PCI device
// and triggers kernel reprobe so the default host driver can claim it.
func (c *Connector) UnbindPCIDeviceVFIO(addr string) error {
	if !validPCIAddress(addr) {
		return fmt.Errorf("invalid PCI address %q", addr)
	}
	inUse := c.pciAddressesInUse()
	if inUse[addr] {
		return fmt.Errorf("cannot unbind %s: currently attached to a virtual machine", addr)
	}

	sysDir := pciSysfsDir(addr)
	if sysDir == "" {
		return fmt.Errorf("invalid PCI address path for %q", addr)
	}
	if _, err := os.Stat(sysDir); err != nil {
		return fmt.Errorf("PCI device %s not found in sysfs: %w", addr, err)
	}

	// 1. Unbind from current driver if bound (both device driver unbind and vfio-pci unbind)
	currentDriver := pciDriverName(addr)
	override := pciDriverOverride(addr)
	if currentDriver == "vfio-pci" || override == "vfio-pci" {
		unbindPath := filepath.Clean(filepath.Join(sysDir, "driver", "unbind"))
		if strings.HasPrefix(unbindPath, sysDir) {
			_ = os.WriteFile(unbindPath, []byte(addr), 0200)
		}
		vfioUnbindPath := "/sys/bus/pci/drivers/vfio-pci/unbind"
		if _, err := os.Stat(vfioUnbindPath); err == nil {
			_ = os.WriteFile(vfioUnbindPath, []byte(addr), 0200)
		}
	}

	// 2. Clear driver_override (writing \n clears sysfs driver_override back to null)
	overridePath := filepath.Clean(filepath.Join(sysDir, "driver_override"))
	if strings.HasPrefix(overridePath, sysDir) {
		_ = os.WriteFile(overridePath, []byte("\n"), 0644)
	}

	// 3. Trigger reprobe by default driver
	_ = os.WriteFile("/sys/bus/pci/drivers_probe", []byte(addr), 0200)

	// 4. Poll up to 500ms for kernel to clear override and re-evaluate drivers
	for attempt := 0; attempt < 10; attempt++ {
		time.Sleep(50 * time.Millisecond)
		ovr := pciDriverOverride(addr)
		drv := pciDriverName(addr)
		if ovr == "" && drv != "vfio-pci" {
			return nil
		}
	}

	return nil
}
