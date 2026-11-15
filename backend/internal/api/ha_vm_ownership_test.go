package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/configstore"
	"webkvm/internal/models"
	"webkvm/internal/sysutil"
)

func useRunner(t *testing.T, r sysutil.CommandRunner) {
	t.Helper()
	prev := sysutil.DefaultRunner
	t.Cleanup(func() { sysutil.DefaultRunner = prev; invalidateHAProbe() })
	sysutil.DefaultRunner = r
	invalidateHAProbe()
}

func haModuleHandler(t *testing.T) *Handler {
	t.Helper()
	settings, err := configstore.New(t.TempDir(), configstore.DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	if _, failed, err := settings.SetMany(map[string]any{"modules.cluster_ha": true}); err != nil || len(failed) > 0 {
		t.Fatalf("enable module: %v %v", err, failed)
	}
	return &Handler{settings: settings}
}

func withVMID(req *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestApplyPacemakerManagementCoversKVMAndIncus(t *testing.T) {
	useRunner(t, &wlRunner{hasCP: true, vm: true, started: true, inc: true, incStarted: true})
	vms := []models.VM{
		{Name: "web", Hypervisor: "kvm", Node: "local"},
		{Name: "c1", Hypervisor: "incus", Node: "ha3"}, // Incus Node is the cluster member
		{Name: "web", Hypervisor: "incus"},             // same name, other kind: not managed
		{Name: "c1", Hypervisor: "incus", NodeID: "n-remote"},
		{Name: "other", Hypervisor: "kvm"},
	}
	applyPacemakerManagement(context.Background(), vms)
	if vms[0].ManagedBy != "pacemaker" || vms[0].HAKey != "kvm:web" || vms[0].PacemakerNode != "ha1" {
		t.Fatalf("kvm: %+v", vms[0])
	}
	if vms[1].ManagedBy != "pacemaker" || vms[1].HAKey != "incus:default/c1" || vms[1].PacemakerNode != "ha2" || vms[1].Node != "ha3" {
		t.Fatalf("incus: %+v", vms[1])
	}
	if vms[2].ManagedBy != "" || vms[3].ManagedBy != "" || vms[4].ManagedBy != "" {
		t.Fatalf("unrelated entries must stay writable: %+v", vms[2:])
	}
	data, _ := json.Marshal(vms[1])
	if !strings.Contains(string(data), `"ha_key":"incus:default/c1"`) || !strings.Contains(string(data), `"managed_by":"pacemaker"`) {
		t.Fatalf("json: %s", data)
	}
	plain, _ := json.Marshal(models.VM{Name: "x"})
	if strings.Contains(string(plain), "ha_key") {
		t.Fatalf("ha_key must be omitted: %s", plain)
	}
}

func TestRejectPacemakerManagedIncusAndMaintenance(t *testing.T) {
	r := &wlRunner{hasCP: true, inc: true, incStarted: true}
	useRunner(t, r)
	h := haModuleHandler(t)
	do := func(target string, strict bool) (int, string) {
		req := withVMID(httptest.NewRequest(http.MethodPost, target, nil), "c1")
		rec := httptest.NewRecorder()
		var rejected bool
		if strict {
			rejected = h.rejectPacemakerManagedStrict(rec, req, "c1", "incus")
		} else {
			rejected = h.rejectPacemakerManaged(rec, req, "c1")
		}
		if !rejected {
			return 0, ""
		}
		return rec.Code, rec.Body.String()
	}
	if code, body := do("/api/vms/c1/start?hv=incus", false); code != 409 || !strings.Contains(body, "pacemaker_managed") || !strings.Contains(body, "incus:default/c1") {
		t.Fatalf("incus must be rejected: %d %s", code, body)
	}
	if code, _ := do("/api/vms/c1/start", false); code != 409 {
		t.Fatalf("name-only request must be rejected too: %d", code)
	}
	if code, _ := do("/api/vms/c1/start?hv=kvm", false); code != 0 {
		t.Fatalf("hv=kvm names another workload: %d", code)
	}
	// Maintenance window: lifecycle allowed, move/migrate still refused.
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	r.maint = `<nvpair name="is-managed" value="false"/><nvpair name="webkvm-maint-until" value="` + until + `"/>`
	invalidateHAProbe()
	if code, body := do("/api/vms/c1/start?hv=incus", false); code != 0 {
		t.Fatalf("maintenance must allow lifecycle: %d %s", code, body)
	}
	if code, _ := do("/api/vms/c1/move", true); code != 409 {
		t.Fatalf("strict guard ignores maintenance: %d", code)
	}
	// Expired window: unmanaged but no longer an exception.
	r.maint = `<nvpair name="is-managed" value="false"/><nvpair name="webkvm-maint-until" value="2020-01-01T00:00:00Z"/>`
	invalidateHAProbe()
	if code, _ := do("/api/vms/c1/start?hv=incus", false); code != 409 {
		t.Fatalf("expired window must not allow lifecycle: %d", code)
	}
}

const dupCIB = `<cib><configuration><nodes><node uname="ha1"/></nodes><resources>
<primitive id="ha-kvm-dup" class="ocf" provider="heartbeat" type="VirtualDomain"><instance_attributes><nvpair name="config" value="/x.xml"/></instance_attributes><meta_attributes id="a"><nvpair name="webkvm-kind" value="kvm"/><nvpair name="webkvm-name" value="dup"/></meta_attributes></primitive>
<primitive id="ha-incus-default-dup" class="ocf" provider="webkvm" type="incus"><meta_attributes id="b"><nvpair name="webkvm-kind" value="incus"/><nvpair name="webkvm-name" value="dup"/><nvpair name="webkvm-project" value="default"/></meta_attributes></primitive>
</resources><constraints/></configuration></cib>`

func TestRejectPacemakerManagedAmbiguousName(t *testing.T) {
	useRunner(t, &sysutil.MockCommandRunner{Handler: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "cluster cib" {
			return []byte(dupCIB), nil
		}
		return nil, nil
	}})
	h := haModuleHandler(t)
	rec := httptest.NewRecorder()
	if !h.rejectPacemakerManaged(rec, httptest.NewRequest(http.MethodPost, "/api/vms/dup/start", nil), "dup") ||
		rec.Code != 409 || !strings.Contains(rec.Body.String(), "ambiguous_workload") {
		t.Fatalf("both kinds managed, hv absent: %d %s", rec.Code, rec.Body.String())
	}
	for _, hv := range []string{"kvm", "incus"} {
		rec = httptest.NewRecorder()
		if !h.rejectPacemakerManaged(rec, httptest.NewRequest(http.MethodPost, "/api/vms/dup/start?hv="+hv, nil), "dup") ||
			!strings.Contains(rec.Body.String(), "pacemaker_managed") {
			t.Fatalf("hv=%s: %d %s", hv, rec.Code, rec.Body.String())
		}
	}
}

func TestPacemakerProbeIsCachedAndModuleOffNeverProbes(t *testing.T) {
	var cibCalls atomic.Int32
	useRunner(t, &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "pcs" {
			t.Fatalf("unexpected exec %s", name)
		}
		if strings.Join(args, " ") == "cluster cib" {
			cibCalls.Add(1)
			return []byte(dupCIB), nil
		}
		return nil, nil
	}})
	h := haModuleHandler(t)
	for i := 0; i < 5; i++ {
		h.rejectPacemakerManaged(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/vms/dup/start?hv=kvm", nil), "dup")
		applyPacemakerManagement(context.Background(), []models.VM{{Name: "dup", Hypervisor: "kvm"}})
	}
	if n := cibCalls.Load(); n != 1 {
		t.Fatalf("burst must cost one probe, got %d", n)
	}
	invalidateHAProbe()
	h.rejectPacemakerManaged(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/vms/dup/start?hv=kvm", nil), "dup")
	if n := cibCalls.Load(); n != 2 {
		t.Fatalf("invalidate must force a new probe, got %d", n)
	}
	// Module off: no pcs at all.
	useRunner(t, &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("module off must not exec: %s %v", name, args)
		return nil, nil
	}})
	if (&Handler{}).rejectPacemakerManaged(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/vms/dup/start", nil), "dup") {
		t.Fatal("module off must not reject")
	}
}

