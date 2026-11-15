package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/auth"
	"webkvm/internal/config"
	"webkvm/internal/ha"
	"webkvm/internal/sysutil"
)

const webUUIDRel = "5b0c6f1e-1111-4222-8333-944455556666"

// relRunner is the wlRunner Pacemaker fake plus one node's DRBD, mount table
// and libvirt for the restore path.
type relRunner struct {
	*wlRunner
	mu                                     sync.Mutex
	up, primary, mounted, defined, running bool
	fail                                   map[string]bool
	cmds                                   []string
	// onDisable runs when the first `pcs resource disable` is issued.
	onDisable func()
	// peersDown: the DRBD peers are not connected (quorum lost).
	peersDown bool
	// host marks the commands run through systemd-run (host mount ns).
	host map[string]bool
	// files models the host filesystem: path -> "file" or "link:<target>".
	files map[string]string
	// drbdInCIB adds the DRBD clone of the VM to the CIB that is read.
	drbdInCIB bool
}

const relDRBDClone = `<clone id="ha-kvm-web-data-clone"><primitive id="ha-kvm-web-data" class="ocf" provider="linbit" type="drbd"><instance_attributes><nvpair name="drbd_resource" value="vm_web"/></instance_attributes></primitive></clone>`

func newRelRunner(w *wlRunner) *relRunner {
	return &relRunner{wlRunner: w, fail: map[string]bool{}, host: map[string]bool{}, files: map[string]string{}}
}

func (f *relRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	viaHost := false
	if name == "systemd-run" {
		for i, a := range args {
			if a == "--" {
				name, args, viaHost = args[i+1], args[i+2:], true
				break
			}
		}
	}
	line := strings.Replace(name+" "+strings.Join(args, " "), "virsh -c qemu:///system ", "virsh ", 1)
	if name == "pcs" {
		if strings.HasPrefix(line, "pcs resource disable") && f.onDisable != nil {
			f.onDisable()
			f.onDisable = nil
		}
		out, err := f.wlRunner.Run(ctx, name, args...)
		if f.drbdInCIB && err == nil && strings.Join(args, " ") == "cluster cib" {
			out = []byte(strings.Replace(string(out), `<group id="ha-kvm-web-grp">`, relDRBDClone+`<group id="ha-kvm-web-grp">`, 1))
		}
		return out, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, line)
	if viaHost {
		f.host[line] = true
	}
	for p := range f.fail {
		if strings.HasPrefix(line, p) {
			return []byte("simulated failure"), errors.New("exit status 1")
		}
	}
	switch {
	case strings.HasPrefix(line, "virsh domuuid"):
		if !f.defined {
			return []byte("error: failed to get domain 'web'"), errors.New("exit status 1")
		}
		return []byte(webUUIDRel + "\n"), nil
	case strings.HasPrefix(line, "drbdsetup status"):
		if !f.up {
			return nil, errors.New("exit status 10")
		}
		role := "Secondary"
		if f.primary {
			role = "Primary"
		}
		if f.peersDown {
			return []byte(fmt.Sprintf("vm_web node-id:0 role:%s\n  volume:0 disk:UpToDate quorum:no\n  ha2 node-id:1 connection:Connecting role:Unknown\n  ha3 node-id:2 connection:Connecting role:Unknown\n", role)), nil
		}
		return []byte(fmt.Sprintf("vm_web node-id:0 role:%s\n  volume:0 disk:UpToDate\n  ha2 node-id:1 connection:Connected role:Secondary\n    volume:0 peer-disk:UpToDate\n", role)), nil
	case strings.HasPrefix(line, "drbdadm up"):
		f.up = true
	case strings.HasPrefix(line, "drbdadm primary"):
		f.primary = true
	case strings.HasPrefix(line, "drbdadm secondary"):
		f.primary = false
	case strings.HasPrefix(line, "drbdadm down"):
		f.up = false
	case strings.HasPrefix(line, "findmnt"):
		if !f.mounted {
			return nil, errors.New("exit status 1")
		}
		return []byte("/dev/drbd/by-res/vm_web/0\n"), nil
	case strings.HasPrefix(line, "mount "):
		f.mounted = true
	case strings.HasPrefix(line, "umount"):
		f.mounted = false
	case strings.HasPrefix(line, "virsh define"):
		f.defined = true
		f.files[libvirtDefinitionDir+"/web.xml"] = "file"
	case strings.HasPrefix(line, "virsh undefine"):
		f.defined = false
		delete(f.files, libvirtDefinitionDir+"/web.xml")
	case name == "stat":
		if _, ok := f.files[args[len(args)-1]]; !ok {
			return []byte("stat: cannot statx: No such file or directory"), errors.New("exit status 1")
		}
	case name == "readlink":
		v := f.files[args[len(args)-1]]
		if !strings.HasPrefix(v, "link:") {
			return nil, errors.New("exit status 1")
		}
		return []byte(strings.TrimPrefix(v, "link:")), nil
	case name == "install":
		f.files[args[len(args)-1]] = "file"
	case name == "mv":
		f.files[args[len(args)-1]] = f.files[args[len(args)-2]]
		delete(f.files, args[len(args)-2])
	case name == "ln":
		f.files[args[len(args)-1]] = "link:" + args[len(args)-2]
	case name == "rm":
		delete(f.files, args[len(args)-1])
	case strings.HasPrefix(line, "virsh start"):
		f.running = true
	}
	return nil, nil
}

