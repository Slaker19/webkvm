package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"webkvm/internal/models"
	"webkvm/internal/nodes"
	"webkvm/internal/sysutil"
)

func TestIsRemoteLibvirtURI(t *testing.T) {
	tests := []struct {
		uri  string
		want bool
	}{
		{"qemu+ssh://user@host/system", true},
		{"qemu+tcp://host:16509/system", true},
		{"qemu+ssh://user:password@host/system", false},
		{"https://host:8080", false},
		{"qemu:///system", false},
		{"qemu+ssh:///system", false},
		{"qemu+ssh://host/session", false},
		{"not a URI", false},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			if got := isRemoteLibvirtURI(tt.uri); got != tt.want {
				t.Fatalf("isRemoteLibvirtURI(%q) = %v, want %v", tt.uri, got, tt.want)
			}
		})
	}
}

func TestTestNodeConnectionRejectsUnsupportedURI(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/test", strings.NewReader(`{"uri":"https://host:8080"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.TestNodeConnection(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", res.Code, http.StatusBadRequest, res.Body.String())
	}
}

func TestNodeInstallSSHArgs(t *testing.T) {
	args, host, err := nodeInstallSSHArgs("qemu+ssh://operator@node.example:2222/system", false)
	if err != nil {
		t.Fatal(err)
	}
	if host != "node.example" {
		t.Fatalf("host = %q, want node.example", host)
	}
	want := []string{
		"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=yes",
		"-p", "2222", "operator@node.example", "sudo -n env WEBKVM_SSH_USER=operator WEBKVM_ALLOW_SYSTEM_UPGRADE=0 /bin/sh -s",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	rootArgs, _, err := nodeInstallSSHArgs("qemu+ssh://root@node.example/system", false)
	if err != nil || !strings.Contains(rootArgs[len(rootArgs)-1], "env WEBKVM_SSH_USER=root") || strings.Contains(rootArgs[len(rootArgs)-1], "sudo") {
		t.Fatalf("root SSH should not need sudo: %#v, %v", rootArgs, err)
	}
	upgradeArgs, _, err := nodeInstallSSHArgs("qemu+ssh://operator@node.example/system", true)
	if err != nil || !strings.Contains(upgradeArgs[len(upgradeArgs)-1], "WEBKVM_ALLOW_SYSTEM_UPGRADE=1") {
		t.Fatalf("Arch upgrade opt-in missing from args: %#v, %v", upgradeArgs, err)
	}
	if _, _, err := nodeInstallSSHArgs("qemu+ssh://operator:secret@node.example/system", false); err == nil {
		t.Fatal("expected URI password to be rejected")
	}
	if _, _, err := nodeInstallSSHArgs("qemu+tcp://node.example/system", false); err == nil {
		t.Fatal("expected TCP URI to be rejected for SSH install")
	}
}

func TestRunNodeDependencyInstallerPassesScriptOverStdin(t *testing.T) {
	called := false
	mock := &sysutil.MockCommandRunner{
		InputHandler: func(_ context.Context, name string, input []byte, args ...string) ([]byte, error) {
			called = true
			if name != "ssh" || !strings.Contains(string(input), "apt-get install") || !strings.Contains(string(input), "pacman -Syu") {
				t.Fatalf("unexpected installer command %q or script contents", name)
			}
			return []byte("installed"), nil
		},
	}
	out, host, err := runNodeDependencyInstaller(context.Background(), mock, "qemu+ssh://operator@node.example/system", true)
	if err != nil || string(out) != "installed" || host != "node.example" || !called {
		t.Fatalf("installer result = %q, %v; called=%v", out, err, called)
	}
}

func TestNodeDependencyInstallerScope(t *testing.T) {
	for _, required := range []string{
		"apt-get install", "dnf install", "pacman -Syu", "systemctl enable --now", "usermod -aG libvirt",
	} {
		if !strings.Contains(nodeDependenciesScript, required) {
			t.Errorf("installer script is missing %q", required)
		}
	}
	for _, forbidden := range []string{"authorized_keys", "ufw", "firewall-cmd", "iptables -"} {
		if strings.Contains(nodeDependenciesScript, forbidden) {
			t.Errorf("installer script unexpectedly changes SSH/firewall state via %q", forbidden)
		}
	}
}

func TestNodeDependencyInstallerRequiresArchOptIn(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pacman-was-run")
	for name, script := range map[string]string{
		"id":     "#!/bin/sh\nprintf '0\\n'\n",
		"pacman": "#!/bin/sh\n: > \"$WEBKVM_TEST_MARKER\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/sh", "-c", nodeDependenciesScript)
	cmd.Env = []string{
		"PATH=" + dir,
		"WEBKVM_SSH_USER=webkvm",
		"WEBKVM_TEST_MARKER=" + marker,
	}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "requires a full system upgrade") {
		t.Fatalf("Arch installer without opt-in: err=%v output=%q", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("pacman ran without opt-in (stat err=%v)", err)
	}
}

func TestRemoteVMRouteGuard(t *testing.T) {
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := reg.Create("remote", "qemu+ssh://user@example.test/system")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{nodes: reg}
	nextCalled := false
	guard := h.rejectUnsupportedRemoteVMRoute(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}))

	t.Run("supported remote read passes through", func(t *testing.T) {
		nextCalled = false
		req := httptest.NewRequest(http.MethodGet, "/api/vms/vm-1/disks?node=remote", nil)
		req.Header.Set("X-Role", models.RoleAdmin)
		res := httptest.NewRecorder()
		guard.ServeHTTP(res, req)
		if res.Code != http.StatusOK || !nextCalled {
			t.Fatalf("supported route: status=%d next=%v", res.Code, nextCalled)
		}
	})

	t.Run("unsupported remote route fails closed", func(t *testing.T) {
		nextCalled = false
		req := httptest.NewRequest(http.MethodGet, "/api/vms/vm-1/boot?node=remote", nil)
		req.Header.Set("X-Role", models.RoleAdmin)
		res := httptest.NewRecorder()
		guard.ServeHTTP(res, req)
		if res.Code != http.StatusNotImplemented || nextCalled {
			t.Fatalf("unsupported route: status=%d next=%v", res.Code, nextCalled)
		}
	})

	t.Run("disabled remote node is rejected", func(t *testing.T) {
		disabled := false
		if _, err := reg.Update(remote.ID, "", "", &disabled); err != nil {
			t.Fatal(err)
		}
		nextCalled = false
		req := httptest.NewRequest(http.MethodGet, "/api/vms/vm-1?node=remote", nil)
		req.Header.Set("X-Role", models.RoleAdmin)
		res := httptest.NewRecorder()
		guard.ServeHTTP(res, req)
		if res.Code != http.StatusNotFound || nextCalled {
			t.Fatalf("disabled node: status=%d next=%v", res.Code, nextCalled)
		}
	})
}