func TestMigrateVMRejectedEvenInMaintenance(t *testing.T) {
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	useRunner(t, &wlRunner{hasCP: true, vm: true, started: true,
		maint: `<nvpair name="is-managed" value="false"/><nvpair name="webkvm-maint-until" value="` + until + `"/>`})
	h := haModuleHandler(t)
	req := withVMID(httptest.NewRequest(http.MethodPost, "/api/vms/web/migrate", strings.NewReader(`{"node":"x"}`)), "web")
	req.Header.Set("X-Role", models.RoleAdmin)
	rec := httptest.NewRecorder()
	h.MigrateVM(rec, req)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "pacemaker_managed") || !strings.Contains(rec.Body.String(), "/api/ha/workloads/kvm/web/move") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestMoveVMRejectsPacemakerManagedIncus(t *testing.T) {
	useRunner(t, &wlRunner{hasCP: true, inc: true, incStarted: true})
	h := haModuleHandler(t)
	req := withVMID(httptest.NewRequest(http.MethodPost, "/api/vms/c1/move", strings.NewReader(`{"target_member":"ha1"}`)), "c1")
	rec := httptest.NewRecorder()
	h.MoveVM(rec, req)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "pacemaker_managed") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// An unmanaged instance falls through to the normal flow (no cluster here).
	req = withVMID(httptest.NewRequest(http.MethodPost, "/api/vms/free/move", strings.NewReader(`{"target_member":"ha1"}`)), "free")
	rec = httptest.NewRecorder()
	h.MoveVM(rec, req)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "not_clustered") {
		t.Fatalf("unmanaged: %d %s", rec.Code, rec.Body.String())
	}
}

