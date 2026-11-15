package libvirt

import (
	"fmt"
	"path"
	"strings"

	"webkvm/internal/models"
)

// ValidPoolName reports whether name is an acceptable storage pool name
// (letters, digits, '_', '-', '.'; no traversal). Exported for the API layer,
// which validates names a caller sends for a remote node before connecting.
func ValidPoolName(name string) bool { return validPoolName(name) }

// IsMediaVolumeName reports whether a volume name is boot media WebKVM lists
// as an ISO (.iso / .img), the same rule GetISOs applies.
func IsMediaVolumeName(name string) bool { return isISOPoolFile(name) }

// validRemoteVolumePath is the syntactic gate for a media path that must name
// a storage volume on a REMOTE node: absolute, already clean, no traversal,
// no control characters or quotes. Existence is checked through libvirt.
func validRemoteVolumePath(p string) bool {
	if p == "" || !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	if strings.Contains(p, "..") || strings.ContainsAny(p, "\x00\n\r\t'\"\\<>&") {
		return false
	}
	return true
}

// resolveCreateMedia resolves an ISO / virtio ISO reference for CreateDomain.
// Local connectors keep the filesystem-confined resolution. A remote
// connector must never look at THIS host's filesystem: the reference has to
// be the path of a storage volume the node's libvirt knows (the API layer
// resolved it from the node's pools by name), confirmed here through
// LookupStorageVolByPath on that connection.
func (c *Connector) resolveCreateMedia(media string, scope models.MediaScope) (string, error) {
	if !c.isRemote() {
		return c.resolveMediaPath(media, scope)
	}
	if !validRemoteVolumePath(media) {
		return "", fmt.Errorf("media file %q rejected: a remote node only accepts volumes of its storage pools", media)
	}
	conn := c.Get()
	if conn == nil {
		return "", fmt.Errorf("media file %q: node not connected", media)
	}
	vol, err := conn.LookupStorageVolByPath(media)
	if err != nil {
		return "", fmt.Errorf("media file %q not found in any storage pool of the node", media)
	}
	defer vol.Free()
	p, err := vol.GetPath()
	if err != nil || p == "" {
		return "", fmt.Errorf("media file %q: cannot read the volume path", media)
	}
	return p, nil
}
