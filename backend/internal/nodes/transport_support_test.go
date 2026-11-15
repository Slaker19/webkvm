package nodes

import "testing"

func TestEffectiveTransportFallsBackOnlyFromLibSSH2(t *testing.T) {
	ResetTransportSupport()
	t.Cleanup(ResetTransportSupport)
	for _, tr := range []Transport{TransportLibSSH2, TransportLibSSH, TransportSSH} {
		if got, fb := EffectiveTransport(tr); got != tr || fb {
			t.Fatalf("no evidence yet: %s -> %s (fallback %v)", tr, got, fb)
		}
	}
	if !MarkTransportUnsupported(TransportLibSSH2) {
		t.Fatal("first mark must report news")
	}
	if MarkTransportUnsupported(TransportLibSSH2) {
		t.Fatal("second mark is cached, not news")
	}
	if !TransportUnsupported(TransportLibSSH2) {
		t.Fatal("not cached")
	}
	// The preferred default falls back to libssh (same key file, same pinned
	// known_hosts, strict verification); explicit choices are honoured.
	if got, fb := EffectiveTransport(TransportLibSSH2); got != TransportLibSSH || !fb {
		t.Fatalf("libssh2 unsupported -> %s %v, want libssh true", got, fb)
	}
	if got, fb := EffectiveTransport(TransportSSH); got != TransportSSH || fb {
		t.Fatalf("explicit ssh must not change: %s %v", got, fb)
	}
	// libssh unsupported too: an explicit libssh choice is never rewritten.
	MarkTransportUnsupported(TransportLibSSH)
	if got, _ := EffectiveTransport(TransportLibSSH); got != TransportLibSSH {
		t.Fatalf("explicit libssh rewritten to %s", got)
	}
	// Both built-in clients missing: libssh2 keeps failing with its typed
	// code (never silently switches to the system ssh binary, which uses a
	// different known_hosts).
	if got, fb := EffectiveTransport(TransportLibSSH2); got != TransportLibSSH2 || fb {
		t.Fatalf("no usable fallback: %s %v", got, fb)
	}
}

func TestTransportOfURI(t *testing.T) {
	for uri, want := range map[string]Transport{
		"qemu+libssh2://root@h:22/system?keyfile=/k": TransportLibSSH2,
		"qemu+libssh://root@h:22/system":             TransportLibSSH,
		"qemu+ssh://root@h/system":                   TransportSSH,
		"qemu:///system":                             "",
		"%%":                                         "",
	} {
		if got := TransportOfURI(uri); got != want {
			t.Errorf("TransportOfURI(%q) = %q, want %q", uri, got, want)
		}
	}
}
