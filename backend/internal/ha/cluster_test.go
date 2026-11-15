package ha

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"webkvm/internal/sysutil"
)

type fakeHost struct {
	mu       sync.Mutex
	calls    []string
	failOn   string // "pcs cluster start" prefix to fail
	active   string // systemctl is-active answer
	activeEr bool
	lsmod    string
	soft     bool
	virt     string
	quorum   string
	stonith  bool // adds a stonith primitive to the CIB (device mode)
}

func (f *fakeHost) runner() *sysutil.MockCommandRunner {
	return &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		line := name + " " + strings.Join(args, " ")
		f.calls = append(f.calls, line)
		if f.failOn != "" && strings.HasPrefix(line, f.failOn) {
			return []byte("Error: boom"), errors.New("exit 1")
		}
		switch name {
		case "systemctl":
			if f.activeEr {
				return nil, errors.New("no systemctl")
			}
			out := f.active
			if out == "" {
				out = "inactive"
			}
			return []byte(out + "\n"), errors.New("exit 3")
		case "lsmod":
			return []byte("Module Size Used by\n" + f.lsmod), nil
		case "modinfo":
			if f.soft {
				return []byte("ok"), nil
			}
			return nil, errors.New("not found")
		case "systemd-detect-virt":
			if f.virt == "" || f.virt == "none" {
				return []byte("none\n"), errors.New("exit 1")
			}
			return []byte(f.virt + "\n"), nil
		case "pcs":
			switch strings.Join(args, " ") {
			case "status xml":
				q := f.quorum
				if q == "" {
					q = "true"
				}
				return []byte(`<pacemaker-result><status code="0"/><crm_mon><summary><cluster_name>c</cluster_name><quorum>` + q + `</quorum></summary><nodes><node name="n1" online="true" type="member"/><node name="n2" online="true" type="member"/><node name="n3" online="true" type="member"/></nodes></crm_mon></pacemaker-result>`), nil
			case "cluster cib":
				res := ""
				if f.stonith {
					res = `<resources><primitive class="stonith" type="fence_pve" id="fence-pve"/></resources>`
				}
				return []byte(`<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-enabled" value="true"/><nvpair name="stonith-watchdog-timeout" value="10s"/></cluster_property_set></crm_config>` + res + `<nodes><node uname="n1"/><node uname="n2"/><node uname="n3"/></nodes></configuration></cib>`), nil
			}
		}
		return nil, nil
	}}
}

func (f *fakeHost) pcsCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "pcs ") && c != "pcs status xml" && c != "pcs cluster cib" {
			out = append(out, c)
		}
	}
	return out
}

