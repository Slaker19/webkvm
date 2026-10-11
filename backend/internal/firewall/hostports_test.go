package firewall

import "testing"

// The VNC range must not be opened to the LAN any more (audit #14).
func TestHostPortsDoNotOpenVNC(t *testing.T) {
	got := HostPorts(8080)
	for _, p := range got {
		if p >= 5900 && p <= 5999 {
			t.Fatalf("HostPorts opens VNC port %d: %v", p, got)
		}
	}
	if len(got) != 2 || got[0] != 22 || got[1] != 8080 {
		t.Fatalf("HostPorts = %v, want [22 8080]", got)
	}
}
