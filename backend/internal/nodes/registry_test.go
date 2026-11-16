package nodes

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryCreateLocal(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("list = %d, want 1", len(got))
	}
	if !got[0].IsLocal() {
		t.Fatal("first node should be local")
	}
	if got[0].URI != "qemu:///system" {
		t.Fatalf("uri = %q", got[0].URI)
	}
}

func TestRegistryCreateRemote(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	n, err := r.Create("node-2", "qemu+ssh://root@10.0.0.2/system")
	if err != nil {
		t.Fatal(err)
	}
	if n.IsLocal() {
		t.Fatal("expected remote node")
	}
	if n.Name != "node-2" {
		t.Fatalf("name = %q", n.Name)
	}
	if r.nodes[n.ID] == nil {
		t.Fatal("not in map")
	}
	if n.Backend != BackendLibvirt {
		t.Fatalf("backend = %q, want %q", n.Backend, BackendLibvirt)
	}
}

func TestRegistryCreateIncusRemotePersistsTypedTarget(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	const credentialID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const fingerprint = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	n, err := r.CreateTyped("incus-2", "https://incus.example:8443", BackendIncus, "containers", credentialID, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if n.Backend != BackendIncus || n.Project != "containers" || n.CredentialID != credentialID || n.ServerFingerprint != fingerprint {
		t.Fatalf("unexpected Incus node: %+v", n)
	}
	r2, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := r2.Get(n.ID)
	if !ok || loaded.Backend != BackendIncus || loaded.Project != "containers" || loaded.CredentialID != credentialID {
		t.Fatalf("Incus node did not survive registry reload: %+v, %v", loaded, ok)
	}
}

func TestOldRegistryDefaultsBackendToLibvirt(t *testing.T) {
	dir := t.TempDir()
	data := `[{"id":"local","name":"local","uri":"qemu:///system","type":"local","enabled":true}]`
	if err := os.WriteFile(filepath.Join(dir, "nodes.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	n, ok := r.Get("local")
	if !ok || n.Backend != BackendLibvirt {
		t.Fatalf("legacy local node backend = %q, ok=%v", n.Backend, ok)
	}
}

func TestLocalNodeUsesHostname(t *testing.T) {
	dir := t.TempDir()
	data := `[{"id":"local","name":"local","uri":"qemu:///system","type":"local","enabled":true}]`
	if err := os.WriteFile(filepath.Join(dir, "nodes.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	n, ok := r.Get("local")
	if !ok {
		t.Fatal("local node missing")
	}
	if want := localHostname(); n.Name != want {
		t.Fatalf("local node name = %q, want %q", n.Name, want)
	}
}

// The local identity is the machine we run on. A name persisted in the
// shared DATA_DIR by another member (the old active during a failover) must
// not survive.
func TestLocalNodeNameFollowsHostname(t *testing.T) {
	dir := t.TempDir()
	data := `[{"id":"local","name":"my-host","uri":"qemu:///system","type":"local","enabled":true}]`
	if err := os.WriteFile(filepath.Join(dir, "nodes.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := r.Get("local")
	if n.Name != LocalHostname() {
		t.Fatalf("local name = %q, want the live hostname %q", n.Name, LocalHostname())
	}
}

func TestIncusNodeProbeUsesAuthenticatedBackendProber(t *testing.T) {
	r, err := New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	const credentialID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const fingerprint = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	n, err := r.CreateTyped("incus-2", "https://incus.example:8443", BackendIncus, "containers", credentialID, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	r.SetIncusProber(func(got Node) (string, error) {
		called = true
		if got.ID != n.ID || got.Backend != BackendIncus || got.CredentialID != credentialID {
			t.Fatalf("prober received wrong node: %+v", got)
		}
		return "6.0", nil
	})
	got, err := r.ProbeNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !called || got.Status != "online" || got.Version != "6.0" {
		t.Fatalf("Incus probe result = %+v, called=%v", got, called)
	}
}

func TestRegistryUpdate(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	n, _ := r.Create("node-2", "qemu+ssh://root@10.0.0.2/system")
	enabled := false
	got, err := r.Update(n.ID, "node-2-renamed", "qemu+ssh://root@10.0.0.3/system", &enabled)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "node-2-renamed" || got.URI != "qemu+ssh://root@10.0.0.3/system" || got.Enabled {
		t.Fatalf("update: %+v", got)
	}
}

func TestRegistryCannotEditLocalURI(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	_, err := r.Update("local", "local", "qemu+tcp://other/system", nil)
	if err == nil {
		t.Fatal("expected error changing local URI")
	}
}

func TestRegistryDeleteRemote(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	n, _ := r.Create("n2", "qemu+ssh://root@10.0.0.2/system")
	if err := r.Delete(n.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get(n.ID); ok {
		t.Fatal("still in registry")
	}
}

func TestRegistryCannotDeleteLocal(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	if err := r.Delete("local"); err == nil {
		t.Fatal("expected error deleting local")
	}
}

func TestRegistryPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	_, _ = r.Create("n2", "qemu+ssh://root@10.0.0.2/system")
	r2, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.List(); len(got) != 2 {
		t.Fatalf("after reopen list = %d, want 2", len(got))
	}
}

func TestRegistryFileExists(t *testing.T) {
	dir := t.TempDir()
	_, _ = New(dir, "qemu:///system")
	if _, err := os.Stat(filepath.Join(dir, "nodes.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryDuplicateName(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	n, _ := r.Create("dup", "qemu+ssh://root@10.0.0.2/system")
	if _, err := r.Create("dup", "qemu+ssh://root@10.0.0.3/system"); err == nil {
		t.Fatal("expected dup error")
	}
	if _, err := r.Update("local", "dup", "", nil); err == nil {
		t.Fatal("expected duplicate name rejection on update")
	}
	if _, err := r.Update(n.ID, "   ", "", nil); err == nil {
		t.Fatal("expected empty normalized name rejection")
	}
}

func TestRegistryProbeLocalAndRemote(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}

	r.SetLocalProber(func() (string, string, int64, int64, int64, string, error) {
		return "online", "0.1.7", 3600, 50000000000, 100000000000, "ok", nil
	})

	localNode, err := r.ProbeNode("local")
	if err != nil {
		t.Fatalf("ProbeNode local failed: %v", err)
	}
	if localNode.Status != "online" || localNode.Version != "0.1.7" || localNode.LibvirtStatus != "ok" {
		t.Fatalf("unexpected local node probe: %+v", localNode)
	}

	// Remote node with invalid URI should be marked offline
	remoteNode, _ := r.Create("remote-offline", "https://127.0.0.1:54321")
	probed, _ := r.ProbeNode(remoteNode.ID)
	if probed.Status != "offline" {
		t.Fatalf("expected remote to be offline, got: %s", probed.Status)
	}

	summary := r.ClusterSummary()
	if summary.TotalNodes != 2 || summary.OnlineNodes != 1 || summary.OfflineNodes != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestRegistryProbeWritesBackTelemetry(t *testing.T) {
	r, err := New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	r.SetLocalProber(func() (string, string, int64, int64, int64, string, error) {
		return "online", "9.9.9", 120, 10, 20, "ok", nil
	})
	if _, err := r.ProbeNode("local"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range r.List() {
		if !n.IsLocal() {
			continue
		}
		found = true
		if n.Status != "online" || n.Version != "9.9.9" || n.LibvirtStatus != "ok" {
			t.Fatalf("local telemetry was not written back: %+v", n)
		}
	}
	if !found {
		t.Fatal("local node missing from List()")
	}

	remote, err := r.Create("remote", "https://127.0.0.1:54321")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ProbeNode(remote.ID); err == nil {
		t.Fatal("expected the unreachable remote probe to fail")
	}
	for _, n := range r.List() {
		if n.ID == remote.ID && n.Status != "offline" {
			t.Fatalf("remote offline status was not written back: %+v", n)
		}
	}
}

// A panel that failed over to another member must present itself with the
// hostname of the machine it is running on, not the one persisted in the
// shared DATA_DIR by the previous active node.
func TestEnsureLocalRefreshesIdentityAfterFailover(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.nodes["local"].Name = "node-c"
	r.mu.Unlock()
	if err := r.save(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	n, ok := restarted.Get("local")
	if !ok {
		t.Fatal("local node missing")
	}
	if n.Name != LocalHostname() {
		t.Fatalf("local name = %q, want %q (persisted identity must not win)", n.Name, LocalHostname())
	}
}

func TestProbeAllStaleHonorsTTL(t *testing.T) {
	r, err := New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	probes := 0
	r.SetLocalProber(func() (string, string, int64, int64, int64, string, error) {
		probes++
		return "online", "0.1.12", 1, 2, 3, "ok", nil
	})
	r.ProbeAllStale(time.Hour)
	r.ProbeAllStale(time.Hour)
	if probes != 1 {
		t.Fatalf("local probes = %d, want 1 inside the TTL", probes)
	}
	r.ProbeAllStale(0)
	if probes != 1 {
		t.Fatalf("local probes = %d, want 1 (default TTL not expired)", probes)
	}
	r.mu.Lock()
	r.lastProbe["local"] = time.Now().Add(-time.Hour)
	r.mu.Unlock()
	r.ProbeAllStale(time.Second)
	if probes != 2 {
		t.Fatalf("local probes = %d, want 2 after the TTL expired", probes)
	}
}

func TestRegistrySetDrainingPersists(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	n, err := r.Create("drain-target", "qemu+ssh://root@10.0.0.9/system")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.SetDraining(n.ID, true, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Draining || got.DrainSince == nil || got.DrainBy != "admin" {
		t.Fatalf("drain state = %+v", got)
	}

	reloaded, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reloaded.Get(n.ID)
	if !ok || !persisted.Draining || persisted.DrainBy != "admin" || persisted.DrainSince == nil {
		t.Fatalf("persisted drain state = %+v (ok=%v)", persisted, ok)
	}

	cleared, err := reloaded.SetDraining(n.ID, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Draining || cleared.DrainSince != nil || cleared.DrainBy != "" {
		t.Fatalf("cleared drain state = %+v", cleared)
	}
	if _, err := reloaded.SetDraining("missing", true, "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing node error = %v, want ErrNotFound", err)
	}
}
