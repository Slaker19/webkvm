package ha

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webkvm/internal/sysutil"
)

func mountRunner(out string, err error) *sysutil.MockCommandRunner {
	return &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "findmnt" {
			return nil, err
		}
		return []byte(out), err
	}}
}

func writeJWT(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jwt.key"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProbeLocalInstallSharedDataDir(t *testing.T) {
	dir := t.TempDir()
	writeJWT(t, dir, "shared-secret")
	runner := mountRunner(dir+" /dev/drbd1002\n/ /dev/sda2\n", nil)
	status := ProbeLocalInstall(context.Background(), runner, dir, PacemakerStatus{
		Resources: []PacemakerResource{{ID: "webkvm-app", Group: "webkvm-cp"}},
	})
	if !status.Shared || !status.Verified || status.Critical || status.DivergentJWT {
		t.Fatalf("shared DATA_DIR misclassified: %+v", status)
	}
}

func TestProbeLocalInstallOutsidePacemaker(t *testing.T) {
	dir := t.TempDir()
	writeJWT(t, dir, "local-secret")
	runner := mountRunner("/ /dev/sda2\n", nil)
	status := ProbeLocalInstall(context.Background(), runner, dir, PacemakerStatus{
		Resources: []PacemakerResource{{ID: "webkvm-app", Group: "webkvm-cp"}},
	})
	if !status.Critical || status.Shared || status.DivergentJWT {
		t.Fatalf("host-local DATA_DIR under Pacemaker must be critical: %+v", status)
	}
	if !strings.Contains(strings.Join(status.Details, " "), "host-local") {
		t.Fatalf("missing explanation: %+v", status.Details)
	}
}

func TestProbeLocalInstallIgnoresNonHAHost(t *testing.T) {
	dir := t.TempDir()
	writeJWT(t, dir, "local-secret")
	runner := mountRunner("/ /dev/sda2\n", nil)
	status := ProbeLocalInstall(context.Background(), runner, dir, PacemakerStatus{})
	if status.Critical || status.DivergentJWT {
		t.Fatalf("a host without Pacemaker must not be flagged: %+v", status)
	}
}

func TestProbeLocalInstallDivergentJWT(t *testing.T) {
	shared := t.TempDir()
	other := t.TempDir()
	writeJWT(t, shared, "shared-secret")
	writeJWT(t, other, "different-secret")
	runner := mountRunner(shared+" /dev/drbd1002\n"+other+" /dev/drbd1003\n/ /dev/sda2\n", nil)
	status := ProbeLocalInstall(context.Background(), runner, shared, PacemakerStatus{
		Resources: []PacemakerResource{{ID: "webkvm-app", Group: "webkvm-cp"}},
	})
	if !status.Shared || !status.DivergentJWT || status.Critical {
		t.Fatalf("divergent DRBD JWT not detected: %+v", status)
	}
}

func TestProbeLocalInstallUnreadableMounts(t *testing.T) {
	status := ProbeLocalInstall(context.Background(), mountRunner("", os.ErrPermission), "/opt/webkvm", PacemakerStatus{
		Resources: []PacemakerResource{{ID: "webkvm-app", Group: "webkvm-cp"}},
	})
	if status.Verified || status.Critical {
		t.Fatalf("unreadable mounts must not produce a critical finding: %+v", status)
	}
}