func TestRemoteIncusRouteGuardRejectsLibvirtOnlyOperations(t *testing.T) {
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := reg.CreateTyped(
		"incus",
		"https://incus.example:8443",
		nodes.BackendIncus,
		"default",
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{nodes: reg}
	guard := h.rejectUnsupportedRemoteVMRoute(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/vms/ct1/logs?node=" + remote.ID},
		{http.MethodPost, "/api/vms/ct1/migrate?node=" + remote.ID},
	} {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(test.method, test.path, nil)
		req.Header.Set("X-Role", models.RoleAdmin)
		guard.ServeHTTP(res, req)
		if res.Code != http.StatusNotImplemented {
			t.Errorf("%s status = %d, want %d", test.path, res.Code, http.StatusNotImplemented)
		}
	}
}

func TestOperatorForDoesNotTreatIncusEndpointAsLibvirt(t *testing.T) {
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := reg.CreateTyped(
		"incus",
		"https://incus.example:8443",
		nodes.BackendIncus,
		"default",
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{nodes: reg}

	if _, ok := h.nodeURI(remote.ID); ok {
		t.Fatal("Incus endpoint must not be exposed as a libvirt URI")
	}
	if _, release, err := h.operatorFor(remote.ID); err == nil {
		release()
		t.Fatal("operatorFor should fail on unavailable Incus credentials, not dial libvirt")
	} else if !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("operatorFor error = %v, want missing Incus credentials", err)
	}
}