func testEnv(t *testing.T, f *fakeHost) ClusterEnv {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "watchdog"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "known-hosts"), []byte(`{"known_hosts":{"n1":{"token":"SECRET-TOKEN"},"n2":{},"n3":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return ClusterEnv{
		Runner:        f.runner(),
		CorosyncConf:  filepath.Join(dir, "etc", "corosync.conf"),
		CIBPath:       filepath.Join(dir, "cib.xml"),
		PcsKnownHosts: filepath.Join(dir, "known-hosts"),
		WatchdogGlob:  filepath.Join(dir, "watchdog*"),
		SBDPaths:      []string{filepath.Join(dir, "sysconfig-sbd"), filepath.Join(dir, "sbd")},
		Hostname:      func() (string, error) { return "n1.lab", nil },
		LocalIPs:      func() []netip.Addr { return nil },
		PackagesOK:    true,
	}
}

func goodReq() ClusterRequest {
	return ClusterRequest{ClusterName: "webkvm-ha", Nodes: []ClusterNode{{"n1", "10.0.0.1"}, {"n2", "10.0.0.2"}, {"n3", "10.0.0.3"}}}
}

func has(list []string, v string) bool { return containsStr(list, v) }

func TestPlanHappyPath(t *testing.T) {
	f := &fakeHost{lsmod: "iTCO_wdt 1 0\n", virt: "none"}
	env := testEnv(t, f)
	plan := env.Plan(context.Background(), goodReq())
	if !plan.OK || len(plan.Blockers) != 0 {
		t.Fatalf("plan not ok: %+v", plan)
	}
	if plan.Watchdog.Type != "hardware" || !plan.Watchdog.Recommended || plan.Virt.IsVM || plan.NodesNeeded != 3 {
		t.Fatalf("probe: %+v %+v", plan.Watchdog, plan.Virt)
	}
	want := []string{
		"sudo pcs cluster setup webkvm-ha n1 addr=10.0.0.1 n2 addr=10.0.0.2 n3 addr=10.0.0.3",
		"sudo pcs stonith sbd enable watchdog=/dev/watchdog SBD_WATCHDOG_TIMEOUT=5",
		"sudo pcs cluster start --all",
		"sudo pcs property set stonith-enabled=true stonith-watchdog-timeout=10s no-quorum-policy=stop",
	}
	for _, w := range want {
		if !has(plan.Commands, w) {
			t.Fatalf("missing command %q in %v", w, plan.Commands)
		}
	}
	if len(plan.ManualSteps) == 0 {
		t.Fatal("manual steps expected")
	}
}

func TestPlanBlockers(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ClusterRequest, *ClusterEnv, *fakeHost)
		want   string
		warn   string
	}{
		{"bad name", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.ClusterName = "bad name;" }, CodeInvalidClusterName, ""},
		{"no nodes", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.Nodes = nil }, CodeInvalidNodeList, ""},
		{"dup name", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.Nodes[1].Name = "N1" }, CodeInvalidNodeList, ""},
		{"dup addr", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.Nodes[1].Address = "10.0.0.1" }, CodeInvalidNodeList, ""},
		{"bad addr", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.Nodes[1].Address = "host.example" }, CodeInvalidNodeList, ""},
		{"flag-like name", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.Nodes[1].Name = "--force" }, CodeInvalidNodeList, ""},
		{"one node", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.Nodes = r.Nodes[:1] }, CodeInvalidNodeList, ""},
		{"two nodes", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.Nodes = r.Nodes[:2] }, CodeQuorumNeedsThree, ""},
		{"two nodes allowed", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.Nodes = r.Nodes[:2]; r.AllowTwoNodes = true }, "", CodeQuorumNeedsThree},
		{"cidr mismatch", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) { e.ClusterCIDR = "192.168.0.0/24" }, CodeCIDRMismatch, ""},
		{"cidr invalid", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) { e.ClusterCIDR = "nonsense" }, CodeCIDRMismatch, ""},
		{"local missing", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) {
			e.Hostname = func() (string, error) { return "other", nil }
		}, CodeLocalNodeMissing, ""},
		{"packages", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) { e.PackagesOK = false }, CodePackagesMissing, ""},
		{"timeout low", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.SBD.TimeoutSeconds = 1 }, CodeInvalidSBDTimeout, ""},
		{"bad watchdog", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.SBD.WatchdogDevice = "/etc/passwd" }, CodeInvalidWatchdogDev, ""},
		{"watchdog absent device", func(r *ClusterRequest, _ *ClusterEnv, _ *fakeHost) { r.SBD.WatchdogDevice = "/dev/watchdog7" }, CodeInvalidWatchdogDev, ""},
		{"no watchdog", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) {
			e.WatchdogGlob = filepath.Join(t.TempDir(), "watchdog*")
		}, CodeWatchdogMissing, ""},
		{"no watchdog but sbd off", func(r *ClusterRequest, e *ClusterEnv, _ *fakeHost) {
			e.WatchdogGlob = filepath.Join(t.TempDir(), "watchdog*")
			off := false
			r.SBDEnable = &off
		}, "", CodeSBDDisabledWarning},
		{"softdog warning", func(_ *ClusterRequest, _ *ClusterEnv, f *fakeHost) { f.lsmod = "softdog 1 0\n" }, "", CodeSoftdogWarning},
		{"auth missing", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) {
			_ = os.WriteFile(e.PcsKnownHosts, []byte(`{"known_hosts":{"n1":{}}}`), 0o600)
		}, CodeHostAuthMissing, ""},
		{"auth file absent", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) { e.PcsKnownHosts += ".none" }, CodeHostAuthMissing, ""},
		{"auth unreadable", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) {
			_ = os.WriteFile(e.PcsKnownHosts, []byte(`not json`), 0o600)
		}, CodeHostAuthUnknown, ""},
		{"exists conf", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) {
			_ = os.MkdirAll(filepath.Dir(e.CorosyncConf), 0o755)
			_ = os.WriteFile(e.CorosyncConf, []byte("x"), 0o600)
		}, CodeClusterExists, ""},
		{"exists cib", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) { _ = os.WriteFile(e.CIBPath, []byte("x"), 0o600) }, CodeClusterExists, ""},
		{"exists conf unreadable", func(_ *ClusterRequest, e *ClusterEnv, _ *fakeHost) {
			_ = os.WriteFile(filepath.Join(filepath.Dir(e.CIBPath), "etc"), []byte("file"), 0o600) // ENOTDIR
		}, CodeClusterExists, ""},
		{"exists active pacemaker", func(_ *ClusterRequest, _ *ClusterEnv, f *fakeHost) { f.active = "active" }, CodeClusterExists, ""},
		{"exists activating", func(_ *ClusterRequest, _ *ClusterEnv, f *fakeHost) { f.active = "activating" }, CodeClusterExists, ""},
		{"exists state unreadable", func(_ *ClusterRequest, _ *ClusterEnv, f *fakeHost) { f.activeEr = true }, CodeClusterExists, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeHost{lsmod: "iTCO_wdt 1 0\n"}
			env := testEnv(t, f)
			req := goodReq()
			tc.mutate(&req, &env, f)
			plan := env.Plan(context.Background(), req)
			if tc.want != "" {
				if plan.OK || !has(plan.Blockers, tc.want) {
					t.Fatalf("want blocker %s, got ok=%v blockers=%v", tc.want, plan.OK, plan.Blockers)
				}
			} else if !plan.OK {
				t.Fatalf("unexpected blockers %v", plan.Blockers)
			}
			if tc.warn != "" && !has(plan.Warnings, tc.warn) {
				t.Fatalf("want warning %s, got %v", tc.warn, plan.Warnings)
			}
		})
	}
}

func TestPlanSoftdogAvailableNotLoadedAddsManualStep(t *testing.T) {
	f := &fakeHost{soft: true}
	env := testEnv(t, f)
	env.WatchdogGlob = filepath.Join(t.TempDir(), "watchdog*")
	plan := env.Plan(context.Background(), goodReq())
	if has(plan.Blockers, CodeWatchdogMissing) || !has(plan.Warnings, CodeSoftdogWarning) || plan.Watchdog.Type != "softdog" || plan.Watchdog.Present {
		t.Fatalf("%+v", plan)
	}
	if !strings.Contains(strings.Join(plan.ManualSteps, "\n"), "modprobe softdog") {
		t.Fatal("modprobe step missing")
	}
}

func TestPlanVirtDetectionAndNoSecretOrPassword(t *testing.T) {
	f := &fakeHost{lsmod: "i6300esb 1 0\n", virt: "kvm"}
	env := testEnv(t, f)
	plan := env.Plan(context.Background(), goodReq())
	if plan.Watchdog.Type != "i6300esb" || !plan.Virt.IsVM || plan.Virt.Kind != "kvm" {
		t.Fatalf("%+v %+v", plan.Watchdog, plan.Virt)
	}
	blob := strings.Join(append(append(plan.Commands, plan.ManualSteps...), plan.Warnings...), "\n")
	if strings.Contains(blob, "SECRET-TOKEN") || strings.Contains(blob, " -p ") || strings.Contains(blob, "--force") || strings.Contains(blob, "destroy") || strings.Contains(blob, "--overwrite") {
		t.Fatalf("unsafe content: %s", blob)
	}
}

func TestPlanProbeFailuresNeverPanic(t *testing.T) {
	env := testEnv(t, &fakeHost{})
	env.Runner = &sysutil.MockCommandRunner{Handler: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("down")
	}}
	plan := env.Plan(context.Background(), goodReq())
	if plan.OK || !has(plan.Blockers, CodeClusterExists) {
		t.Fatalf("unreadable state must block: %+v", plan)
	}
}

func TestRewriteSBDConfigPreservesUnrelated(t *testing.T) {
	in := "# header\nSBD_DELAY_START=no\n#SBD_WATCHDOG_DEV=/dev/old\nSBD_WATCHDOG_DEV=/dev/watchdog1\nSBD_OPTS=\"-n x\"\nSBD_WATCHDOG_DEV=/dev/dup\n"
	got := RewriteSBDConfig(in, [][2]string{{"SBD_PACEMAKER", "yes"}, {"SBD_WATCHDOG_DEV", "/dev/watchdog"}, {"SBD_WATCHDOG_TIMEOUT", "5"}})
	want := "# header\nSBD_DELAY_START=no\n#SBD_WATCHDOG_DEV=/dev/old\nSBD_WATCHDOG_DEV=/dev/watchdog\nSBD_OPTS=\"-n x\"\nSBD_PACEMAKER=yes\nSBD_WATCHDOG_TIMEOUT=5\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if RewriteSBDConfig("", [][2]string{{"A", "1"}}) != "A=1\n" {
		t.Fatal("empty input")
	}
}

func TestWriteSBDConfigOnDisk(t *testing.T) {
	env := testEnv(t, &fakeHost{})
	path := env.SBDPaths[1]
	if err := os.WriteFile(path, []byte("KEEP=me\nSBD_WATCHDOG_TIMEOUT=30\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := env.WriteSBDConfig("/dev/watchdog", "5")
	if err != nil || got != path {
		t.Fatalf("%v %s", err, got)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "KEEP=me") || !strings.Contains(string(data), "SBD_WATCHDOG_TIMEOUT=5") || strings.Contains(string(data), "=30") {
		t.Fatalf("%s", data)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func runSetup(t *testing.T, f *fakeHost, mutate func(*ClusterRequest, *ClusterEnv)) (SetupStatus, ClusterEnv) {
	t.Helper()
	env := testEnv(t, f)
	req := goodReq()
	if mutate != nil {
		mutate(&req, &env)
	}
	m := NewSetupManager(t.TempDir())
	m.sleep = func(time.Duration) {}
	if err := m.Start(env, req, nil); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	return m.Snapshot(), env
}

func TestSetupStepOrderAndNoDestructiveFlags(t *testing.T) {
	f := &fakeHost{lsmod: "iTCO_wdt 1 0\n"}
	st, env := runSetup(t, f, func(r *ClusterRequest, _ *ClusterEnv) { r.EnableOnBoot = true })
	if st.State != SetupDone {
		t.Fatalf("state %s err=%s step=%s log=%v", st.State, st.Error, st.FailedStep, st.Log)
	}
	want := []string{
		"pcs cluster setup webkvm-ha n1 addr=10.0.0.1 n2 addr=10.0.0.2 n3 addr=10.0.0.3",
		"pcs stonith sbd enable watchdog=/dev/watchdog SBD_WATCHDOG_TIMEOUT=5",
		"pcs cluster start --all",
		"pcs cluster enable --all",
		"pcs property set stonith-enabled=true stonith-watchdog-timeout=10s no-quorum-policy=stop",
	}
	got := f.pcsCalls()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("pcs calls\n%v\nwant\n%v", got, want)
	}
	for _, c := range f.calls {
		for _, bad := range []string{"destroy", "--force", "--overwrite", "host auth", " -p", "passwd", "chpasswd"} {
			if strings.Contains(c, bad) {
				t.Fatalf("unsafe call %q", c)
			}
		}
	}
	for i, s := range st.Steps {
		if s.ID != setupStepOrder[i] || s.State != StepDone {
			t.Fatalf("step %d = %+v", i, s)
		}
	}
	if data, err := os.ReadFile(env.SBDPath()); err != nil || !strings.Contains(string(data), "SBD_PACEMAKER=yes") {
		t.Fatalf("sbd file: %v %s", err, data)
	}
	if strings.Contains(strings.Join(st.Log, "\n"), "SECRET") {
		t.Fatal("secret in log")
	}
}

func TestSetupNoEnableOnBootByDefaultAndSBDOff(t *testing.T) {
	f := &fakeHost{}
	st, _ := runSetup(t, f, func(r *ClusterRequest, _ *ClusterEnv) { off := false; r.SBDEnable = &off })
	if st.State != SetupDone {
		t.Fatalf("%+v", st)
	}
	for _, c := range f.pcsCalls() {
		if strings.Contains(c, "enable --all") || strings.Contains(c, "sbd") || strings.Contains(c, "watchdog") {
			t.Fatalf("unexpected %s", c)
		}
	}
	if st.Steps[1].State != StepSkipped {
		t.Fatalf("sbd_config must be skipped: %+v", st.Steps[1])
	}
}

func TestSetupStopsOnFailureWithRemainingCommands(t *testing.T) {
	f := &fakeHost{failOn: "pcs cluster start", lsmod: "iTCO_wdt 1 0\n"}
	st, _ := runSetup(t, f, nil)
	if st.State != SetupFailed || st.FailedStep != "cluster_start" {
		t.Fatalf("%+v", st)
	}
	for _, c := range f.pcsCalls() {
		if strings.Contains(c, "property set") {
			t.Fatal("must stop after the failing step")
		}
	}
	joined := strings.Join(st.CommandsRemaining, "\n")
	if !strings.Contains(joined, "sudo pcs cluster start --all") || !strings.Contains(joined, "sudo pcs property set stonith-enabled=true") || strings.Contains(joined, "cluster setup") {
		t.Fatalf("remaining: %v", st.CommandsRemaining)
	}
	if st.Steps[3].State != StepFailed || st.Steps[4].State != StepPending {
		t.Fatalf("steps %+v", st.Steps)
	}
}

func TestSetupRefusesWhenClusterExistsAtPreflight(t *testing.T) {
	f := &fakeHost{active: "active", lsmod: "iTCO_wdt 1 0\n"}
	st, _ := runSetup(t, f, nil)
	if st.State != SetupFailed || st.FailedStep != "preflight" || len(f.pcsCalls()) != 0 {
		t.Fatalf("%+v calls=%v", st, f.pcsCalls())
	}
}

func TestSetupVerifyFailsWithoutQuorum(t *testing.T) {
	f := &fakeHost{quorum: "false", lsmod: "iTCO_wdt 1 0\n"}
	st, _ := runSetup(t, f, nil)
	if st.State != SetupFailed || st.FailedStep != "verify" {
		t.Fatalf("%+v", st)
	}
}

func TestSetupSingleFlightAndInterruptedRestart(t *testing.T) {
	f := &fakeHost{lsmod: "iTCO_wdt 1 0\n"}
	env := testEnv(t, f)
	release := make(chan struct{})
	inner := env.Runner.(*sysutil.MockCommandRunner).Handler
	env.Runner = &sysutil.MockCommandRunner{Handler: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "pcs" && len(args) > 1 && args[1] == "setup" {
			<-release
		}
		return inner(ctx, name, args...)
	}}
	dir := t.TempDir()
	m := NewSetupManager(dir)
	m.sleep = func(time.Duration) {}
	if err := m.Start(env, goodReq(), nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(env, goodReq(), nil); !errors.Is(err, ErrSetupBusy) {
		t.Fatalf("want busy, got %v", err)
	}
	// A second process reading the state mid-run sees an interrupted job.
	if s := NewSetupManager(dir).Snapshot(); s.State != SetupFailed || s.FailedStep != "interrupted" {
		t.Fatalf("%+v", s)
	}
	close(release)
	m.Wait()
}

func TestRecordedStatusJSONShapeNeverNull(t *testing.T) {
	s := NewSetupManager("").Snapshot()
	if s.State != SetupIdle || s.Steps == nil || s.Log == nil || s.CommandsRemaining == nil {
		t.Fatalf("%+v", s)
	}
}
