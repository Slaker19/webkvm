package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"webkvm/internal/ha"
)

type fakeInspector struct {
	driver  string
	devices []ha.DeviceInfo
	cfg     map[string]string
	online  map[string]bool
	nics    []ha.NICInfo
	nets    map[string]map[string]bool
	managed bool
}

func (f fakeInspector) RootPoolDriver(string) string    { return f.driver }
func (f fakeInspector) Devices(string) []ha.DeviceInfo  { return f.devices }
func (f fakeInspector) Config(string) map[string]string { return f.cfg }
func (f fakeInspector) MemberOnline(member string) bool { return f.online[member] }
func (f fakeInspector) NICs(string) []ha.NICInfo        { return f.nics }
func (f fakeInspector) NetworkMembers(name string) (map[string]bool, bool, error) {
	p, ok := f.nets[name]
	if !ok {
		return nil, false, errors.New("not probed")
	}
	return p, f.managed, nil
}

func indexOf(cmds []string, sub string) int {
	for i, c := range cmds {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

// KVM: enable dry_run -> enable -> list shows managed -> disable (stop).
func TestHAWorkloadEndToEndKVM(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true}
	h := setupWL(t, r, goodFacts(), true)

	code, body := doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{"dry_run":true}`)
	if code != http.StatusOK || body["changed"] != true || r.has("cib-push") {
		t.Fatalf("dry run: %d %v", code, body)
	}
	code, body = doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{"preferred_node":"ha1"}`)
	if code != http.StatusOK || body["changed"] != true || body["state"] != "started" {
		t.Fatalf("enable: %d %v", code, body)
	}
	code, body = doWL(h, http.MethodGet, "/workloads", "")
	wls, _ := body["workloads"].([]any)
	if code != 200 || len(wls) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}
	first := wls[0].(map[string]any)
	if first["key"] != "kvm:web" || first["managed"] != true || first["state"] != "started" || first["node"] != "ha1" {
		t.Fatalf("workload: %v", first)
	}
	code, body = doWL(h, http.MethodDelete, "/workloads/kvm/web", "")
	if code != http.StatusOK || body["state"] != "absent" || body["changed"] != true {
		t.Fatalf("disable: %d %v", code, body)
	}
	if indexOf(r.cmds, "pcs resource disable ha-kvm-web-grp") > indexOf(r.cmds, "resource delete") {
		t.Fatalf("stop must disable before deleting: %v", r.cmds)
	}
	_, body = doWL(h, http.MethodGet, "/workloads", "")
	if wls, _ := body["workloads"].([]any); len(wls) != 0 {
		t.Fatalf("workload still listed: %v", body)
	}
}

// Incus: autostart is forced off before the CIB push, then the same cycle.
func TestHAWorkloadEndToEndIncus(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true}
	insp := fakeInspector{driver: "linstor", cfg: map[string]string{"boot.autostart": "true"},
		online: map[string]bool{"ha1": true, "ha2": true, "ha3": true}}
	h := setupWL(t, r, fakeFacts{insp: insp, members: []string{"ha1", "ha2", "ha3"}}, true)

	code, body := doWL(h, http.MethodPost, "/workloads/incus/c1/enable?project=default", `{"dry_run":true}`)
	if code != http.StatusOK || body["changed"] != true {
		t.Fatalf("dry run: %d %v", code, body)
	}
	if r.has("boot.autostart") || r.has("cib-push") {
		t.Fatalf("dry run must not touch the instance or CIB: %v", r.cmds)
	}
	code, body = doWL(h, http.MethodPost, "/workloads/incus/c1/enable?project=default", `{}`)
	if code != http.StatusOK || body["state"] != "started" {
		t.Fatalf("enable: %d %v", code, body)
	}
	auto, push := indexOf(r.cmds, "incus config set c1 boot.autostart=false --project default"), indexOf(r.cmds, "cib-push")
	if auto < 0 || push < 0 || auto > push {
		t.Fatalf("boot.autostart=false must precede the CIB push (%d, %d): %v", auto, push, r.cmds)
	}
	code, body = doWL(h, http.MethodGet, "/workloads", "")
	wls, _ := body["workloads"].([]any)
	if code != 200 || len(wls) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}
	first := wls[0].(map[string]any)
	if first["key"] != "incus:default/c1" || first["kind"] != "incus" || first["managed"] != true || first["node"] != "ha2" {
		t.Fatalf("workload: %v", first)
	}
	code, body = doWL(h, http.MethodDelete, "/workloads/incus/c1?project=default", "")
	if code != http.StatusOK || body["state"] != "absent" {
		t.Fatalf("disable: %d %v", code, body)
	}
	_, body = doWL(h, http.MethodGet, "/workloads", "")
	if wls, _ := body["workloads"].([]any); len(wls) != 0 {
		t.Fatalf("workload still listed: %v", body)
	}
}

func TestHAWorkloadIncusBlockersAndErrorCodes(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true}
	insp := fakeInspector{driver: "dir", online: map[string]bool{"ha1": true, "ha2": false},
		devices: []ha.DeviceInfo{{Name: "g", Type: "gpu"}}}
	h := setupWL(t, r, fakeFacts{insp: insp, members: []string{"ha1", "ha2"}}, true)
	code, body := doWL(h, http.MethodPost, "/workloads/incus/c1/enable?project=default", `{}`)
	if code != http.StatusConflict || body["code"] != "precondition_failed" {
		t.Fatalf("%d %v", code, body)
	}
	for _, want := range []string{"not_eligible", "incus_member_not_online"} {
		if !strings.Contains(blockerList(body), want) {
			t.Errorf("missing %s in %s", want, blockerList(body))
		}
	}
	if r.has("boot.autostart") || r.has("cib-push") {
		t.Fatalf("nothing may be written: %v", r.cmds)
	}
	// An instance that does not exist is a 404 with the documented code.
	h2 := setupWL(t, &wlRunner{watchdog: true, hasCP: true}, fakeFacts{}, true)
	if code, body = doWL(h2, http.MethodPost, "/workloads/incus/ghost/enable?project=default", `{}`); code != 404 || body["code"] != "workload_not_found" {
		t.Fatalf("%d %v", code, body)
	}
}
