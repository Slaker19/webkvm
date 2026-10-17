package libvirt

import (
	"fmt"
	"regexp"
	"strings"

	"libvirt.org/go/libvirt"
)

func (c *Connector) StartDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	state, _, err := dom.GetState()
	if err == nil && state == libvirt.DOMAIN_RUNNING {
		return ErrDomainAlreadyRunning
	}
	return dom.Create()
}

func (c *Connector) ShutdownDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	state, _, err := dom.GetState()
	if err == nil && state == libvirt.DOMAIN_SHUTOFF {
		return ErrDomainNotRunning
	}
	return dom.Shutdown()
}

func (c *Connector) ForceOffDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	state, _, err := dom.GetState()
	if err == nil && (state == libvirt.DOMAIN_SHUTOFF || state == libvirt.DOMAIN_CRASHED) {
		return nil
	}
	if err := dom.Destroy(); err != nil {
		// If Destroy returned an error, verify if the domain is actually already
		// shut off (e.g. QEMU process terminated concurrently or during monitor teardown).
		if st, _, stErr := dom.GetState(); stErr == nil && (st == libvirt.DOMAIN_SHUTOFF || st == libvirt.DOMAIN_CRASHED) {
			return nil
		}
		if act, actErr := dom.IsActive(); actErr == nil && !act {
			return nil
		}
		return err
	}
	return nil
}

func (c *Connector) RebootDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	state, _, err := dom.GetState()
	if err == nil && state == libvirt.DOMAIN_SHUTOFF {
		return ErrDomainNotRunning
	}
	return dom.Reboot(0)
}

func (c *Connector) SuspendDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	state, _, err := dom.GetState()
	if err == nil {
		if state == libvirt.DOMAIN_PAUSED {
			return ErrDomainAlreadyPaused
		}
		if state != libvirt.DOMAIN_RUNNING {
			return ErrDomainNotRunning
		}
	}
	return dom.Suspend()
}

func (c *Connector) ResumeDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	state, _, err := dom.GetState()
	if err == nil && state != libvirt.DOMAIN_PAUSED {
		return ErrDomainNotPaused
	}
	return dom.Resume()
}

func (c *Connector) DeleteDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	state, _, err := dom.GetState()
	if err == nil && state == libvirt.DOMAIN_RUNNING {
		dom.Destroy()
	}

	flags := libvirt.DOMAIN_UNDEFINE_MANAGED_SAVE | libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA | libvirt.DOMAIN_UNDEFINE_NVRAM
	return dom.UndefineFlags(flags)
}

func (c *Connector) GetBootDevice(id string) (string, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return "", err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return "", err
	}

	re := regexp.MustCompile(`<boot dev='([^']*)'/>`)
	m := re.FindStringSubmatch(xmlDesc)
	if len(m) > 1 {
		return m[1], nil
	}
	return "hd", nil // default
}

// SetBootDevice rewrites the domain's <boot dev='…'/> element. The
// device value is interpolated straight into the domain XML, so it is
// whitelisted first: without that an operator could close the <boot>
// attribute and inject arbitrary elements into the domain definition
// (the same values UpdateDomain accepts for boot_order). Accepts either
// "hd" (libvirt schema) or "disk" (API alias), normalising to "hd".
func (c *Connector) SetBootDevice(id string, device string) error {
	switch device {
	case "disk", "hd", "cdrom", "network":
	default:
		return fmt.Errorf("boot device must be one of: disk, hd, cdrom, network")
	}

	libvirtDev := device
	if libvirtDev == "disk" {
		libvirtDev = "hd"
	}

	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return err
	}

	newBoot := fmt.Sprintf("<boot dev='%s'/>", xmlEscape(libvirtDev))
	re := regexp.MustCompile(`<boot dev='[^']*'/>`)
	if re.MatchString(xmlDesc) {
		xmlDesc = re.ReplaceAllString(xmlDesc, newBoot)
	} else {
		// Add boot tag before </os>
		xmlDesc = strings.Replace(xmlDesc, "</os>", fmt.Sprintf("%s\n  </os>", newBoot), 1)
	}

	nd, err := c.defineDomain(xmlDesc, TrustedDomain)
	if err == nil {
		nd.Free()
	}
	return err
}

// SetDomainAutostart toggles whether libvirtd will start this
// domain automatically when the host boots. Backed by
// `virDomainSetAutostart` — does not affect the domain's current
// running state.
func (c *Connector) SetDomainAutostart(id string, enabled bool) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	return dom.SetAutostart(enabled)
}

// GetDomainAutostart returns the autostart flag for a domain.
func (c *Connector) GetDomainAutostart(id string) (bool, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return false, err
	}
	defer dom.Free()
	return dom.GetAutostart()
}
