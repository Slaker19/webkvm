package ha

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"webkvm/internal/sysutil"
)

const relUUID = "0d4e7c1a-2b3c-4d5e-8f90-112233445566"

func relXML(name, uuid string) string {
	return fmt.Sprintf(`<domain type='kvm'><name>%s</name><uuid>%s</uuid><memory unit='KiB'>131072</memory>
<devices><disk type='file' device='disk'><source file='/var/lib/libvirt/zz/disk.qcow2'/></disk></devices></domain>`, name, uuid)
}

func relEntry() ReleasedVM {
	x := relXML("web", relUUID)
	return ReleasedVM{
		Name: "web", UUID: relUUID, Status: ReleaseStatusReleased, Mode: ModeStop,
		Stack: KVMStack{DRBDResource: "zz_web", Device: "/dev/drbd/by-res/zz_web/0", MountDir: "/var/lib/libvirt/zz",
			FSType: "ext4", ConfigPath: "/var/lib/libvirt/zz/web.xml"},
		XML: x, XMLSource: ReleaseSourceConfigFile, XMLSHA256: XMLDigest(x),
	}
}

// restoreFake simulates one node: DRBD, the mount table and libvirt.
type restoreFake struct {
	mu                                 sync.Mutex
	up, primary, mounted, defined, run bool
	uptodate                           bool
	peerPrimary                        bool
	peersDown                          bool
	definedUUID                        string
	definedXML                         string
	fail                               map[string]bool // command prefix -> fail
	cmds                               []string
	mountSource                        string
	libvirtDown                        bool
	// quorum adds quorum:<v> to the local volume line ("" = not reported).
	quorum string
	// status overrides the whole `drbdsetup status` output when up.
	status string
	// hiddenMount: the mount happened in a private namespace, so the host
	// findmnt never shows it.
	hiddenMount bool
	// files models the host filesystem: path -> "file" or "link:<target>".
	files map[string]string
	// host marks the commands that ran through systemd-run (host mount ns).
	host map[string]bool
}

const relDefLink = "/etc/libvirt/qemu/web.xml"

func newRestoreFake() *restoreFake {
	return &restoreFake{uptodate: true, fail: map[string]bool{}, files: map[string]string{}, host: map[string]bool{}}
}

// hostRunner wraps the fake like production does (systemd-run available).
func (f *restoreFake) hostRunner() sysutil.CommandRunner {
	return sysutil.HostNamespaceRunner{Inner: f, Available: func() bool { return true }}
}

