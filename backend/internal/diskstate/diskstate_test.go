package diskstate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeFixture creates a temp file with the given content and returns
// its path.
func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return p
}

// realHostMountInfo mirrors the shape of /proc/self/mountinfo on the
// machine this bug was found on: root on sdc2, an active f2fs mount on
// nvme0n1p1, and nothing at all for sda1/sdb1.
const realHostMountInfo = `25 30 0:23 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw
30 1 8:34 / / rw,relatime shared:1 - xfs /dev/sdc2 rw,attr2
36 30 8:33 / /boot/efi rw,relatime shared:8 - vfat /dev/sdc1 rw,fmask=0077
95 30 259:1 / /mnt/storage-nvme0n1 rw,relatime shared:44 - f2fs /dev/nvme0n1p1 rw,lazytime
99 30 8:34 / /var/lib/incus/storage-pools/webkvm-incus rw,relatime shared:1 - xfs /dev/sdc2 rw,attr2
`

func TestResolve_ActiveMountsFromMountInfo(t *testing.T) {
	r := &Resolver{
		MountInfoPath: writeFixture(t, "mountinfo", realHostMountInfo),
		FstabPath:     writeFixture(t, "fstab", ""),
		Systemd:       func(context.Context) ([]SystemdUnit, error) { return nil, nil },
		Libvirt:       func(context.Context) (map[string]string, error) { return nil, nil },
	}
	table := r.Resolve(context.Background())

	s, ok := table.Lookup("/dev/nvme0n1p1")
	if !ok {
		t.Fatal("expected /dev/nvme0n1p1 in table")
	}
	if !s.Active {
		t.Error("nvme0n1p1 is mounted; Active should be true")
	}
	if s.Status() != StatusActive {
		t.Errorf("Status = %q, want %q", s.Status(), StatusActive)
	}
	if !contains(s.MountPoints, "/mnt/storage-nvme0n1") {
		t.Errorf("MountPoints = %v, want to include /mnt/storage-nvme0n1", s.MountPoints)
	}

	// The root device must be flagged as a system mount.
	root, ok := table.Lookup("/dev/sdc2")
	if !ok {
		t.Fatal("expected /dev/sdc2 in table")
	}
	if !root.HasSystemMount() {
		t.Error("sdc2 holds / and an incus pool; HasSystemMount should be true")
	}
}

// This is the data-loss bug: an armed automount whose .mount unit is
// dead. The kernel reports nothing, so the old lsblk-only check called
// the disk free and allowed a format.
func TestResolve_ArmedAutomountWithNoActiveMount(t *testing.T) {
	r := &Resolver{
		MountInfoPath: writeFixture(t, "mountinfo", realHostMountInfo),
		FstabPath:     writeFixture(t, "fstab", ""),
		Systemd: func(context.Context) ([]SystemdUnit, error) {
			return []SystemdUnit{{
				Name:   `mnt-storage\x2dsdc.automount`,
				What:   "/dev/sda1",
				Where:  "/mnt/storage-sdc",
				Active: true,
			}}, nil
		},
		Libvirt: func(context.Context) (map[string]string, error) { return nil, nil },
	}
	table := r.Resolve(context.Background())

	s, ok := table.Lookup("/dev/sda1")
	if !ok {
		t.Fatal("armed automount device missing from table")
	}
	if s.Active {
		t.Error("Active should be false: the kernel has nothing mounted")
	}
	if !s.Configured {
		t.Fatal("Configured must be true: an armed automount will remount on access")
	}
	if !s.InUse() {
		t.Fatal("InUse must be true; this is the check that prevents the wipe")
	}
	if s.Status() != StatusConfigured {
		t.Errorf("Status = %q, want %q", s.Status(), StatusConfigured)
	}
	if !contains(s.Units, `mnt-storage\x2dsdc.automount`) {
		t.Errorf("Units = %v, want the automount unit name", s.Units)
	}
	if !s.hasSource(SourceAutomount) {
		t.Errorf("Sources = %v, want to include %q", s.Sources, SourceAutomount)
	}
	// The operator must be told which unit to disable.
	if reason := s.Reason(); !containsSubstr(reason, "mnt-storage") || !containsSubstr(reason, "disable") {
		t.Errorf("Reason = %q, want it to name the unit and say to disable it", reason)
	}
}

