package libvirt

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"libvirt.org/go/libvirt"
)

// qemuOwner returns the uid/gid that libvirt runs QEMU as. Debian uses
// libvirt-qemu; other distributions use qemu.
func qemuOwner() (int, int, bool) {
	for _, name := range []string{"libvirt-qemu", "qemu"} {
		u, err := user.Lookup(name)
		if err != nil {
			continue
		}
		uid, uerr := strconv.Atoi(u.Uid)
		gid, gerr := strconv.Atoi(u.Gid)
		if uerr == nil && gerr == nil {
			return uid, gid, true
		}
	}
	return 0, 0, false
}

// chownDiskChain makes every file of each disk chain (base image, external
// snapshot overlays and the active overlay) readable/writable by QEMU.
// libvirt chowns the active disk on start but not its backing files, so a
// VM with external snapshots would otherwise fail to boot after a restart
// when the base is still root-owned.
func (c *Connector) chownDiskChain(dom *libvirt.Domain) {
	uid, gid, ok := qemuOwner()
	if !ok {
		return
	}
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, d := range c.parseDisksFiltered(xmlDesc, false) {
		if d.Source == "" || d.Device == "cdrom" || strings.HasPrefix(d.Source, "/dev/") {
			continue
		}
		dir := filepath.Dir(d.Source)
		base := filepath.Base(d.Source)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !matchesDiskChainFile(name, base, stem) {
				continue
			}
			path := filepath.Join(dir, name)
			if seen[path] {
				continue
			}
			seen[path] = true
			_ = os.Chown(path, uid, gid)
			_ = os.Chmod(path, 0o660)
		}
	}
}

// matchesDiskChainFile reports whether name belongs to the chain rooted at
// sourceBase. libvirt names external overlays "<stem>.<snapshot|timestamp>".
func matchesDiskChainFile(name, sourceBase, stem string) bool {
	if name == sourceBase {
		return true
	}
	return stem != "" && strings.HasPrefix(name, stem+".")
}
