package backupstore

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testHostKey returns a fresh ssh.PublicKey (ed25519) plus its full
// fingerprint string in ssh-keyscan format.
func testHostKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return pub, pub.Type() + " " + ssh.FingerprintSHA256(pub)
}

func invokeHostKeyCallback(t *testing.T, cb ssh.HostKeyCallback, key ssh.PublicKey) error {
	t.Helper()
	return cb("backup.example.com", &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 22}, key)
}

// V13-BCK-05: a configured target with NO pinned fingerprints must be
// REFUSED, and the error must surface the presented key so the operator
// can copy it into the target's known_hosts to authorize it.
func TestHostKeyCallback_NoFingerprintsRefusesAndSurfacesKey(t *testing.T) {
	pub, full := testHostKey(t)
	tgt := Target{ID: "t_x", Type: TargetSFTP, KnownHosts: nil}
	err := invokeHostKeyCallback(t, hostKeyCallbackFor(tgt), pub)
	if err == nil {
		t.Fatal("expected dial to be refused when no fingerprints are configured")
	}
	if !strings.Contains(err.Error(), "not trusted") {
		t.Errorf("error should say the key is not trusted: %v", err)
	}
	if !strings.Contains(err.Error(), full) {
		t.Errorf("error should embed the presented fingerprint %q: %v", full, err)
	}
}

// V13-BCK-05: a mismatched key (server reprovisioned or MITM) is refused
// with an error naming both the presented and the expected fingerprint.
func TestHostKeyCallback_MismatchRefused(t *testing.T) {
	keyA, _ := testHostKey(t)
	keyB, fullB := testHostKey(t)
	tgt := Target{ID: "t_y", Type: TargetSFTP, KnownHosts: []string{fullAOf(keyA, t)}}
	err := invokeHostKeyCallback(t, hostKeyCallbackFor(tgt), keyB)
	if err == nil {
		t.Fatal("expected mismatch to be refused")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error should say the key does not match: %v", err)
	}
	if !strings.Contains(err.Error(), fullB) {
		t.Errorf("error should embed the presented fingerprint %q: %v", fullB, err)
	}
}

// V13-BCK-05: a matching fingerprint is accepted (both the bare
// "SHA256:…" form and the full "ssh-ed25519 SHA256:…" line from a dial
// error / ssh-keyscan).
func TestHostKeyCallback_MatchingAccepted(t *testing.T) {
	pub, full := testHostKey(t)
	shaPart := strings.TrimPrefix(full, pub.Type()+" ")
	for _, tc := range []struct {
		name    string
		known   []string
		present ssh.PublicKey
	}{
		{"bare sha256", []string{shaPart}, pub},
		{"full line", []string{full}, pub},
		{"full line pasted from dial error", []string{full + " comment"}, pub},
		{"newline separated list", []string{"ignored\n" + full}, pub},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tgt := Target{ID: "t_z", Type: TargetSFTP, KnownHosts: tc.known}
			if err := invokeHostKeyCallback(t, hostKeyCallbackFor(tgt), tc.present); err != nil {
				t.Fatalf("expected match to be accepted: %v", err)
			}
		})
	}
}

func fullAOf(key ssh.PublicKey, t *testing.T) string {
	t.Helper()
	return key.Type() + " " + ssh.FingerprintSHA256(key)
}

// V13-BCK-05: the ad-hoc "Test connection" dial (empty target ID) has
// nothing to pin against yet — it is allowed through, but the presented
// key is recorded so TestSFTP can surface it.
func TestHostKeyCallback_AdHocTestAllowedAndRecorded(t *testing.T) {
	pub, full := testHostKey(t)
	tgt := Target{Type: TargetSFTP} // no ID
	if err := invokeHostKeyCallback(t, hostKeyCallbackFor(tgt), pub); err != nil {
		t.Fatalf("ad-hoc test dial should be allowed: %v", err)
	}
	if got := LastPresentedHostKey(); got != full {
		t.Fatalf("LastPresentedHostKey = %q, want %q", got, full)
	}
}

// V13-BCK-05: configuredFingerprints tolerates bare SHA256 tokens,
// full ssh-keyscan lines and whitespace/newline-separated lists.
func TestConfiguredFingerprints(t *testing.T) {
	got := configuredFingerprints([]string{
		"ssh-ed25519 SHA256:AAA bbb",
		"SHA256:CCC",
		"\t ssh-rsa SHA256:DDD \n extra",
		"not-a-fingerprint",
		"",
	})
	for _, want := range []string{"SHA256:AAA", "SHA256:CCC", "SHA256:DDD"} {
		if !got[want] {
			t.Errorf("configuredFingerprints missing %s (got %v)", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("configuredFingerprints has %d entries, want 3: %v", len(got), got)
	}
}