func TestResolve_WholeDiskAggregatesPartitionIntent(t *testing.T) {
	r := &Resolver{
		MountInfoPath: writeFixture(t, "mountinfo", realHostMountInfo),
		FstabPath:     writeFixture(t, "fstab", ""),
		Systemd: func(context.Context) ([]SystemdUnit, error) {
			return []SystemdUnit{{
				Name:   `mnt-storage\x2dsdc.automount`,
				What:   "/dev/sda1",
				Where:  "/mnt/storage-sdc",
				Active: true,
			}}, nil
		},
		Libvirt: func(context.Context) (map[string]string, error) { return nil, nil },
	}
	table := r.Resolve(context.Background())

	// Wiping /dev/sda destroys /dev/sda1, so the parent must inherit
	// the partition's declaration.
	disk := table.ForDisk("/dev/sda", []string{"/dev/sda1"})
	if !disk.InUse() {
		t.Fatal("whole disk must be in use when a partition has mount intent")
	}
	if disk.Status() != StatusConfigured {
		t.Errorf("Status = %q, want %q", disk.Status(), StatusConfigured)
	}

	// A disk with genuinely nothing pointing at it stays free.
	free := table.ForDisk("/dev/sdb", []string{"/dev/sdb1"})
	if free.InUse() {
		t.Errorf("sdb has no declaration anywhere; got %+v", free)
	}
	if free.Status() != StatusFree {
		t.Errorf("Status = %q, want %q", free.Status(), StatusFree)
	}
}

func TestResolve_FstabNoautoEntryCountsAsConfigured(t *testing.T) {
	fstab := `# /etc/fstab
UUID=9f6d9df6-983a-41cd-badd-e71b189d32b8 /srv/archive xfs noauto,nofail 0 2
/dev/sdd1  /mnt/with\040space  ext4  defaults  0 2
UUID=aaaa none swap sw 0 0
tmpfs /tmp tmpfs defaults 0 0
`
	devDisk := t.TempDir()
	if err := os.MkdirAll(filepath.Join(devDisk, "by-uuid"), 0755); err != nil {
		t.Fatal(err)
	}
	// Stand in for /dev/disk/by-uuid/<uuid> -> /dev/sdb1.
	uuidPath := filepath.Join(devDisk, "by-uuid", "9f6d9df6-983a-41cd-badd-e71b189d32b8")
	if err := os.WriteFile(uuidPath, nil, 0644); err != nil {
		t.Fatal(err)
	}

	r := &Resolver{
		MountInfoPath: writeFixture(t, "mountinfo", ""),
		FstabPath:     writeFixture(t, "fstab", fstab),
		DevDiskDir:    devDisk,
		Systemd:       func(context.Context) ([]SystemdUnit, error) { return nil, nil },
		Libvirt:       func(context.Context) (map[string]string, error) { return nil, nil },
	}
	table := r.Resolve(context.Background())

	s, ok := table.Lookup(uuidPath)
	if !ok {
		t.Fatalf("fstab UUID entry not resolved; table = %+v", table)
	}
	if !s.Configured || s.Active {
		t.Errorf("noauto entry should be Configured and not Active, got %+v", s)
	}

	// Escaped spaces must be decoded, not left as \040.
	spaced, ok := table.Lookup("/dev/sdd1")
	if !ok {
		t.Fatal("/dev/sdd1 missing from table")
	}
	if !contains(spaced.MountPoints, "/mnt/with space") {
		t.Errorf("MountPoints = %v, want the octal escape decoded", spaced.MountPoints)
	}

	// swap and tmpfs have no backing block device worth tracking.
	if _, ok := table.Lookup("/tmp"); ok {
		t.Error("tmpfs must not appear as a block device")
	}
}

func TestResolve_LibvirtPoolOnMountedDisk(t *testing.T) {
	r := &Resolver{
		MountInfoPath: writeFixture(t, "mountinfo", realHostMountInfo),
		FstabPath:     writeFixture(t, "fstab", ""),
		Systemd:       func(context.Context) ([]SystemdUnit, error) { return nil, nil },
		Libvirt: func(context.Context) (map[string]string, error) {
			return map[string]string{
				"Lexar-Discos": "/mnt/storage-nvme0n1/disks",
			}, nil
		},
	}
	table := r.Resolve(context.Background())

	s, _ := table.Lookup("/dev/nvme0n1p1")
	if !contains(s.Pools, "Lexar-Discos") {
		t.Errorf("Pools = %v, want the pool attributed to the backing device", s.Pools)
	}
	if !s.hasSource(SourceLibvirt) {
		t.Errorf("Sources = %v, want %q", s.Sources, SourceLibvirt)
	}
}