func (f *relRunner) RunWithTimeout(n string, _ time.Duration, a ...string) ([]byte, error) {
	return f.Run(context.Background(), n, a...)
}

func (f *relRunner) did(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func setupRel(t *testing.T, w *wlRunner) (http.Handler, *relRunner, string) {
	t.Helper()
	h := setupWL(t, w, goodFacts(), true)
	rr := newRelRunner(w)
	sysutil.DefaultRunner = rr
	prevNS := releaseHostNamespace
	releaseHostNamespace = func() bool { return true }
	t.Cleanup(func() { releaseHostNamespace = prevNS })
	return h, rr, w.dataDir
}

func storeEntry(t *testing.T, dir string) (ha.ReleasedVM, bool) {
	t.Helper()
	e, ok, err := (ha.ReleasedStore{Dir: dir}).Get("web")
	if err != nil {
		t.Fatal(err)
	}
	return e, ok
}

func seedReleased(t *testing.T, dir string) ha.ReleasedVM {
	t.Helper()
	e := ha.ReleasedVM{Name: "web", UUID: webUUIDRel, Status: ha.ReleaseStatusReleased, Mode: ha.ModeStop,
		Stack: ha.KVMStack{DRBDResource: "vm_web", Device: "/dev/drbd/by-res/vm_web/0", MountDir: "/mnt/vm", FSType: "ext4", ConfigPath: "/mnt/vm/web.xml"},
		XML:   webDomainXML, XMLSource: ha.ReleaseSourceConfigFile, XMLSHA256: ha.XMLDigest(webDomainXML), ReleasedAt: time.Now().UTC()}
	if _, err := (ha.ReleasedStore{Dir: dir}).Put(e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestHADisableKVMCapturesBeforeTouchingTheCluster(t *testing.T) {
	w := &wlRunner{watchdog: true, hasCP: true, vm: true, started: true}
	h, rr, dir := setupRel(t, w)
	storedBeforeDisable := false
	rr.onDisable = func() {
		e, ok, _ := (ha.ReleasedStore{Dir: dir}).Get("web")
		storedBeforeDisable = ok && e.XML == webDomainXML
	}
	code, body := doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=stop", "")
	if code != http.StatusOK || body["state"] != "absent" {
		t.Fatalf("%d %v", code, body)
	}
	if !storedBeforeDisable {
		t.Fatal("the inventory entry must be stored before the first pcs write")
	}
	rel, _ := body["released"].(map[string]any)
	if rel == nil || rel["status"] != "released" || rel["restorable"] != true || rel["uuid"] != webUUIDRel || rel["last_node"] != "ha1" {
		t.Fatalf("released view: %v", body["released"])
	}
	if _, leaked := rel["xml"]; leaked {
		t.Fatal("the API must never return the XML")
	}
	e, ok := storeEntry(t, dir)
	if !ok || e.Stack.ConfigPath != "/mnt/vm/web.xml" || e.XMLSource != ha.ReleaseSourceConfigFile || e.Unrestorable {
		t.Fatalf("%+v", e)
	}
	fi, err := os.Stat(filepath.Join(dir, "ha-released-vms.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store file %v %v", fi, err)
	}
	// Listed afterwards, still without XML.
	code, body = doWL(h, http.MethodGet, "/released", "")
	list, _ := body["released"].([]any)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["has_xml"] != true {
		t.Fatalf("%d %v", code, body)
	}
	// A second disable of the (now absent) workload creates no ghost entry
	// and leaves the existing one alone.
	before, _ := os.ReadFile(filepath.Join(dir, "ha-released-vms.json"))
	if code, body = doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=stop", ""); code != 200 || body["changed"] != false {
		t.Fatalf("%d %v", code, body)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "ha-released-vms.json"))
	if string(before) != string(after) {
		t.Fatal("idempotent disable rewrote the inventory")
	}
}

func TestHADisableKVMRefusedWhenXMLCannotBeCaptured(t *testing.T) {
	w := &wlRunner{watchdog: true, hasCP: true, vm: true, started: true}
	h, _, dir := setupRel(t, w)
	releaseReadFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
	code, body := doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=stop", "")
	if code != http.StatusConflict || body["code"] != ha.CodeReleaseCaptureFailed {
		t.Fatalf("%d %v", code, body)
	}
	if w.has("pcs resource disable") || w.has("resource delete") || w.has("cib-push") || !w.vm {
		t.Fatalf("refusal must not touch the cluster: %v", w.cmds)
	}
	if _, ok := storeEntry(t, dir); ok {
		t.Fatal("no entry on refusal")
	}
	// A definition with another identity is not a capture either.
	releaseReadFile = func(string) ([]byte, error) {
		return []byte(strings.Replace(webDomainXML, "<name>web</name>", "<name>evil</name>", 1)), nil
	}
	if code, body = doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=stop", ""); code != http.StatusConflict || body["code"] != ha.CodeReleaseCaptureFailed {
		t.Fatalf("%d %v", code, body)
	}
	// Bulk disable follows the same rule.
	code, body = doWL(h, http.MethodPost, "/workloads/bulk", `{"action":"disable","items":[{"kind":"kvm","name":"web"}]}`)
	res, _ := body["results"].([]any)
	if code != 200 || len(res) != 1 || res[0].(map[string]any)["code"] != ha.CodeReleaseCaptureFailed || w.has("pcs resource disable") {
		t.Fatalf("%d %v", code, body)
	}
	// Explicit acceptance proceeds and records an unrestorable entry.
	code, body = doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=stop&accept_unrestorable=true", "")
	if code != http.StatusOK || body["state"] != "absent" {
		t.Fatalf("%d %v", code, body)
	}
	e, ok := storeEntry(t, dir)
	if !ok || !e.Unrestorable || e.XML != "" || e.View().Restorable {
		t.Fatalf("%+v", e)
	}
}

