package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Golden assertions for the issue #14 fix: the generated units must never
// carry the After=local-fs.target cycle, never drop default dependencies on
// the .mount (shutdown ordering), and never rely on x-systemd options inside
// a native .mount unit (fstab-generator only).
func TestRenderHostMountUnit(t *testing.T) {
	out := renderHostMountUnit("datapool", "/dev/disk/by-uuid/abc", "/mnt/storage-sda", "btrfs", "defaults,noatime,nofail")
	for _, bad := range []string{"After=local-fs.target", "DefaultDependencies=no", "x-systemd.automount"} {
		if strings.Contains(out, bad) {
			t.Errorf("mount unit contains %q:\n%s", bad, out)
		}
	}
	for _, want := range []string{"WantedBy=local-fs.target", "What=/dev/disk/by-uuid/abc", "Type=btrfs"} {
		if !strings.Contains(out, want) {
			t.Errorf("mount unit missing %q:\n%s", want, out)
		}
	}
}

func TestRenderHostAutomountUnit(t *testing.T) {
	out := renderHostAutomountUnit("datapool", "/mnt/storage-sda")
	if strings.Contains(out, "After=local-fs.target") {
		t.Errorf("automount unit still carries the ordering cycle:\n%s", out)
	}
	for _, want := range []string{"WantedBy=local-fs.target", "Where=/mnt/storage-sda", "TimeoutIdleSec=60"} {
		if !strings.Contains(out, want) {
			t.Errorf("automount unit missing %q:\n%s", want, out)
		}
	}
}

// Broken fixtures exactly as 0.1.13 / 0.1.14-rc.1 wrote them (issue #14).
const brokenMountFixture = `[Unit]
Description=WebKVM Mount for storage-sda (/mnt/storage-sda)
After=local-fs.target
DefaultDependencies=no
StartLimitIntervalSec=0

[Mount]
What=/dev/disk/by-uuid/abc123
Where=/mnt/storage-sda
Type=btrfs
Options=defaults,noatime,nofail,x-systemd.device-timeout=10,x-systemd.default-timeout=10,x-systemd.automount,x-systemd.idle-timeout=60

[Install]
WantedBy=local-fs.target
`

const brokenAutomountFixture = `[Unit]
Description=WebKVM Automount for storage-sda (/mnt/storage-sda)
After=local-fs.target
StartLimitIntervalSec=0

[Automount]
Where=/mnt/storage-sda
TimeoutIdleSec=0

[Install]
WantedBy=local-fs.target
`

func TestRepairGeneratedMountUnitFile(t *testing.T) {
	dir := t.TempDir()
	mountPath := filepath.Join(dir, "mnt-storage\\x2dsda.mount")
	automountPath := filepath.Join(dir, "mnt-storage\\x2dsda.automount")
	if err := os.WriteFile(mountPath, []byte(brokenMountFixture), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(automountPath, []byte(brokenAutomountFixture), 0644); err != nil {
		t.Fatal(err)
	}

	ok, err := repairGeneratedMountUnitFile(mountPath)
	if err != nil || !ok {
		t.Fatalf("mount repair: ok=%v err=%v", ok, err)
	}
	ok, err = repairGeneratedMountUnitFile(automountPath)
	if err != nil || !ok {
		t.Fatalf("automount repair: ok=%v err=%v", ok, err)
	}

	fixed, _ := os.ReadFile(mountPath)
	for _, bad := range []string{"After=local-fs.target", "DefaultDependencies=no", "x-systemd.automount", "x-systemd.idle-timeout"} {
		if strings.Contains(string(fixed), bad) {
			t.Errorf("repaired mount still contains %q:\n%s", bad, fixed)
		}
	}
	if !strings.Contains(string(fixed), "Options=defaults,noatime,nofail,x-systemd.device-timeout=10,x-systemd.default-timeout=10") {
		t.Errorf("repaired mount lost user options:\n%s", fixed)
	}

	fixedAuto, _ := os.ReadFile(automountPath)
	if strings.Contains(string(fixedAuto), "After=local-fs.target") || !strings.Contains(string(fixedAuto), "TimeoutIdleSec=60") {
		t.Errorf("repaired automount wrong:\n%s", fixedAuto)
	}

	// Idempotent: second run must not rewrite.
	ok, err = repairGeneratedMountUnitFile(mountPath)
	if err != nil || ok {
		t.Errorf("second run rewrote again: ok=%v err=%v", ok, err)
	}
}

func TestRepairSkipsForeignUnits(t *testing.T) {
	dir := t.TempDir()
	foreign := "[Mount]\nWhat=/dev/x\nWhere=/mnt/storage-sda\nType=ext4\nOptions=defaults\nAfter=local-fs.target\n"
	path := filepath.Join(dir, "mnt-storage\\x2dsda.mount")
	if err := os.WriteFile(path, []byte(foreign), 0644); err != nil {
		t.Fatal(err)
	}
	ok, err := repairGeneratedMountUnitFile(path)
	if err != nil || ok {
		t.Errorf("foreign unit should be untouched: ok=%v err=%v", ok, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != foreign {
		t.Error("foreign unit content changed")
	}
}

// CI gate that would have caught the issue #14 cycle pre-ship. The cycle
// only exists once local-fs.target pulls the automount in (WantedBy=), so
// verify must run against an enabled tree: the broken After=local-fs.target
// template fails here with "Transaction order is cyclic".
func TestSystemdAnalyzeVerifyRenderedUnits(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze not available")
	}
	localFsTarget := "/lib/systemd/system/local-fs.target"
	if _, err := os.Stat(localFsTarget); err != nil {
		localFsTarget = "/usr/lib/systemd/system/local-fs.target"
	}
	if _, err := os.Stat(localFsTarget); err != nil {
		t.Skip("local-fs.target not available on this system")
	}
	root := t.TempDir()
	// --root looks units up under the standard tree, so place them in
	// <root>/etc/systemd/system/ and simulate the enablement symlink
	// systemctl enable would create.
	unitDir := filepath.Join(root, "etc", "systemd", "system")
	if err := os.MkdirAll(filepath.Join(unitDir, "local-fs.target.wants"), 0755); err != nil {
		t.Fatal(err)
	}
	mountPath := filepath.Join(unitDir, "mnt-storage\\x2dsda.mount")
	automountPath := filepath.Join(unitDir, "mnt-storage\\x2dsda.automount")
	if err := os.WriteFile(mountPath, []byte(renderHostMountUnit("datapool", "/dev/disk/by-uuid/abc", "/mnt/storage-sda", "btrfs", "defaults,noatime,nofail")), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(automountPath, []byte(renderHostAutomountUnit("datapool", "/mnt/storage-sda")), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../mnt-storage\\x2dsda.automount", filepath.Join(unitDir, "local-fs.target.wants", "mnt-storage\\x2dsda.automount")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(localFsTarget); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(filepath.Join(unitDir, "local-fs.target"), data, 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("systemd-analyze", "verify", "--root", root,
		"local-fs.target", "mnt-storage\\x2dsda.automount", "mnt-storage\\x2dsda.mount").CombinedOutput()
	if err != nil {
		t.Errorf("systemd-analyze verify rejected generated units: %v\n%s", err, out)
	}
}
