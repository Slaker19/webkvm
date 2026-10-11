package libvirt

import (
	"fmt"
	"regexp"

	"libvirt.org/go/libvirt"
	"webkvm/internal/models"
)

// --- 9p shared folders ---
//
// virtiofs was considered and rejected: it needs a separately-supervised
// virtiofsd helper process per share (a process-supervision class this
// codebase doesn't have anywhere else) plus <memoryBacking> the domain XML
// here never sets. 9p needs neither.

var (
	filesystemBlockRE = regexp.MustCompile(`(?s)<filesystem\b[^>]*>.*?</filesystem>`)
	fsSourceDirRE     = regexp.MustCompile(`<source\s+dir=['"]([^'"]+)['"]`)
	fsTargetDirRE     = regexp.MustCompile(`<target\s+dir=['"]([^'"]+)['"]`)
	fsReadonlyRE      = regexp.MustCompile(`<readonly\s*/>`)
)

// parseSharedFolders extracts the 9p shared folders already attached to a
// domain from its XML, so the UI can offer a "Detach" action.
func parseSharedFolders(xmlDesc string) []models.SharedFolder {
	blocks := filesystemBlockRE.FindAllString(xmlDesc, -1)
	out := make([]models.SharedFolder, 0, len(blocks))
	for _, b := range blocks {
		sm := fsSourceDirRE.FindStringSubmatch(b)
		tm := fsTargetDirRE.FindStringSubmatch(b)
		if len(sm) < 2 || len(tm) < 2 {
			continue
		}
		out = append(out, models.SharedFolder{
			HostPath: sm[1],
			Tag:      tm[1],
			ReadOnly: fsReadonlyRE.MatchString(b),
		})
	}
	return out
}

func sharedFolderXML(hostPath, tag string, readOnly bool) string {
	ro := ""
	if readOnly {
		ro = "\n  <readonly/>"
	}
	return fmt.Sprintf(`<filesystem type='mount' accessmode='mapped'>
  <driver type='path'/>
  <source dir='%s'/>
  <target dir='%s'/>%s
</filesystem>`, xmlEscape(hostPath), xmlEscape(tag), ro)
}

// AttachSharedFolder adds a 9p shared folder to a VM. Like PCI passthrough
// (and unlike USB), this requires the VM to already be DOMAIN_SHUTOFF:
// hot-plug support for <filesystem> devices is unverified on the
// libvirt/QEMU versions this codebase targets, so a clean "shut off first"
// requirement beats risking an unpredictable live-attach result.
func (c *Connector) AttachSharedFolder(id, hostPath, tag string, readOnly bool) error {
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
		return fmt.Errorf("VM must be shut off to attach a shared folder")
	}
	return dom.AttachDeviceFlags(sharedFolderXML(hostPath, tag, readOnly), libvirt.DOMAIN_DEVICE_MODIFY_CONFIG)
}

// DetachSharedFolder removes a previously attached 9p shared folder,
// identified by its guest-visible tag. Requires DOMAIN_SHUTOFF — see
// AttachSharedFolder.
func (c *Connector) DetachSharedFolder(id, tag string) error {
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
		return fmt.Errorf("VM must be shut off to detach a shared folder")
	}
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}
	var target string
	for _, b := range filesystemBlockRE.FindAllString(xmlDesc, -1) {
		if tm := fsTargetDirRE.FindStringSubmatch(b); len(tm) > 1 && tm[1] == tag {
			target = b
			break
		}
	}
	if target == "" {
		return fmt.Errorf("shared folder with tag '%s' not found", tag)
	}
	return dom.DetachDeviceFlags(target, libvirt.DOMAIN_DEVICE_MODIFY_CONFIG)
}