func TestHADisableKVMPeerAndPreviousCaptureSources(t *testing.T) {
	w := &wlRunner{watchdog: true, hasCP: true, vm: true, started: true}
	h, _, dir := setupRel(t, w)
	// The VM runs on ha1 but this panel is ha2: the file is not read here;
	// the member's libvirt answers.
	releaseLocalNode = func() string { return "ha2" }
	read := false
	releaseReadFile = func(string) ([]byte, error) { read = true; return []byte(webDomainXML), nil }
	asked := ""
	releasePeerDomainXML = func(_ context.Context, _ *Handler, node, name string) (string, error) {
		asked = node + "/" + name
		return webDomainXML, nil
	}
	if code, body := doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=stop", ""); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	if read || asked != "ha1/web" {
		t.Fatalf("read=%v asked=%q", read, asked)
	}
	if e, _ := storeEntry(t, dir); e.XMLSource != ha.ReleaseSourceLibvirtPeer {
		t.Fatalf("%+v", e)
	}
}

func TestHADisableKVMFailureWithoutChangeRevertsTheEntry(t *testing.T) {
	w := &wlRunner{watchdog: true, hasCP: true, vm: true, started: true}
	h, _, dir := setupRel(t, w)
	prev := seedReleased(t, dir)
	prev.Mode = ha.ModeForget
	if _, err := (ha.ReleasedStore{Dir: dir}).Put(prev); err != nil {
		t.Fatal(err)
	}
	unlock, ok := ha.LockKey("kvm:web")
	if !ok {
		t.Fatal("lock")
	}
	code, body := doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=stop", "")
	unlock()
	if code != http.StatusConflict || body["code"] != ha.CodeBusy {
		t.Fatalf("%d %v", code, body)
	}
	if e, ok := storeEntry(t, dir); !ok || e.Mode != ha.ModeForget {
		t.Fatalf("previous entry not restored: %+v", e)
	}
}

