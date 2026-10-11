package api

import (
	"net"
	"testing"
)

func TestIsBlockedIP_ReservedAndNAT64(t *testing.T) {
	for _, s := range []string{"0.0.0.0", "0.9.9.9", "198.18.0.1", "198.19.1.1", "240.0.0.1", "255.255.255.255", "64:ff9b::a00:1", "64:ff9b:1::1"} {
		if !isBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "198.17.0.1", "198.20.0.1", "2606:4700::1111"} {
		if ip := net.ParseIP(s); ip != nil && isBlockedIP(ip) {
			t.Errorf("%s should not be blocked", s)
		}
	}
}
