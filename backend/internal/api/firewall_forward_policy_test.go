package api

import (
	"testing"

	"webkvm/internal/firewall"
)

// #22: DNAT target / port policy for per-VM forwards.
func TestValidateVMForwards(t *testing.T) {
	prot := firewall.ProtectedPorts(8081)
	fwd := func(port int, target string) []firewall.Forward {
		return []firewall.Forward{{Proto: "tcp", HostPort: port, GuestPort: 80, TargetIP: target}}
	}
	cases := []struct {
		name    string
		f       []firewall.Forward
		admin   bool
		vmIP    string
		wantErr bool
	}{
		{"own VM, high port", fwd(8080, ""), false, "192.168.122.10", false},
		{"explicit own IP", fwd(8080, "192.168.122.10"), false, "192.168.122.10", false},
		{"other VM's IP as operator", fwd(8080, "192.168.122.11"), false, "192.168.122.10", true},
		{"LAN host as operator", fwd(8080, "192.168.1.1"), false, "192.168.122.10", true},
		{"override with unknown VM IP", fwd(8080, "192.168.122.10"), false, "", true},
		{"admin may target another IP", fwd(8080, "192.168.122.11"), true, "192.168.122.10", false},
		{"IPv6 rejected for admin", fwd(8080, "fd00::1"), true, "192.168.122.10", true},
		{"IPv4-mapped IPv6 rejected", fwd(8080, "::ffff:10.0.0.1"), true, "", true},
		{"garbage rejected", fwd(8080, "not-an-ip"), true, "", true},
		{"protected ssh port", fwd(22, ""), true, "192.168.122.10", true},
		{"protected web port", fwd(8081, ""), true, "192.168.122.10", true},
		{"vnc range", fwd(5950, ""), true, "192.168.122.10", true},
		{"privileged port, operator", fwd(80, ""), false, "192.168.122.10", true},
		{"privileged port, admin", fwd(80, ""), true, "192.168.122.10", false},
		{"disabled forward ignored", []firewall.Forward{{HostPort: 22, Disabled: true}}, false, "", false},
	}
	for _, c := range cases {
		msg := validateVMForwards(c.f, c.admin, c.vmIP, prot)
		if (msg != "") != c.wantErr {
			t.Errorf("%s: msg=%q wantErr=%v", c.name, msg, c.wantErr)
		}
	}
}