func (f *restoreFake) failing(line string) bool {
	for p := range f.fail {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

func (f *restoreFake) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	viaHost := false
	if name == "systemd-run" {
		for i, a := range args {
			if a == "--" {
				name, args, viaHost = args[i+1], args[i+2:], true
				break
			}
		}
	}
	line := name + " " + strings.Join(args, " ")
	line = strings.Replace(line, "virsh -c qemu:///system ", "virsh ", 1)
	f.cmds = append(f.cmds, line)
	if viaHost {
		f.host[line] = true
	}
	if f.failing(line) {
		return []byte("simulated failure"), errors.New("exit status 1")
	}
	switch {
	case strings.HasPrefix(line, "virsh domuuid"):
		if f.libvirtDown {
			return []byte("error: failed to connect to the hypervisor"), errors.New("exit status 1")
		}
		if !f.defined {
			return []byte("error: failed to get domain 'web'"), errors.New("exit status 1")
		}
		return []byte(f.definedUUID + "\n"), nil
	case strings.HasPrefix(line, "drbdsetup status"):
		if !f.up {
			return []byte("zz_web: No such resource"), errors.New("exit status 10")
		}
		role, disk := "Secondary", "Outdated"
		if f.primary {
			role = "Primary"
		}
		if f.uptodate {
			disk = "UpToDate"
		}
		peerRole, conn := "Secondary", "Connected"
		if f.peerPrimary {
			peerRole = "Primary"
		}
		if f.peersDown {
			conn, peerRole = "Connecting", "Unknown"
		}
		if f.status != "" {
			return []byte(strings.ReplaceAll(f.status, "ROLE", role)), nil
		}
		q := ""
		if f.quorum != "" {
			q = " quorum:" + f.quorum
		}
		return []byte(fmt.Sprintf("zz_web node-id:0 role:%s suspended:no\n  volume:0 minor:1001 disk:%s open:no%s\n  ha2 node-id:1 connection:%s role:%s\n    volume:0 replication:Established peer-disk:UpToDate\n", role, disk, q, conn, peerRole)), nil
	case strings.HasPrefix(line, "drbdadm up"):
		f.up = true
	case strings.HasPrefix(line, "drbdadm primary"):
		f.primary = true
	case strings.HasPrefix(line, "drbdadm secondary"):
		f.primary = false
	case strings.HasPrefix(line, "drbdadm down"):
		f.up = false
	case strings.HasPrefix(line, "findmnt"):
		if !f.mounted || f.hiddenMount {
			return nil, errors.New("exit status 1")
		}
		src := f.mountSource
		if src == "" {
			src = "/dev/drbd/by-res/zz_web/0"
		}
		return []byte(src + "\n"), nil
	case strings.HasPrefix(line, "mount "):
		f.mounted = true
	case strings.HasPrefix(line, "umount"):
		f.mounted = false
	case strings.HasPrefix(line, "virsh define"):
		data, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		f.defined, f.definedXML = true, string(data)
		f.definedUUID = relUUID
		f.files[relDefLink] = "file"
	case strings.HasPrefix(line, "virsh start"):
		f.run = true
	case strings.HasPrefix(line, "virsh undefine"):
		f.defined, f.definedXML = false, ""
		delete(f.files, relDefLink)
	case name == "stat":
		p := args[len(args)-1]
		if _, ok := f.files[p]; !ok {
			return []byte("stat: cannot statx '" + p + "': No such file or directory"), errors.New("exit status 1")
		}
		return []byte("regular file\n"), nil
	case name == "readlink":
		v := f.files[args[len(args)-1]]
		if !strings.HasPrefix(v, "link:") {
			return nil, errors.New("exit status 1")
		}
		return []byte(strings.TrimPrefix(v, "link:") + "\n"), nil
	case name == "install":
		f.files[args[len(args)-1]] = "file"
	case name == "mv":
		f.files[args[len(args)-1]] = f.files[args[len(args)-2]]
		delete(f.files, args[len(args)-2])
	case name == "ln":
		f.files[args[len(args)-1]] = "link:" + args[len(args)-2]
	case name == "rm":
		delete(f.files, args[len(args)-1])
	}
	return nil, nil
}

func (f *restoreFake) RunWithTimeout(n string, _ time.Duration, a ...string) ([]byte, error) {
	return f.Run(context.Background(), n, a...)
}

func (f *restoreFake) has(prefix string) bool {
	for _, c := range f.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *restoreFake) writes() []string {
	var out []string
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "virsh domuuid") || strings.HasPrefix(c, "drbdsetup status") || strings.HasPrefix(c, "findmnt") ||
			strings.HasPrefix(c, "stat ") || strings.HasPrefix(c, "readlink ") {
			continue
		}
		out = append(out, c)
	}
	return out
}

func restoreOpts(t *testing.T, start bool) RestoreOptions {
	return RestoreOptions{Start: start, TmpDir: t.TempDir(), ConnectWait: 30 * time.Millisecond, Poll: 5 * time.Millisecond}
}

// hostOpts are restoreOpts with the production host-namespace wrapping.
func hostOpts(t *testing.T, f *restoreFake, start bool) RestoreOptions {
	o := restoreOpts(t, start)
	o.HostRunner = f.hostRunner()
	return o
}

func TestRestoreReleasedSuccessOrder(t *testing.T) {
	f := newRestoreFake()
	res, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, true))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !res.Changed || res.State != "running" || res.UUID != relUUID || res.XMLSource != ReleaseSourceSaved || !res.XMLLinked {
		t.Fatalf("result %+v", res)
	}
	want := []string{"drbdadm up zz_web", "drbdadm primary zz_web", "mkdir -p /var/lib/libvirt/zz",
		"mount -t ext4 /dev/drbd/by-res/zz_web/0 /var/lib/libvirt/zz", "virsh define",
		"install -m 0600 -T ", "sync -- /var/lib/libvirt/zz/web.xml.webkvm-new",
		"mv -fT /var/lib/libvirt/zz/web.xml.webkvm-new /var/lib/libvirt/zz/web.xml",
		"ln -sfnT /var/lib/libvirt/zz/web.xml /etc/libvirt/qemu/web.xml", "virsh start web"}
	got := f.writes()
	if len(got) != len(want) {
		t.Fatalf("writes %v", got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("step %d: got %q want %q (%v)", i, got[i], want[i], got)
		}
	}
	if !f.up || !f.primary || !f.mounted || !f.defined || !f.run || !strings.Contains(f.definedXML, "<uuid>"+relUUID) {
		t.Fatalf("state %+v", f)
	}
	if f.files["/var/lib/libvirt/zz/web.xml"] != "file" || f.files[relDefLink] != "link:/var/lib/libvirt/zz/web.xml" {
		t.Fatalf("definition not linked: %v", f.files)
	}
	// the transient XML file is removed
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(strings.Fields(got[4])[2]), ".restore-*")); len(left) != 0 {
		t.Fatalf("temporary XML left behind: %v", left)
	}
}

