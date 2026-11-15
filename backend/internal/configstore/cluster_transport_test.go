package configstore

import "testing"

func TestClusterLibvirtTransportDefaultAndValidation(t *testing.T) {
	s, err := New(t.TempDir(), DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.GetString("cluster.libvirt_transport"); got != "libssh2" {
		t.Fatalf("default = %q, want libssh2", got)
	}
	for _, bad := range []string{"tcp", "tls", "libssh3", "qemu+libssh2", "", "ssh?no_verify=1"} {
		_, failed, _ := s.SetMany(Set{"cluster.libvirt_transport": bad})
		if failed["cluster.libvirt_transport"] == "" {
			t.Errorf("value %q accepted", bad)
		}
	}
	// Stored values from before libssh2 existed keep working.
	for _, ok := range []string{"libssh", "libssh2", "ssh"} {
		if _, failed, err := s.SetMany(Set{"cluster.libvirt_transport": ok}); err != nil || len(failed) != 0 {
			t.Fatalf("%s rejected: %v %v", ok, failed, err)
		}
	}
	if _, failed, err := s.SetMany(Set{"cluster.libvirt_transport": "ssh"}); err != nil || len(failed) != 0 {
		t.Fatalf("ssh rejected: %v %v", failed, err)
	}
	if got := s.GetString("cluster.libvirt_transport"); got != "ssh" {
		t.Fatalf("after set = %q", got)
	}
}
