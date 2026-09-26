package libvirt

import "testing"

// A domain with no <serial>/<console> can never serve a console, and the
// caller uses this to avoid a 30s retry that ends in a misleading
// "powered off or still booting" message about a running VM.
func TestHasSerialDevice(t *testing.T) {
	cases := []struct {
		name string
		xml  string
		want bool
	}{
		{"serial with attrs", `<devices><serial type='pty'><target port='0'/></serial></devices>`, true},
		{"bare serial", `<devices><serial></serial></devices>`, true},
		{"console only", `<devices><console type='pty'/></devices>`, true},
		{"no console devices", `<devices><interface type='bridge'/></devices>`, false},
		// The real-world false positive this guards against: a
		// virtio-serial controller and a guest-agent channel carry agent
		// traffic, not a console. Counting them would put us straight
		// back to the retry loop we are removing.
		{
			"virtio-serial controller and channel do not count",
			`<devices>
			   <controller type='virtio-serial' index='0'/>
			   <channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel>
			 </devices>`,
			false,
		},
		{"empty xml", ``, false},
	}
	for _, c := range cases {
		if got := hasSerialDevice(c.xml); got != c.want {
			t.Errorf("%s: hasSerialDevice = %v, want %v", c.name, got, c.want)
		}
	}
}