func TestDeviceProviding_PrefersLongestMatchAndIgnoresRoot(t *testing.T) {
	table := Table{}
	table.add("/dev/sdc2", "/", SourceKernel, "", "")
	table.add("/dev/nvme0n1p1", "/mnt/storage", SourceKernel, "", "")
	table.add("/dev/sdb1", "/mnt/storage/deep", SourceKernel, "", "")

	if got := table.deviceProviding("/mnt/storage/deep/pool"); got != "/dev/sdb1" {
		t.Errorf("deviceProviding = %q, want /dev/sdb1 (longest prefix)", got)
	}
	// "/" backs every path; attributing a pool to the root device would
	// make every pool look like it lives on the OS disk.
	if got := table.deviceProviding("/srv/elsewhere"); got != "" {
		t.Errorf("deviceProviding = %q, want empty (root must not match)", got)
	}
}

func TestResolveWhat_TagForms(t *testing.T) {
	devDisk := t.TempDir()
	for _, sub := range []string{"by-uuid", "by-label", "by-partuuid"} {
		if err := os.MkdirAll(filepath.Join(devDisk, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"by-uuid/abc", "by-label/data", "by-partuuid/xyz"} {
		if err := os.WriteFile(filepath.Join(devDisk, f), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	r := &Resolver{DevDiskDir: devDisk}

	cases := map[string]string{
		"UUID=abc":     filepath.Join(devDisk, "by-uuid", "abc"),
		"LABEL=data":   filepath.Join(devDisk, "by-label", "data"),
		"PARTUUID=xyz": filepath.Join(devDisk, "by-partuuid", "xyz"),
		"/dev/sda1":    "/dev/sda1",
		`"/dev/sdb1"`:  "/dev/sdb1",
		"BOGUS=x":      "",
		"":             "",
	}
	for in, want := range cases {
		if got := r.resolveWhat(in); got != want {
			t.Errorf("resolveWhat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnescapeOctal(t *testing.T) {
	cases := map[string]string{
		`/mnt/with\040space`: "/mnt/with space",
		`/mnt/tab\011here`:   "/mnt/tab\there",
		`/plain/path`:        "/plain/path",
		`/trailing\`:         `/trailing\`,
		`/bad\999escape`:     `/bad\999escape`,
	}
	for in, want := range cases {
		if got := unescapeOctal(in); got != want {
			t.Errorf("unescapeOctal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsSystemMount(t *testing.T) {
	system := []string{"/", "/boot", "/boot/efi", "/etc", "/usr", "/var",
		"/var/lib/incus/storage-pools/webkvm-incus", "/opt/webkvm/pools/ISOS"}
	for _, mp := range system {
		if !IsSystemMount(mp) {
			t.Errorf("IsSystemMount(%q) = false, want true", mp)
		}
	}
	for _, mp := range []string{"/mnt/storage-sdc", "/srv/data", "/mnt/xd"} {
		if IsSystemMount(mp) {
			t.Errorf("IsSystemMount(%q) = true, want false", mp)
		}
	}
}

func TestResolve_MissingSourcesDegradeGracefully(t *testing.T) {
	// No mountinfo, no fstab, no systemd, no libvirt: the resolver must
	// return an empty table rather than panicking, so a container
	// without /proc still serves the API.
	r := &Resolver{
		MountInfoPath: "/nonexistent/mountinfo",
		FstabPath:     "/nonexistent/fstab",
		Systemd:       func(context.Context) ([]SystemdUnit, error) { return nil, os.ErrNotExist },
		Libvirt:       func(context.Context) (map[string]string, error) { return nil, os.ErrNotExist },
	}
	if table := r.Resolve(context.Background()); len(table) != 0 {
		t.Errorf("expected empty table, got %+v", table)
	}
}

func containsSubstr(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}
