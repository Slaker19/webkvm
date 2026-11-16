package main

import "testing"

func TestIsLoopbackBind(t *testing.T) {
	for h, want := range map[string]bool{"127.0.0.1": true, "::1": true, "[::1]": true, "localhost": true, "127.1.2.3": true, "0.0.0.0": false, "::": false, "": false, "192.168.1.5": false} {
		if got := isLoopbackBind(h); got != want {
			t.Errorf("isLoopbackBind(%q) = %v, want %v", h, got, want)
		}
	}
}

func TestEffectiveBindAddr(t *testing.T) {
	cases := []struct {
		bind, want                string
		tlsEnabled, allowInsecure bool
	}{
		{"0.0.0.0", "0.0.0.0", true, false},
		{"0.0.0.0", "0.0.0.0", false, true},
		{"0.0.0.0", "127.0.0.1", false, false},
		{"192.168.1.5", "127.0.0.1", false, false},
		{"::", "127.0.0.1", false, false},
		{"127.0.0.1", "127.0.0.1", false, false},
		{"localhost", "localhost", false, false},
	}
	for _, tc := range cases {
		if got := effectiveBindAddr(tc.bind, tc.tlsEnabled, tc.allowInsecure); got != tc.want {
			t.Errorf("effectiveBindAddr(%q, tls=%v, insecure=%v) = %q, want %q", tc.bind, tc.tlsEnabled, tc.allowInsecure, got, tc.want)
		}
	}
}
