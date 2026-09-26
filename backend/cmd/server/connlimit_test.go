package main

import "testing"

type fakeCIDRSettings []string

func (f fakeCIDRSettings) GetList(string) []string { return f }

func TestPerIPLimitExempt(t *testing.T) {
	t.Setenv("WEBKVM_TRUSTED_PROXY_CIDRS", "10.0.0.0/8, bogus")
	exempt := perIPLimitExempt(fakeCIDRSettings{"192.168.5.0/24"})
	cases := map[string]bool{
		"127.0.0.1":   true,
		"::1":         true,
		"10.1.2.3":    true,
		"192.168.5.9": true,
		"192.168.6.9": false,
		"8.8.8.8":     false,
		"":            false,
		"not-an-ip":   false,
	}
	for ip, want := range cases {
		if got := exempt(ip); got != want {
			t.Errorf("exempt(%q) = %v, want %v", ip, got, want)
		}
	}
	if perIPLimitExempt(nil)("8.8.8.8") {
		t.Error("nil settings must not exempt arbitrary IPs")
	}
}