func TestHADisablePauseAndIncusDoNotCapture(t *testing.T) {
	w := &wlRunner{watchdog: true, hasCP: true, vm: true, started: true}
	h, _, dir := setupRel(t, w)
	if code, body := doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=pause", ""); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	if _, ok := storeEntry(t, dir); ok {
		t.Fatal("pause must not create a released entry")
	}
}

func TestHARestoreReleasedSuccessAndIdempotent(t *testing.T) {
	w := &wlRunner{watchdog: true, hasCP: true}
	h, rr, dir := setupRel(t, w)
	seedReleased(t, dir)
	code, body := doWL(h, http.MethodPost, "/released/web/restore", `{"confirm":"web","start":true}`)
	if code != http.StatusOK || body["changed"] != true || body["state"] != "running" || body["node"] != "ha1" {
		t.Fatalf("%d %v", code, body)
	}
	if !rr.up || !rr.primary || !rr.mounted || !rr.defined || !rr.running {
		t.Fatalf("state %+v", rr.cmds)
	}
	e, _ := storeEntry(t, dir)
	if e.Status != ha.ReleaseStatusRestored || e.RestoredNode != "ha1" || e.XML == "" {
		t.Fatalf("entry must be kept and marked restored: %+v", e)
	}
	rr.cmds = nil
	code, body = doWL(h, http.MethodPost, "/released/web/restore", `{"confirm":"web","start":true}`)
	if code != http.StatusOK || body["changed"] != false {
		t.Fatalf("%d %v", code, body)
	}
	if rr.did("drbdadm") || rr.did("mount") || rr.did("virsh define") || rr.did("virsh start") {
		t.Fatalf("idempotent restore wrote: %v", rr.cmds)
	}
	// Discard requires the name and only removes the inventory entry.
	if code, body = doWL(h, http.MethodDelete, "/released/web", ""); code != http.StatusConflict || body["code"] != ha.CodeConfirmation {
		t.Fatalf("%d %v", code, body)
	}
	rr.cmds = nil
	if code, body = doWL(h, http.MethodDelete, "/released/web?confirm=web", ""); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	if len(rr.cmds) != 0 || w.has("pcs resource") {
		t.Fatalf("discard must not run commands: %v", rr.cmds)
	}
	if code, body = doWL(h, http.MethodDelete, "/released/web?confirm=web", ""); code != 404 || body["code"] != ha.CodeReleaseNotFound {
		t.Fatalf("%d %v", code, body)
	}
}

