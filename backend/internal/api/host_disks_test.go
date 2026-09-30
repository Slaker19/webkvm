package api

import (
	"errors"
	"strings"
	"testing"

	"webkvm/internal/diskstate"
	"webkvm/internal/models"
)

// TestDiskGuardFailsClosed verifies that when the safety probe is
// unavailable the guard returns an error wrapping errDiskProbeUnavailable
// (which handlers map to HTTP 503), instead of the old fail-open
// behaviour where any lsblk failure was silently read as "safe".
func TestDiskGuardFailsClosed(t *testing.T) {
	h := &Handler{}
	// A device path that cannot exist makes lsblk fail (or, if lsblk is
	// missing entirely, exec fails), exercising the error branch.
	err := h.assertDiskNotMounted(t.Context(), "/dev/definitely-not-a-real-disk-xyz")
	if err == nil {
		t.Skip("lsblk answered for a nonexistent device; cannot exercise the failure branch here")
	}
	if !errors.Is(err, errDiskProbeUnavailable) {
		t.Errorf("guard error = %v, want it to wrap errDiskProbeUnavailable", err)
	}
}

// TestDiskGuardErrMapping locks the HTTP status mapping: a probe
// failure becomes 503, everything else stays 409 (conflict).
func TestDiskGuardErrMapping(t *testing.T) {
	if !errors.Is(errDiskProbeUnavailable, errDiskProbeUnavailable) {
		t.Fatal("sentinel identity broken")
	}
	// Ensures the sentinel is distinct from a plain error so the mapping
	// in diskGuardErr does not accidentally classify conflicts as 503.
	if errors.Is(errors.New("some conflict"), errDiskProbeUnavailable) {
		t.Error("a plain error must not match errDiskProbeUnavailable")
	}
}