func TestRestoreReleasedHostVisibleStepsRunInHostNamespace(t *testing.T) {
	// 8.21 defect 1: mount/umount/findmnt and the definition file/link must
	// run in the host mount namespace; libvirt and DRBD calls need not.
	f := newRestoreFake()
	if _, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false)); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.cmds {
		hostOnly := false
		for _, p := range []string{"findmnt", "mkdir", "mount ", "umount", "stat ", "install ", "sync ", "mv ", "ln ", "readlink ", "rm "} {
			if strings.HasPrefix(c, p) {
				hostOnly = true
			}
		}
		if hostOnly && !f.host[c] {
			t.Errorf("%q ran inside the service namespace", c)
		}
		if (strings.HasPrefix(c, "virsh") || strings.HasPrefix(c, "drbd")) && f.host[c] {
			t.Errorf("%q needlessly wrapped", c)
		}
	}
	// Rollback goes through the same path.
	f2 := newRestoreFake()
	f2.fail["virsh define"] = true
	if _, err := RestoreReleased(context.Background(), f2, relEntry(), hostOpts(t, f2, false)); err == nil {
		t.Fatal("define failure ignored")
	}
	if !f2.host["umount /var/lib/libvirt/zz"] || f2.mounted {
		t.Fatalf("rollback umount not in the host namespace: %v", f2.cmds)
	}
}

func TestRestoreReleasedMountNotVisibleToHostRollsBack(t *testing.T) {
	f := newRestoreFake()
	f.hiddenMount = true
	res, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, true))
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeMountNotVisible || res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	if f.mounted || f.primary || f.up || f.defined || f.has("virsh define") {
		t.Fatalf("not rolled back: %v", f.cmds)
	}
}

func TestRestoreReleasedIdempotent(t *testing.T) {
	f := newRestoreFake()
	if _, err := RestoreReleased(context.Background(), f, relEntry(), restoreOpts(t, false)); err != nil {
		t.Fatal(err)
	}
	f.cmds = nil
	res, err := RestoreReleased(context.Background(), f, relEntry(), restoreOpts(t, false))
	if err != nil || res.Changed || res.State != "defined" {
		t.Fatalf("second restore: %+v %v", res, err)
	}
	if w := f.writes(); len(w) != 0 {
		t.Fatalf("idempotent restore must not write: %v", w)
	}
}

func TestRestoreReleasedRollbackEachStep(t *testing.T) {
	for _, tc := range []struct {
		fail, code string
	}{
		{"drbdadm up", CodeDRBDUpFailed},
		{"drbdadm primary", CodeDRBDPromoteFailed},
		{"mkdir", CodeMountFailed},
		{"mount -t", CodeMountFailed},
		{"virsh define", CodeDefineFailed},
		{"virsh start", CodeStartFailed},
	} {
		t.Run(tc.fail, func(t *testing.T) {
			f := newRestoreFake()
			f.fail[tc.fail] = true
			res, err := RestoreReleased(context.Background(), f, relEntry(), restoreOpts(t, true))
			var oe *OpError
			if !errors.As(err, &oe) || oe.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
			if res.Changed {
				t.Fatalf("failed restore reports changed: %+v", res)
			}
			if f.up || f.primary || f.mounted || f.defined || f.run {
				t.Fatalf("not rolled back: up=%v primary=%v mounted=%v defined=%v run=%v cmds=%v", f.up, f.primary, f.mounted, f.defined, f.run, f.cmds)
			}
			for _, c := range f.cmds {
				if strings.Contains(c, "--remove-all-storage") || strings.Contains(c, "mkfs") || strings.Contains(c, "wipe") || strings.Contains(c, "--force") {
					t.Fatalf("destructive command %q", c)
				}
			}
		})
	}
}