func TestHARestoreReleasedRollsBackEachStep(t *testing.T) {
	for _, tc := range []struct {
		fail   string
		status int
		code   string
	}{
		{"drbdadm up", http.StatusBadGateway, ha.CodeDRBDUpFailed},
		{"drbdadm primary", http.StatusBadGateway, ha.CodeDRBDPromoteFailed},
		{"mount -t", http.StatusBadGateway, ha.CodeMountFailed},
		{"virsh define", http.StatusBadGateway, ha.CodeDefineFailed},
		{"virsh start", http.StatusBadGateway, ha.CodeStartFailed},
	} {
		t.Run(tc.fail, func(t *testing.T) {
			w := &wlRunner{watchdog: true, hasCP: true}
			h, rr, dir := setupRel(t, w)
			seedReleased(t, dir)
			rr.fail[tc.fail] = true
			code, body := doWL(h, http.MethodPost, "/released/web/restore", `{"confirm":"web","start":true}`)
			if code != tc.status || body["code"] != tc.code {
				t.Fatalf("%d %v", code, body)
			}
			if rr.up || rr.primary || rr.mounted || rr.defined || rr.running {
				t.Fatalf("not rolled back: %v", rr.cmds)
			}
			if e, _ := storeEntry(t, dir); e.Status != ha.ReleaseStatusReleased {
				t.Fatalf("entry must stay released: %+v", e)
			}
		})
	}
}

func TestHARestoreReleasedRefusals(t *testing.T) {
	t.Run("still managed by pacemaker", func(t *testing.T) {
		w := &wlRunner{watchdog: true, hasCP: true, vm: true, started: true}
		h, rr, dir := setupRel(t, w)
		seedReleased(t, dir)
		code, body := doWL(h, http.MethodPost, "/released/web/restore", `{"confirm":"web"}`)
		if code != http.StatusConflict || body["code"] != ha.CodeWorkloadManaged || len(rr.cmds) != 0 {
			t.Fatalf("%d %v %v", code, body, rr.cmds)
		}
	})
	w := &wlRunner{watchdog: true, hasCP: true}
	h, rr, dir := setupRel(t, w)
	for _, tc := range []struct {
		path, body string
		status     int
		code       string
	}{
		{"/released/web/restore", `{"confirm":"nope"}`, http.StatusConflict, ha.CodeConfirmation},
		{"/released/web/restore", `{}`, http.StatusConflict, ha.CodeConfirmation},
		{"/released/web/restore", `{"confirm":"web","node":"ha3"}`, http.StatusConflict, ha.CodeRestoreNodeNotLocal},
		{"/released/web/restore", `{"confirm":"web"}`, http.StatusNotFound, ha.CodeReleaseNotFound},
		{"/released/..%2Fetc/restore", `{"confirm":"../etc"}`, http.StatusBadRequest, "invalid_body"},
		{"/released/web/restore", `not json`, http.StatusBadRequest, "invalid_body"},
	} {
		code, body := doWL(h, http.MethodPost, tc.path, tc.body)
		if code != tc.status || body["code"] != tc.code {
			t.Errorf("%s %s: %d %v", tc.path, tc.body, code, body)
		}
	}
	if len(rr.cmds) != 0 {
		t.Fatalf("refusals must not run commands: %v", rr.cmds)
	}
	// An unrestorable entry is refused without commands.
	e := seedReleased(t, dir)
	e.XML, e.Unrestorable = "", true
	if _, err := (ha.ReleasedStore{Dir: dir}).Put(e); err != nil {
		t.Fatal(err)
	}
	if code, body := doWL(h, http.MethodPost, "/released/web/restore", `{"confirm":"web"}`); code != http.StatusConflict || body["code"] != ha.CodeReleaseUnrestorable || len(rr.cmds) != 0 {
		t.Fatalf("%d %v %v", code, body, rr.cmds)
	}
}

