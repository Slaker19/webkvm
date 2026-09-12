package incus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
)

// A fake LXD daemon that records instance-state operations so we can
// assert the lifecycle mapping (start/stop/restart/force/delete) without
// a real LXD install.
type lifecycleFake struct {
	path       string
	stateCalls []string
	delCalls   int
}

func newLifecycleFake(t *testing.T) *lifecycleFake {
	t.Helper()
	f := &lifecycleFake{}
	f.path = filepath.Join(t.TempDir(), "unix.socket")
	l, err := net.Listen("unix", f.path)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":{"api_extensions":["instances","snapshots","instance_force_delete"],"api_status":"stable","api_version":"1.0","auth":"trusted","public":false,"environment":{"server_version":"6.0.0-fake","certificate":""}}}`))
	})
	mux.HandleFunc("/1.0/instances/", func(w http.ResponseWriter, r *http.Request) {
		// delete: DELETE /1.0/instances/{name}
		if r.Method == http.MethodDelete {
			f.delCalls++
			w.Write([]byte(`{"type":"async","status":"Success","status_code":100,"operation":"/1.0/operations/op1","metadata":{"id":"op1","status":"Running"}}`))
			return
		}
		// state: PUT /1.0/instances/{name}/state
		if r.Method == http.MethodPut && len(r.URL.Path) > 0 {
			var put api.InstanceStatePut
			decodeJSON(r, &put)
			f.stateCalls = append(f.stateCalls, put.Action+":"+bool2str(put.Force))
			w.Write([]byte(`{"type":"async","status":"Success","status_code":100,"operation":"/1.0/operations/op1","metadata":{"id":"op1","status":"Running"}}`))
			return
		}
		w.WriteHeader(404)
	})
	mux.HandleFunc("/1.0/operations/op1/wait", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":{"id":"op1","status":"Success","err":"","metadata":{}}}`))
	})
	mux.HandleFunc("/1.0/operations/op1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":{"id":"op1","status":"Success","err":"","metadata":{}}}`))
	})
	go http.Serve(l, mux)
	t.Cleanup(func() { l.Close(); _ = os.Remove(f.path) })
	return f
}

func decodeJSON(r *http.Request, v any) {
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, v)
}

func bool2str(b bool) string {
	if b {
		return "force"
	}
	return "graceful"
}

// TestIncusBackendLifecycle: Start/Shutdown/ForceOff/Reboot/Delete map to
// the LXD state API with the right action + force flags and wait for the
// operation.
func TestIncusBackendLifecycle(t *testing.T) {
	f := newLifecycleFake(t)
	b, err := NewIncusBackend(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.StartDomain("web"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := b.ShutdownDomain("web"); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := b.ForceOffDomain("web"); err != nil {
		t.Fatalf("forceoff: %v", err)
	}
	if err := b.RebootDomain("web"); err != nil {
		t.Fatalf("reboot: %v", err)
	}
	if err := b.DeleteDomain("web"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := []string{"start:graceful", "stop:graceful", "stop:force", "restart:graceful"}
	if len(f.stateCalls) != 4 {
		t.Fatalf("state calls = %v, want %v", f.stateCalls, want)
	}
	for i, w := range want {
		if f.stateCalls[i] != w {
			t.Errorf("call %d = %q, want %q", i, f.stateCalls[i], w)
		}
	}
	if f.delCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", f.delCalls)
	}
}

// TestIncusBackendExecOnStopped: exec on a stopped instance returns
// compute.ErrDomainNotRunning so the serial proxy retries.
func TestIncusBackendExecOnStopped(t *testing.T) {
	// Reuse a fake that reports the instance as Stopped (no /instances/{n}
	// handler with Running state). The lifecycle fake returns 404 for
	// instance GETs, which mapLXErr turns into a not-found error — but the
	// exec path pre-checks GetInstanceState, which 404s -> not running is
	// not what we want here. Use a dedicated stopped-state fake instead.
	dir := t.TempDir()
	sock := filepath.Join(dir, "unix.socket")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":{"api_extensions":["instances"],"api_status":"stable","api_version":"1.0","auth":"trusted","public":false,"environment":{"server_version":"6.0.0-fake","certificate":""}}}`))
	})
	mux.HandleFunc("/1.0/instances/web/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":{"status":"Stopped","status_code":102}}`))
	})
	go http.Serve(l, mux)
	t.Cleanup(func() { l.Close() })

	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.OpenSerialConsole("web")
	if !errors.Is(err, compute.ErrDomainNotRunning) {
		t.Fatalf("exec on stopped instance: err = %v, want ErrDomainNotRunning", err)
	}
}

// TestLXDCloudInitConfig: a cloud-init request maps to user.user-data +
// user.network-config native LXD keys (no NoCloud ISO).
func TestLXDCloudInitConfig(t *testing.T) {
	cfg := cloudinit.Config{
		User:            "ubuntu",
		Password:        "pw",
		SSHKey:          "ssh-ed25519 AAAA... key@host",
		Hostname:        "web",
		ProvisionScript: "apt-get update",
	}
	keys, ok := lxdCloudInitConfig(cfg, "lxdbr0")
	if !ok {
		t.Fatal("expected a cloud-init config")
	}
	ud, hasUser := keys["user.user-data"]
	if !hasUser {
		t.Fatal("user.user-data missing")
	}
	if !contains(ud, "#cloud-config") || !contains(ud, "name: ubuntu") {
		t.Errorf("user-data malformed:\n%s", ud)
	}
	if nc, ok := keys["user.network-config"]; !ok || !contains(nc, "dhcp4: true") {
		t.Errorf("network-config missing/malformed: %q", nc)
	}
	// Empty request -> no keys (nothing to provision).
	if _, ok := lxdCloudInitConfig(cloudinit.Config{}, "lxdbr0"); ok {
		t.Error("empty config should produce no keys")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Sanity: the new lifecycle methods still satisfy the seam.
var _ = context.Background
var _ compute.Backend = (*IncusBackend)(nil)
