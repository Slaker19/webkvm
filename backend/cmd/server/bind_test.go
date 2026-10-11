package main

import "testing"

func TestIsLoopbackBind(t *testing.T) {
	for h, want := range map[string]bool{"127.0.0.1": true, "::1": true, "[::1]": true, "localhost": true, "127.1.2.3": true, "0.0.0.0": false, "::": false, "": false, "192.168.1.5": false} {
		if got := isLoopbackBind(h); got != want {
			t.Errorf("isLoopbackBind(%q) = %v, want %v", h, got, want)
		}
	}
}