// The released routes sit in the admin-only group with the other HA routes.
func TestHAReleasedRoutesAreAdminOnly(t *testing.T) {
	w := &wlRunner{watchdog: true, hasCP: true}
	setupWL(t, w, goodFacts(), true)
	rr := newRelRunner(w)
	sysutil.DefaultRunner = rr
	seedReleased(t, w.dataDir)
	h := &Handler{cfg: &config.Config{DataDir: w.dataDir}}
	router := chi.NewRouter()
	h.mountHAWorkloads(router)
	for _, role := range []string{"", "viewer", "operator"} {
		for _, tc := range []struct{ method, path, body string }{
			{http.MethodGet, "/released", ""},
			{http.MethodPost, "/released/web/restore", `{"confirm":"web"}`},
			{http.MethodDelete, "/released/web?confirm=web", ""},
		} {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if role != "" {
				req.Header.Set(auth.HeaderRole, role)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
				t.Errorf("role %q %s %s: %d", role, tc.method, tc.path, rec.Code)
			}
		}
	}
	if len(rr.cmds) != 0 {
		t.Fatalf("unauthorised calls ran commands: %v", rr.cmds)
	}
	if _, ok := storeEntry(t, w.dataDir); !ok {
		t.Fatal("unauthorised discard removed the entry")
	}
	req := httptest.NewRequest(http.MethodGet, "/released", nil)
	req.Header.Set(auth.HeaderRole, "admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "<domain") {
		t.Fatalf("admin list: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOpToAPIReleaseCodes(t *testing.T) {
	for code, status := range map[string]int{
		ha.CodeReleaseNotFound: 404, ha.CodeReleaseStoreFailed: 500, ha.CodeDRBDUpFailed: 502, ha.CodeMountFailed: 502,
		ha.CodeDefineFailed: 502, ha.CodeStartFailed: 502, ha.CodeDRBDPrimaryElsewhere: 409, ha.CodeDomainConflict: 409,
		ha.CodeWorkloadManaged: 409, ha.CodeReleaseXMLInvalid: 409,
		ha.CodeDRBDPeersDown: 409, ha.CodeMountNotVisible: 502, ha.CodeXMLLinkFailed: 502,
	} {
		if e := opToAPI(&ha.OpError{Code: code}); e.Status != status {
			t.Errorf("%s: %d want %d", code, e.Status, status)
		}
	}
	// Details reach the body but never override error/code.
	e := opToAPI(&ha.OpError{Code: ha.CodeDRBDPeersDown, Msg: "m", Details: map[string]any{"resource": "r", "code": "x"}})
	if b := e.body(); b["resource"] != "r" || b["code"] != ha.CodeDRBDPeersDown || b["error"] != "m" {
		t.Fatalf("%v", b)
	}
}

// 8.21 defects 1/3: the host-visible steps run through systemd-run and the
// definition ends up linked from libvirt's directory to the shared mount.
func TestHARestoreReleasedHostNamespaceAndLink(t *testing.T) {
	w := &wlRunner{watchdog: true, hasCP: true}
	h, rr, dir := setupRel(t, w)
	seedReleased(t, dir)
	code, body := doWL(h, http.MethodPost, "/released/web/restore", `{"confirm":"web"}`)
	if code != http.StatusOK || body["changed"] != true || body["xml_linked"] != true {
		t.Fatalf("%d %v", code, body)
	}
	for _, c := range []string{"findmnt -rn -o SOURCE --mountpoint /mnt/vm", "mkdir -p /mnt/vm", "mount -t ext4 /dev/drbd/by-res/vm_web/0 /mnt/vm",
		"ln -sfnT /mnt/vm/web.xml " + libvirtDefinitionDir + "/web.xml"} {
		if !rr.host[c] {
			t.Errorf("%q did not run in the host mount namespace (%v)", c, rr.cmds)
		}
	}
	// The fixture's /mnt/vm/web.xml is this VM's definition: linked, not rewritten.
	if body["xml_source"] != ha.ReleaseSourceConfigFile || rr.did("install") || rr.files[libvirtDefinitionDir+"/web.xml"] != "link:/mnt/vm/web.xml" {
		t.Fatalf("%v files %v", body, rr.files)
	}
	// Without systemd the same steps run directly (no private namespace).
	releaseHostNamespace = func() bool { return false }
	rr2 := newRelRunner(w)
	sysutil.DefaultRunner = rr2
	if _, err := (ha.ReleasedStore{Dir: dir}).Delete("web"); err != nil {
		t.Fatal(err)
	}
	seedReleased(t, dir)
	if code, body = doWL(h, http.MethodPost, "/released/web/restore", `{"confirm":"web"}`); code != http.StatusOK {
		t.Fatalf("%d %v", code, body)
	}
	if len(rr2.host) != 0 || !rr2.mounted {
		t.Fatalf("unexpected wrapping: %v", rr2.host)
	}
}

// 8.21 defect 2: peers down after stop/forget -> typed 409 with the exact
// commands, and nothing left behind.
func TestHARestoreReleasedPeersDown(t *testing.T) {
	w := &wlRunner{watchdog: true, hasCP: true}
	h, rr, dir := setupRel(t, w)
	seedReleased(t, dir)
	rr.peersDown = true
	code, body := doWL(h, http.MethodPost, "/released/web/restore", `{"confirm":"web","start":true}`)
	if code != http.StatusConflict || body["code"] != ha.CodeDRBDPeersDown || body["resource"] != "vm_web" {
		t.Fatalf("%d %v", code, body)
	}
	peers, _ := body["peers"].([]any)
	if len(peers) != 2 {
		t.Fatalf("peers %v", body["peers"])
	}
	for i, want := range []string{"ha2", "ha3"} {
		p := peers[i].(map[string]any)
		if p["node"] != want || p["command"] != "drbdadm up vm_web" || p["connection"] != "Connecting" {
			t.Fatalf("peer %v", p)
		}
	}
	if rr.up || rr.primary || rr.mounted || rr.defined || rr.did("drbdadm primary") {
		t.Fatalf("not a no-op: %v", rr.cmds)
	}
	if e, _ := storeEntry(t, dir); e.Status != ha.ReleaseStatusReleased {
		t.Fatalf("entry must stay released: %+v", e)
	}
	// Peers brought up by the operator: the retry (UI "Re-check") succeeds.
	rr.peersDown = false
	if code, body = doWL(h, http.MethodPost, "/released/web/restore", `{"confirm":"web"}`); code != http.StatusOK || body["changed"] != true {
		t.Fatalf("%d %v", code, body)
	}
}

// The capture notes the DRBD peers (read-only) so the UI can say where
// `drbdadm up` is needed before restoring.
func TestHADisableKVMRecordsDRBDPeers(t *testing.T) {
	w := &wlRunner{watchdog: true, hasCP: true, vm: true, started: true}
	h, rr, dir := setupRel(t, w)
	rr.up, rr.drbdInCIB = true, true
	code, body := doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=stop", "")
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, body)
	}
	e, ok := storeEntry(t, dir)
	if !ok || len(e.DRBDPeers) != 1 || e.DRBDPeers[0] != "ha2" {
		t.Fatalf("%+v", e)
	}
	rel, _ := body["released"].(map[string]any)
	if peers, _ := rel["drbd_peers"].([]any); len(peers) != 1 || peers[0] != "ha2" {
		t.Fatalf("view %v", rel)
	}
	if rr.did("drbdadm") {
		t.Fatalf("capture must be read-only: %v", rr.cmds)
	}
}