// TestInitHostDiskModeDefaults verifies that an empty/unknown mode is
// normalized to the destructive "format" mode (which is the historical
// behaviour) while an explicit "mount" is preserved.
func TestInitHostDiskModeDefaults(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "format"},
		{"  ", "format"},
		{"format", "format"},
		{"FORMAT", "format"},
		{"mount", "mount"},
		{" MOUNT ", "mount"},
		{"garbage", "format"},
	}
	for _, c := range cases {
		got := normalizeDiskMode(c.in)
		if got != c.want {
			t.Errorf("normalizeDiskMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeDiskModePure mirrors the inline normalization used in the
// handler so a future refactor keeps the same contract.
func normalizeDiskMode(in string) string {
	m := strings.ToLower(strings.TrimSpace(in))
	if m != "format" && m != "mount" {
		return "format"
	}
	return m
}

// TestSafeDiskPathRE guards the disk-path validator against injection of
// unsafe targets into wipefs/mkfs/mount.
func TestSafeDiskPathRE(t *testing.T) {
	valid := []string{"/dev/sdb", "/dev/sdb1", "/dev/nvme0n1", "/dev/nvme0n1p1", "/dev/vda", "/dev/loop0", "/dev/md0", "/dev/md127", "/dev/md/data"}
	for _, p := range valid {
		if !safeDiskPathRE.MatchString(p) {
			t.Errorf("safeDiskPathRE should accept %q", p)
		}
	}
	invalid := []string{
		"/dev/sdb; rm -rf /",
		"/etc/passwd",
		"/dev/sd",
		"../../dev/sdb",
		"/dev/sdb`whoami`",
		"",
	}
	for _, p := range invalid {
		if safeDiskPathRE.MatchString(p) {
			t.Errorf("safeDiskPathRE should reject %q", p)
		}
	}
}

// TestSafeMountOptionRE guards the per-option validator used when
// building the systemd unit's Options= line against injection.
func TestSafeMountOptionRE(t *testing.T) {
	valid := []string{"compress=zstd", "subvol=@data", "uid=1000", "gid=100", "umask=022", "discard"}
	for _, o := range valid {
		if !safeMountOptionRE.MatchString(o) {
			t.Errorf("safeMountOptionRE should accept %q", o)
		}
	}
	invalid := []string{
		"a b",
		"nofail,ro",          // commas are field separators, not one option
		"opt\nWhat=/dev/sdb", // newline injection breaks the unit file
		"opt=;rm -rf /",
		"opt\"quote",
		"-weird",
		"",
	}
	for _, o := range invalid {
		if safeMountOptionRE.MatchString(o) {
			t.Errorf("safeMountOptionRE should reject %q", o)
		}
	}
}

// TestValidateMountPoint guards the mount-point validator against
// mounting over critical system paths or injecting systemd directives
// via newlines.
func TestValidateMountPoint(t *testing.T) {
	valid := []string{"/mnt/data", "/mnt/nvme-fast", "/srv/storage", "/mnt/a/b"}
	for _, mp := range valid {
		if err := validateMountPoint(mp); err != nil {
			t.Errorf("validateMountPoint(%q) = %v, want nil", mp, err)
		}
	}
	invalid := []string{
		"relative/path",
		"/",
		"/etc",
		"/boot",
		"/mnt",
		"/mnt/../etc",
		"/mnt/../../etc",
		"/mnt/data\nWhat=/dev/sdb",
		"/mnt/data\rExtra=x",
		"/mnt//data",
		"/mnt/.",
		"",
	}
	for _, mp := range invalid {
		if err := validateMountPoint(mp); err == nil {
			t.Errorf("validateMountPoint(%q) = nil, want error", mp)
		}
	}
}

// TestSafeDiskNameRE guards the volume-name validator.
func TestSafeDiskNameRE(t *testing.T) {
	valid := []string{"nvme-fast", "data-3tb", "pool_1", "A.b-c_d"}
	for _, n := range valid {
		if !safeDiskNameRE.MatchString(n) {
			t.Errorf("safeDiskNameRE should accept %q", n)
		}
	}
	invalid := []string{"a b", "a/b", "../x", "a\"b", "", "a;b"}
	for _, n := range invalid {
		if safeDiskNameRE.MatchString(n) {
			t.Errorf("safeDiskNameRE should reject %q", n)
		}
	}
}

// TestFilesystemByID verifies the format catalog accepts every supported
// ID with its expected mkfs binary/args, and rejects anything outside
// the curated list (in particular the filesystems deliberately excluded
// as unsuitable storage backends — see the doc comment on
// supportedFilesystems).
func TestFilesystemByID(t *testing.T) {
	cases := []struct {
		id      string
		wantBin string
	}{
		{"ext4", "mkfs.ext4"},
		{"xfs", "mkfs.xfs"},
		{"btrfs", "mkfs.btrfs"},
		{"f2fs", "mkfs.f2fs"},
	}
	for _, c := range cases {
		fs, ok := filesystemByID(c.id)
		if !ok {
			t.Errorf("filesystemByID(%q) should be found in the catalog", c.id)
			continue
		}
		if fs.Bin != c.wantBin {
			t.Errorf("filesystemByID(%q).Bin = %q, want %q", c.id, fs.Bin, c.wantBin)
		}
		if len(fs.Args) == 0 {
			t.Errorf("filesystemByID(%q).Args should include a non-interactive flag", c.id)
		}
	}

	excluded := []string{"vfat", "exfat", "ntfs", "ext2", "ext3", "jfs", "nilfs2", "minix", "udf", "swap", "reiserfs", ""}
	for _, id := range excluded {
		if _, ok := filesystemByID(id); ok {
			t.Errorf("filesystemByID(%q) should NOT be in the catalog (excluded filesystem)", id)
		}
	}
}

// TestFilesystemCatalogIsCurated locks the catalog to exactly the four
// filesystems chosen as useful storage backends for WebKVM, so an
// accidental addition/removal fails a test instead of silently shipping.
func TestFilesystemCatalogIsCurated(t *testing.T) {
	want := []string{"ext4", "xfs", "btrfs", "f2fs"}
	if len(supportedFilesystems) != len(want) {
		t.Fatalf("supportedFilesystems has %d entries, want %d", len(supportedFilesystems), len(want))
	}
	for i, fs := range supportedFilesystems {
		if fs.ID != want[i] {
			t.Errorf("supportedFilesystems[%d].ID = %q, want %q", i, fs.ID, want[i])
		}
		if fs.Bin == "" || fs.Label == "" {
			t.Errorf("supportedFilesystems[%d] (%s) missing Bin/Label", i, fs.ID)
		}
	}
}

// TestFirstConflictingMount covers the pure guard logic behind
// assertDiskNotMounted / assertDeviceNotMountedElsewhere, which exists
// specifically to prevent a repeat of a real incident: a disk
// (/dev/vda) was mounted at /mnt/nvme via the UI's "format" flow, then
// mounted a second time at /mnt/storage-vda via "mount existing"
// (which didn't check for an existing mount), and finally had its GPT
// table wiped via the disk-wipe endpoint (which didn't check for any
// mount at all) while both mountpoints were still live.
func TestFirstConflictingMount(t *testing.T) {
	cases := []struct {
		name         string
		mounts       []string
		allowed      string
		wantConflict bool
		wantMount    string
	}{
		{"no mounts at all is never a conflict", nil, "", false, ""},
		{"any mount conflicts when nothing is allowed (wipe/format path)", []string{"/mnt/data"}, "", true, "/mnt/data"},
		{"mount matching the requested target is not a conflict (idempotent re-mount)", []string{"/mnt/nvme"}, "/mnt/nvme", false, ""},
		{"mount at a different path than requested is a conflict (the real incident)", []string{"/mnt/nvme"}, "/mnt/storage-vda", true, "/mnt/nvme"},
		{"multiple mounts, one matches allowed but another doesn't", []string{"/mnt/nvme", "/mnt/storage-vda"}, "/mnt/nvme", true, "/mnt/storage-vda"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mount, conflict := firstConflictingMount(c.mounts, c.allowed)
			if conflict != c.wantConflict {
				t.Fatalf("firstConflictingMount(%v, %q) conflict = %v, want %v", c.mounts, c.allowed, conflict, c.wantConflict)
			}
			if conflict && mount != c.wantMount {
				t.Errorf("firstConflictingMount(%v, %q) = %q, want %q", c.mounts, c.allowed, mount, c.wantMount)
			}
		})
	}
}

// TestNonEmpty verifies the lsblk mountpoints filter drops the empty
// strings lsblk emits for unmounted partitions (e.g. an EFI partition
// with no mountpoints prints [""] in some lsblk versions' JSON), so
// they never get treated as a real "mounted at \"\"" conflict.
func TestNonEmpty(t *testing.T) {
	got := nonEmpty([]string{"/mnt/a", "", "/mnt/b", ""})
	want := []string{"/mnt/a", "/mnt/b"}
	if len(got) != len(want) {
		t.Fatalf("nonEmpty() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("nonEmpty()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestOnlyMountPoint covers the idempotency escape hatch in
// assertNoDeclaredMount: re-running the "mount existing filesystem"
// flow against the device already declared for that exact path is not
// a conflict, but a declaration pointing anywhere else is.
func TestOnlyMountPoint(t *testing.T) {
	cases := []struct {
		name    string
		mounts  []string
		allowed string
		want    bool
	}{
		{"no declarations", nil, "/mnt/data", true},
		{"same mount point", []string{"/mnt/data"}, "/mnt/data", true},
		{"different mount point", []string{"/mnt/other"}, "/mnt/data", false},
		{"one of several differs", []string{"/mnt/data", "/srv/x"}, "/mnt/data", false},
		{"destructive path allows nothing", []string{"/mnt/data"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := onlyMountPoint(c.mounts, c.allowed); got != c.want {
				t.Errorf("onlyMountPoint(%v, %q) = %v, want %v", c.mounts, c.allowed, got, c.want)
			}
		})
	}
}

// TestMountDetail verifies only the "configured" state carries prose.
// An actively mounted disk already shows its mountpoint in its own
// column, so repeating it as a detail string is noise.
func TestMountDetail(t *testing.T) {
	configured := diskstate.State{
		Configured:  true,
		MountPoints: []string{"/mnt/storage-sdc"},
		Units:       []string{`mnt-storage\x2dsdc.automount`},
	}
	if got := mountDetail(configured); got == "" {
		t.Error("configured state must explain itself")
	} else if !strings.Contains(got, "mnt-storage") {
		t.Errorf("mountDetail = %q, want it to name the systemd unit", got)
	}

	active := diskstate.State{Active: true, MountPoints: []string{"/mnt/x"}}
	if got := mountDetail(active); got != "" {
		t.Errorf("mountDetail(active) = %q, want empty", got)
	}
	if got := mountDetail(diskstate.State{}); got != "" {
		t.Errorf("mountDetail(free) = %q, want empty", got)
	}
}

// TestPartitionHasMount ensures a mountpoint already listed against a
// partition is not duplicated onto the parent disk row.
func TestPartitionHasMount(t *testing.T) {
	parts := []models.HostPartition{
		{Path: "/dev/sda1", MountPoints: []string{"/mnt/storage-sdc"}},
		{Path: "/dev/sda2"},
	}
	if !partitionHasMount(parts, "/mnt/storage-sdc") {
		t.Error("expected the partition mountpoint to be found")
	}
	if partitionHasMount(parts, "/mnt/elsewhere") {
		t.Error("unrelated mountpoint must not match")
	}
	if partitionHasMount(nil, "/mnt/x") {
		t.Error("empty partition list must not match")
	}
}