func TestRestoreReleasedKeepsPreexistingState(t *testing.T) {
	// DRBD already up and Primary, FS already mounted: a define failure must
	// not demote, unmount or down what this call did not do.
	f := newRestoreFake()
	f.up, f.primary, f.mounted = true, true, true
	f.fail["virsh define"] = true
	_, err := RestoreReleased(context.Background(), f, relEntry(), restoreOpts(t, false))
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeDefineFailed {
		t.Fatalf("%v", err)
	}
	if !f.up || !f.primary || !f.mounted || f.has("umount") || f.has("drbdadm secondary") || f.has("drbdadm down") {
		t.Fatalf("pre-existing state touched: %v", f.cmds)
	}
}

func TestRestoreReleasedRefusals(t *testing.T) {
	t.Run("peer primary", func(t *testing.T) {
		f := newRestoreFake()
		f.peerPrimary = true
		_, err := RestoreReleased(context.Background(), f, relEntry(), restoreOpts(t, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeDRBDPrimaryElsewhere || f.up || f.has("drbdadm primary") || f.has("mount -t") {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
	t.Run("not uptodate", func(t *testing.T) {
		f := newRestoreFake()
		f.uptodate = false
		_, err := RestoreReleased(context.Background(), f, relEntry(), restoreOpts(t, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeDRBDNotUpToDate || f.up || f.has("drbdadm primary") {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
	t.Run("domain conflict", func(t *testing.T) {
		f := newRestoreFake()
		f.defined, f.definedUUID = true, "99999999-2b3c-4d5e-8f90-112233445566"
		_, err := RestoreReleased(context.Background(), f, relEntry(), restoreOpts(t, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeDomainConflict || len(f.writes()) != 0 {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
	t.Run("libvirt unreachable", func(t *testing.T) {
		f := newRestoreFake()
		f.libvirtDown = true
		_, err := RestoreReleased(context.Background(), f, relEntry(), restoreOpts(t, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeDefineFailed || len(f.writes()) != 0 {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
	t.Run("mount conflict", func(t *testing.T) {
		f := newRestoreFake()
		f.mounted, f.mountSource = true, "/dev/sdz1"
		_, err := RestoreReleased(context.Background(), f, relEntry(), restoreOpts(t, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeMountConflict || len(f.writes()) != 0 {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
	for name, mutate := range map[string]func(*ReleasedVM){
		"name mismatch":  func(e *ReleasedVM) { e.XML = relXML("other", relUUID) },
		"uuid mismatch":  func(e *ReleasedVM) { e.UUID = "11111111-2b3c-4d5e-8f90-112233445566" },
		"not a domain":   func(e *ReleasedVM) { e.XML = "<network><name>web</name></network>" },
		"bad mount path": func(e *ReleasedVM) { e.Stack.MountDir = "/var/lib/../../etc" },
		"bad device":     func(e *ReleasedVM) { e.Stack.Device = "/dev/sda;rm" },
		"bad drbd name":  func(e *ReleasedVM) { e.Stack.DRBDResource = "../x" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newRestoreFake()
			e := relEntry()
			mutate(&e)
			_, err := RestoreReleased(context.Background(), f, e, restoreOpts(t, false))
			var oe *OpError
			if !errors.As(err, &oe) || oe.Code != CodeReleaseXMLInvalid || len(f.cmds) != 0 {
				t.Fatalf("%v %v", err, f.cmds)
			}
		})
	}
	t.Run("unrestorable", func(t *testing.T) {
		f := newRestoreFake()
		e := relEntry()
		e.XML = ""
		_, err := RestoreReleased(context.Background(), f, e, restoreOpts(t, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeReleaseUnrestorable || len(f.cmds) != 0 {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
}

func TestRestoreReleasedRollbackFailureReportsLeftovers(t *testing.T) {
	f := newRestoreFake()
	f.fail["virsh define"] = true
	f.fail["umount"] = true
	_, err := RestoreReleased(context.Background(), f, relEntry(), restoreOpts(t, false))
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeRollbackFailed || len(oe.Leftovers) != 1 || !strings.Contains(oe.Leftovers[0], "/var/lib/libvirt/zz") {
		t.Fatalf("%v", err)
	}
	// it still demotes and downs only if the unmount worked: here secondary
	// runs (DRBD refuses while mounted in reality); the report names the mount.
	if !f.has("drbdadm secondary") {
		t.Fatalf("%v", f.cmds)
	}
}

func TestRestoreReleasedPrefersConfigFileWithSameIdentity(t *testing.T) {
	f := newRestoreFake()
	opts := restoreOpts(t, false)
	newer := strings.Replace(relXML("web", relUUID), "131072", "262144", 1)
	opts.ReadFile = func(p string) ([]byte, error) {
		if p != "/var/lib/libvirt/zz/web.xml" {
			t.Fatalf("read %s", p)
		}
		return []byte(newer), nil
	}
	res, err := RestoreReleased(context.Background(), f, relEntry(), opts)
	if err != nil || res.XMLSource != ReleaseSourceConfigFile || !res.XMLDiffers || f.definedXML != newer {
		t.Fatalf("%+v %v", res, err)
	}
	// A file with another identity is ignored: the saved XML is defined.
	f2 := newRestoreFake()
	opts.ReadFile = func(string) ([]byte, error) { return []byte(relXML("intruder", relUUID)), nil }
	res, err = RestoreReleased(context.Background(), f2, relEntry(), opts)
	if err != nil || res.XMLSource != ReleaseSourceSaved || !strings.Contains(f2.definedXML, "<name>web</name>") {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRestoreReleasedPeersDown(t *testing.T) {
	// 8.21 defect 2: after stop/forget the DRBD agent leaves the resource
	// down on every node; with quorum the local promote cannot succeed. The
	// restore must stop before promoting, list the peers with the command
	// to run there, and leave nothing behind.
	peersDownStatus := "zz_web node-id:0 role:ROLE suspended:no\n  volume:0 minor:1001 disk:UpToDate open:no quorum:no blocked:no\n" +
		"  ha2 node-id:1 connection:Connecting role:Unknown\n    volume:0 replication:Off peer-disk:DUnknown\n" +
		"  ha3 node-id:2 connection:Connecting role:Unknown\n    volume:0 replication:Off peer-disk:DUnknown\n"
	for _, tc := range []struct {
		name, status string
		peersDown    bool
		quorum       string
		wantPeers    []string
	}{
		{name: "quorum no, two peers", status: peersDownStatus, wantPeers: []string{"ha2", "ha3"}},
		{name: "quorum not reported, only peer down", peersDown: true, wantPeers: []string{"ha2"}},
		{name: "quorum no even if disk not uptodate", peersDown: true, quorum: "no", wantPeers: []string{"ha2"}},
		{name: "peer line without connection field", status: "zz_web node-id:0 role:ROLE\n  volume:0 disk:UpToDate\n  ha2 node-id:1 role:Unknown\n", wantPeers: []string{"ha2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRestoreFake()
			f.status, f.peersDown, f.quorum = tc.status, tc.peersDown, tc.quorum
			if tc.quorum == "no" {
				f.uptodate = false
			}
			_, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false))
			var oe *OpError
			if !errors.As(err, &oe) || oe.Code != CodeDRBDPeersDown {
				t.Fatalf("want drbd_peers_down, got %v (%v)", err, f.cmds)
			}
			peers, _ := oe.Details["peers"].([]DRBDPeerDown)
			if oe.Details["resource"] != "zz_web" || len(peers) != len(tc.wantPeers) {
				t.Fatalf("details %+v", oe.Details)
			}
			for i, p := range peers {
				if p.Node != tc.wantPeers[i] || p.Command != "drbdadm up zz_web" || p.Connection == "" {
					t.Fatalf("peer %+v", p)
				}
			}
			if f.up || f.has("drbdadm primary") || f.has("mount -t") || f.has("virsh define") || !f.has("drbdadm down zz_web") {
				t.Fatalf("not a no-op: %v", f.cmds)
			}
		})
	}
	t.Run("already up is left up", func(t *testing.T) {
		f := newRestoreFake()
		f.up, f.peersDown = true, true
		_, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeDRBDPeersDown || !f.up || f.has("drbdadm down") || f.has("drbdadm up") {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
	t.Run("invalid peer name gets no command", func(t *testing.T) {
		f := newRestoreFake()
		f.status = "zz_web node-id:0 role:ROLE\n  volume:0 disk:UpToDate quorum:no\n  bad;name node-id:1 connection:Connecting\n"
		_, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeDRBDPeersDown {
			t.Fatalf("%v", err)
		}
		if peers := oe.Details["peers"].([]DRBDPeerDown); len(peers) != 1 || peers[0].Command != "" {
			t.Fatalf("%+v", peers)
		}
	})
	t.Run("one of three down with quorum yes proceeds", func(t *testing.T) {
		f := newRestoreFake()
		f.status = "zz_web node-id:0 role:ROLE\n  volume:0 disk:UpToDate quorum:yes\n" +
			"  ha2 node-id:1 connection:Connected role:Secondary\n    volume:0 peer-disk:UpToDate\n" +
			"  ha3 node-id:2 connection:Connecting role:Unknown\n"
		if _, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false)); err != nil {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
	t.Run("malformed status is not promoted", func(t *testing.T) {
		f := newRestoreFake()
		f.status = "garbage without structure\n"
		_, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeDRBDNotUpToDate || f.has("drbdadm primary") || f.up {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
	t.Run("promote refused with peers connected still rolls back", func(t *testing.T) {
		f := newRestoreFake()
		f.fail["drbdadm primary"] = true
		_, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeDRBDPromoteFailed || f.up {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
}

func TestReadDRBDPeers(t *testing.T) {
	f := newRestoreFake()
	f.up = true
	if got := ReadDRBDPeers(context.Background(), f, "zz_web"); len(got) != 1 || got[0] != "ha2" {
		t.Fatalf("%v", got)
	}
	if got := ReadDRBDPeers(context.Background(), f, "../x"); got != nil {
		t.Fatalf("%v", got)
	}
	f.up = false
	if got := ReadDRBDPeers(context.Background(), f, "zz_web"); got != nil {
		t.Fatalf("%v", got)
	}
}

func TestRestoreReleasedDefinitionLink(t *testing.T) {
	t.Run("config file on the filesystem is linked, not rewritten", func(t *testing.T) {
		f := newRestoreFake()
		f.files["/var/lib/libvirt/zz/web.xml"] = "file"
		opts := hostOpts(t, f, false)
		opts.ReadFile = func(string) ([]byte, error) { return []byte(relXML("web", relUUID)), nil }
		res, err := RestoreReleased(context.Background(), f, relEntry(), opts)
		if err != nil || !res.XMLLinked || res.XMLSource != ReleaseSourceConfigFile {
			t.Fatalf("%+v %v", res, err)
		}
		if f.has("install") || f.has("mv ") || f.files[relDefLink] != "link:/var/lib/libvirt/zz/web.xml" {
			t.Fatalf("%v %v", f.cmds, f.files)
		}
	})
	t.Run("foreign file is never overwritten", func(t *testing.T) {
		f := newRestoreFake()
		f.files["/var/lib/libvirt/zz/web.xml"] = "file"
		res, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeXMLLinkFailed || res.Changed {
			t.Fatalf("%v", err)
		}
		if f.has("install") || f.has("rm ") || f.files["/var/lib/libvirt/zz/web.xml"] != "file" || f.defined || f.mounted || f.up {
			t.Fatalf("%v %v", f.cmds, f.files)
		}
	})
	t.Run("no config path recorded uses <mount>/<vm>.xml", func(t *testing.T) {
		f := newRestoreFake()
		e := relEntry()
		e.Stack.ConfigPath = ""
		if _, err := RestoreReleased(context.Background(), f, e, hostOpts(t, f, false)); err != nil {
			t.Fatal(err)
		}
		if f.files[relDefLink] != "link:/var/lib/libvirt/zz/web.xml" {
			t.Fatalf("%v", f.files)
		}
	})
	for _, step := range []string{"install", "sync", "mv", "ln", "virsh start"} {
		t.Run("rollback when "+step+" fails", func(t *testing.T) {
			f := newRestoreFake()
			f.fail[step] = true
			_, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, true))
			var oe *OpError
			want := CodeXMLLinkFailed
			if step == "virsh start" {
				want = CodeStartFailed
			}
			if !errors.As(err, &oe) || oe.Code != want {
				t.Fatalf("want %s: %v", want, err)
			}
			if len(f.files) != 0 || f.defined || f.mounted || f.primary || f.up || f.run {
				t.Fatalf("left behind: files=%v cmds=%v", f.files, f.cmds)
			}
		})
	}
	t.Run("without storage nothing is linked", func(t *testing.T) {
		f := newRestoreFake()
		e := relEntry()
		e.Stack = KVMStack{}
		res, err := RestoreReleased(context.Background(), f, e, hostOpts(t, f, false))
		if err != nil || res.XMLLinked || f.has("ln ") {
			t.Fatalf("%+v %v %v", res, err, f.cmds)
		}
	})
}

func TestRestoreReleasedIdempotencyVerifiesHostState(t *testing.T) {
	restored := func(t *testing.T) *restoreFake {
		f := newRestoreFake()
		if _, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false)); err != nil {
			t.Fatal(err)
		}
		f.cmds = nil
		return f
	}
	t.Run("all in place is a no-op", func(t *testing.T) {
		f := restored(t)
		res, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, true))
		if err != nil || res.Changed || res.State != "defined" || !res.XMLLinked || len(f.writes()) != 0 {
			t.Fatalf("%+v %v %v", res, err, f.writes())
		}
	})
	t.Run("mount only in the service namespace is repaired", func(t *testing.T) {
		// The v13 state: domain defined, DRBD Primary (held by the hidden
		// mount), host findmnt empty.
		f := restored(t)
		f.hiddenMount = true
		f.mounted = false
		opts := hostOpts(t, f, false)
		f.hiddenMount = false
		res, err := RestoreReleased(context.Background(), f, relEntry(), opts)
		if err != nil || !res.Changed || !f.mounted || !f.host["mount -t ext4 /dev/drbd/by-res/zz_web/0 /var/lib/libvirt/zz"] {
			t.Fatalf("%+v %v %v", res, err, f.cmds)
		}
		if f.has("drbdadm up") || f.has("drbdadm primary") || f.has("virsh define") || f.has("virsh undefine") {
			t.Fatalf("repair touched what was in place: %v", f.cmds)
		}
	})
	t.Run("DRBD secondary is promoted and remounted", func(t *testing.T) {
		f := restored(t)
		f.mounted, f.primary = false, false
		res, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false))
		if err != nil || !res.Changed || !f.primary || !f.mounted || f.has("virsh define") {
			t.Fatalf("%+v %v %v", res, err, f.cmds)
		}
	})
	t.Run("repair failure never undefines the existing domain", func(t *testing.T) {
		f := restored(t)
		f.mounted, f.primary, f.up = false, false, false
		f.fail["mount -t"] = true
		_, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false))
		var oe *OpError
		if !errors.As(err, &oe) || oe.Code != CodeMountFailed || !f.defined || f.has("virsh undefine") || f.up || f.primary {
			t.Fatalf("%v %v", err, f.cmds)
		}
	})
	t.Run("plain definition file (v13) is moved onto the filesystem and linked", func(t *testing.T) {
		f := restored(t)
		f.files = map[string]string{relDefLink: "file"}
		res, err := RestoreReleased(context.Background(), f, relEntry(), hostOpts(t, f, false))
		if err != nil || !res.Changed || !res.XMLLinked {
			t.Fatalf("%+v %v", res, err)
		}
		if !f.has("install -m 0600 -T "+relDefLink+" /var/lib/libvirt/zz/web.xml.webkvm-new") || f.files[relDefLink] != "link:/var/lib/libvirt/zz/web.xml" {
			t.Fatalf("%v %v", f.cmds, f.files)
		}
	})
}

func TestReleasedStoreAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	s := ReleasedStore{Dir: dir}
	if list, err := s.List(); err != nil || len(list) != 0 {
		t.Fatalf("%v %v", list, err)
	}
	e := relEntry()
	prev, err := s.Put(e)
	if err != nil || prev != nil {
		t.Fatalf("%v %v", prev, err)
	}
	fi, err := os.Stat(filepath.Join(dir, releasedStoreFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi, err)
	}
	if tmp, _ := filepath.Glob(filepath.Join(dir, ".ha-released-*")); len(tmp) != 0 {
		t.Fatalf("temp files left: %v", tmp)
	}
	got, ok, err := (ReleasedStore{Dir: dir}).Get("web")
	if err != nil || !ok || got.XML != e.XML || got.Stack.DRBDResource != "zz_web" {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	e2 := e
	e2.Mode = ModeForget
	prev, err = s.Put(e2)
	if err != nil || prev == nil || prev.Mode != ModeStop {
		t.Fatalf("%v %v", prev, err)
	}
	if err := s.Revert("web", prev); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get("web"); got.Mode != ModeStop {
		t.Fatalf("revert: %+v", got)
	}
	if v := got.View(); v.HasXML != true || !v.Restorable || v.Key != "kvm:web" {
		t.Fatalf("view %+v", v)
	}
	if found, err := s.Delete("web"); !found || err != nil {
		t.Fatalf("%v %v", found, err)
	}
	if found, _ := s.Delete("web"); found {
		t.Fatal("double delete")
	}
	if _, err := (ReleasedStore{}).Put(e); err == nil {
		t.Fatal("store without a data dir must fail")
	}
	if _, err := s.Put(ReleasedVM{Name: "../etc"}); err == nil {
		t.Fatal("invalid name accepted")
	}
}

func TestReleasedViewHidesXML(t *testing.T) {
	v := relEntry().View()
	b := fmt.Sprintf("%+v", v)
	if strings.Contains(b, "<domain") {
		t.Fatalf("view leaks XML: %s", b)
	}
}

const relCIB = `<cib><configuration><nodes><node uname="ha1"/></nodes><resources>
<clone id="ha-kvm-web-data-clone"><primitive id="ha-kvm-web-data" class="ocf" provider="linbit" type="drbd"><instance_attributes><nvpair name="drbd_resource" value="zz_web"/></instance_attributes></primitive></clone>
<group id="ha-kvm-web-grp"><primitive id="ha-kvm-web-fs" class="ocf" provider="heartbeat" type="Filesystem"><instance_attributes><nvpair name="device" value="/dev/drbd/by-res/zz_web/0"/><nvpair name="directory" value="/var/lib/libvirt/zz"/><nvpair name="fstype" value="ext4"/></instance_attributes></primitive>
<primitive id="ha-kvm-web" class="ocf" provider="heartbeat" type="VirtualDomain"><instance_attributes><nvpair name="config" value="/var/lib/libvirt/zz/web.xml"/></instance_attributes><meta_attributes id="m"><nvpair name="webkvm-kind" value="kvm"/><nvpair name="webkvm-name" value="web"/></meta_attributes></primitive></group>
</resources><constraints/></configuration></cib>`

type cibOnly struct{ cib string }

func (c cibOnly) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "pcs" && strings.Join(args, " ") == "cluster cib" {
		return []byte(c.cib), nil
	}
	return nil, nil
}
func (c cibOnly) RunWithTimeout(n string, _ time.Duration, a ...string) ([]byte, error) {
	return c.Run(context.Background(), n, a...)
}

func TestReadKVMStackAndRestoreConflicts(t *testing.T) {
	st, err := ReadKVMStack(context.Background(), cibOnly{relCIB}, "web")
	if err != nil || st != relEntry().Stack {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := ReadKVMStack(context.Background(), cibOnly{relCIB}, "ghost"); err == nil {
		t.Fatal("ghost found")
	}
	var oe *OpError
	if err := RestoreConflicts(context.Background(), cibOnly{relCIB}, "web", "zz_web", ""); !errors.As(err, &oe) || oe.Code != CodeWorkloadManaged {
		t.Fatalf("%v", err)
	}
	if err := RestoreConflicts(context.Background(), cibOnly{relCIB}, "other", "zz_web", ""); !errors.As(err, &oe) || oe.Code != CodeDRBDManaged {
		t.Fatalf("%v", err)
	}
	if err := RestoreConflicts(context.Background(), cibOnly{relCIB}, "other", "zz_other", ""); err != nil {
		t.Fatalf("%v", err)
	}
	empty := `<cib><configuration><nodes/><resources/><constraints/></configuration></cib>`
	if err := RestoreConflicts(context.Background(), cibOnly{empty}, "web", "zz_web", ""); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestParseDomainIdentity(t *testing.T) {
	if n, u, err := ParseDomainIdentity([]byte(relXML("web", relUUID))); err != nil || n != "web" || u != relUUID {
		t.Fatalf("%s %s %v", n, u, err)
	}
	for _, bad := range []string{"", "<domain><name>../x</name></domain>", "<domain><name>web</name><uuid>zz</uuid></domain>", "not xml"} {
		if _, _, err := ParseDomainIdentity([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, _, err := ParseDomainIdentity([]byte("<domain><name>web</name>" + strings.Repeat(" ", maxReleaseXMLBytes) + "</domain>")); err == nil {
		t.Fatal("oversized XML accepted")
	}
}