func TestEvacuateRestoreRefusedWhileMemberHostsManagedIncus(t *testing.T) {
	useRunner(t, &wlRunner{hasCP: true, inc: true, incStarted: true}) // c1 runs on ha2
	h := haModuleHandler(t)
	al, err := audit.New(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = al.Close() })
	h.audit = al

	call := func(fn http.HandlerFunc, member, target, body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", member)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec.Code, rec.Body.String()
	}
	for name, fn := range map[string]http.HandlerFunc{"evacuate": h.EvacuateClusterMember, "restore": h.RestoreClusterMember} {
		code, body := call(fn, "ha2", "/api/cluster/members/ha2/"+name, `{}`)
		if code != 409 || !strings.Contains(body, "healing_managed_by_pacemaker") || !strings.Contains(body, "incus:default/c1") {
			t.Fatalf("%s on the hosting member: %d %s", name, code, body)
		}
		// A member without managed workloads is not blocked by the guard.
		if code, body = call(fn, "ha1", "/api/cluster/members/ha1/"+name, `{}`); !strings.Contains(body, "not_clustered") {
			t.Fatalf("%s on a free member: %d %s", name, code, body)
		}
		// force=true goes through (here it stops at not_clustered) and is audited.
		if code, body = call(fn, "ha2", "/api/cluster/members/ha2/"+name+"?force=true", `{}`); !strings.Contains(body, "not_clustered") {
			t.Fatalf("%s forced: %d %s", name, code, body)
		}
		entries, _, _ := al.List(audit.ListOptions{Action: "cluster.member_" + name + ".forced"}, 10, 0)
		if len(entries) != 1 {
			t.Fatalf("%s: forced override must be audited once, got %d", name, len(entries))
		}
	}
	if code, body := call(h.EvacuateClusterMember, "ha2", "/api/cluster/members/ha2/evacuate", `{"force":true}`); !strings.Contains(body, "not_clustered") {
		t.Fatalf("force in body: %d %s", code, body)
	}
	// Module off: no guard.
	off := &Handler{}
	if code, body := call(off.EvacuateClusterMember, "ha2", "/api/cluster/members/ha2/evacuate", `{}`); !strings.Contains(body, "not_clustered") {
		t.Fatalf("module off: %d %s", code, body)
	}
}
