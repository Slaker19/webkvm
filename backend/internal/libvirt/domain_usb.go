package libvirt

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"libvirt.org/go/libvirt"
	"webkvm/internal/models"
)

var (
	usbVendorRE  = regexp.MustCompile(`<vendor id='(0x[0-9a-fA-F]+)'(?:>([^<]*)</vendor>|/>)`)
	usbProductRE = regexp.MustCompile(`<product id='(0x[0-9a-fA-F]+)'(?:>([^<]*)</product>|/>)`)
	usbBusRE     = regexp.MustCompile(`<bus>(\d+)</bus>`)
	usbDeviceRE  = regexp.MustCompile(`<device>(\d+)</device>`)
)

// hostUSBRootHubVendor is the Linux Foundation vendor ID shared by
// every USB root hub; passing one through is never useful, so it's
// filtered out (same convention virt-manager uses).
const hostUSBRootHubVendor = "0x1d6b"

// ListHostUSBDevices enumerates USB devices on the host available
// for passthrough, excluding generic root hubs.
func (c *Connector) ListHostUSBDevices() ([]models.USBDevice, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}
	devs, err := c.conn.ListAllNodeDevices(libvirt.CONNECT_LIST_NODE_DEVICES_CAP_USB_DEV)
	if err != nil {
		// On split-daemon setups (Fedora/Arch) virtnodedevd may not be
		// running; the USB tab must never 500 the whole detail page.
		slog.Warn("list_host_usb_devices_unavailable", "err", err)
		return []models.USBDevice{}, nil
	}
	out := make([]models.USBDevice, 0, len(devs))
	for i := range devs {
		xmlDesc, err := devs[i].GetXMLDesc(0)
		name, _ := devs[i].GetName()
		devs[i].Free()
		if err != nil {
			continue
		}
		vendorID, vendorName := usbMatch(usbVendorRE, xmlDesc)
		productID, productName := usbMatch(usbProductRE, xmlDesc)
		if vendorID == "" || productID == "" || vendorID == hostUSBRootHubVendor {
			continue
		}
		bus := ""
		if m := usbBusRE.FindStringSubmatch(xmlDesc); len(m) > 1 {
			bus = m[1]
		}
		device := ""
		if m := usbDeviceRE.FindStringSubmatch(xmlDesc); len(m) > 1 {
			device = m[1]
		}
		label := strings.TrimSpace(vendorName + " " + productName)
		if label == "" {
			label = name
		}
		out = append(out, models.USBDevice{
			Name:      label,
			VendorID:  vendorID,
			ProductID: productID,
			Bus:       bus,
			Device:    device,
		})
	}
	return out, nil
}

func usbMatch(re *regexp.Regexp, xmlDesc string) (id, label string) {
	m := re.FindStringSubmatch(xmlDesc)
	if len(m) < 2 {
		return "", ""
	}
	if len(m) > 2 {
		label = strings.TrimSpace(m[2])
	}
	return m[1], label
}

var usbHostdevBlockRE = regexp.MustCompile(`(?s)<hostdev\b[^>]*type=['"]usb['"][^>]*>.*?</hostdev>`)

// parseUSBHostdevs extracts the USB devices already passed through
// to a domain from its XML, so the UI can offer a "Detach" action.
func parseUSBHostdevs(xmlDesc string) []models.USBDevice {
	blocks := usbHostdevBlockRE.FindAllString(xmlDesc, -1)
	out := make([]models.USBDevice, 0, len(blocks))
	for _, b := range blocks {
		vendorID, _ := usbMatch(usbVendorRE, b)
		productID, _ := usbMatch(usbProductRE, b)
		if vendorID == "" || productID == "" {
			continue
		}
		out = append(out, models.USBDevice{VendorID: vendorID, ProductID: productID})
	}
	return out
}

func usbHostdevXML(vendorID, productID string) string {
	return fmt.Sprintf(`<hostdev mode='subsystem' type='usb' managed='yes'>
  <source>
    <vendor id='%s'/>
    <product id='%s'/>
  </source>
</hostdev>`, xmlEscape(vendorID), xmlEscape(productID))
}

// AttachUSBDevice passes a host USB device (identified by vendor and
// product ID) through to a VM, live if running.
func (c *Connector) AttachUSBDevice(id, vendorID, productID string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	// Unlike AttachDisk/DetachDisk's CURRENT|CONFIG (CURRENT==0, so
	// that combination is really just CONFIG — takes effect on next
	// boot only), USB passthrough's whole point is being usable
	// against a running VM, so this explicitly requests LIVE too.
	flags := libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	state, _, err := dom.GetState()
	if err == nil && state == libvirt.DOMAIN_RUNNING {
		flags |= libvirt.DOMAIN_DEVICE_MODIFY_LIVE
	}
	return dom.AttachDeviceFlags(usbHostdevXML(vendorID, productID), flags)
}

// DetachUSBDevice removes a previously passed-through USB device from a VM.
func (c *Connector) DetachUSBDevice(id, vendorID, productID string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	// Unlike AttachDisk/DetachDisk's CURRENT|CONFIG (CURRENT==0, so
	// that combination is really just CONFIG — takes effect on next
	// boot only), USB passthrough's whole point is being usable
	// against a running VM, so this explicitly requests LIVE too.
	flags := libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	state, _, err := dom.GetState()
	if err == nil && state == libvirt.DOMAIN_RUNNING {
		flags |= libvirt.DOMAIN_DEVICE_MODIFY_LIVE
	}
	return dom.DetachDeviceFlags(usbHostdevXML(vendorID, productID), flags)
}
