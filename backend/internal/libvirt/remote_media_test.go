package libvirt

import (
	"strings"
	"testing"

	"webkvm/internal/models"
)

func TestValidRemoteVolumePath(t *testing.T) {
	for _, p := range []string{"/srv/isos/debian.iso", "/var/lib/libvirt/images/a b.img"} {
		if !validRemoteVolumePath(p) {
			t.Errorf("%q rejected", p)
		}
	}
	for _, p := range []string{"", "debian.iso", "srv/x.iso", "/srv/../etc/passwd", "/srv//x.iso", "/srv/x.iso/",
		"/srv/x'.iso", "/srv/x\n.iso", "/srv/x<y>.iso", "/srv/a\\b.iso", "/srv/x\".iso"} {
		if validRemoteVolumePath(p) {
			t.Errorf("%q accepted", p)
		}
	}
}

// A remote connector never resolves media against THIS host's filesystem:
// even a file that exists locally is refused unless the node's libvirt
// reports it as one of its volumes.
func TestResolveCreateMediaRemoteNeverUsesLocalFS(t *testing.T) {
	c := NewTestConnector("qemu+ssh://user@node2.example/system")
	for _, m := range []string{"/etc/hostname", "debian.iso", "../x.iso"} {
		if _, err := c.resolveCreateMedia(m, models.MediaScope{Admin: true}); err == nil {
			t.Fatalf("%q resolved without the node", m)
		}
	}
	if _, err := c.resolveCreateMedia("/srv/isos/debian.iso", models.MediaScope{Admin: true}); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("unconnected node: %v", err)
	}
}

func TestMediaHelpersExported(t *testing.T) {
	if !ValidPoolName("east-isos") || ValidPoolName("../x") || ValidPoolName("..") {
		t.Fatal("ValidPoolName")
	}
	if !IsMediaVolumeName("a.ISO") || !IsMediaVolumeName("b.img") || IsMediaVolumeName("c.qcow2") {
		t.Fatal("IsMediaVolumeName")
	}
}
