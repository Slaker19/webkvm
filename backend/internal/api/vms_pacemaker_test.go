package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webkvm/internal/configstore"
	"webkvm/internal/models"
	"webkvm/internal/sysutil"

	"github.com/go-chi/chi/v5"
)

func mockPacemakerDomain(t *testing.T, name string) *sysutil.MockCommandRunner {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".xml")
	if err := os.WriteFile(path, []byte("<domain><name>"+name+"</name></domain>"), 0o600); err != nil {
		t.Fatal(err)
	}
	cib := `<cib><configuration><resources><group id="kvm-ha"><primitive id="kvm-vm" class="ocf" provider="heartbeat" type="VirtualDomain"><instance_attributes><nvpair name="config" value="` + path + `"/></instance_attributes></primitive></group></resources></configuration></cib>`
	status := `<pacemaker-result api-version="2.15"><status code="0" message="OK"/><crm_mon version="3.0.1"><summary/><nodes><node name="node-a" online="true"/><node name="node-b" online="true"/></nodes><resources><resource id="kvm-vm" resource_agent="ocf:heartbeat:VirtualDomain" role="Started" active="true"><node name="node-a"/></resource></resources></crm_mon></pacemaker-result>`
	return &sysutil.MockCommandRunner{Handler: func(_ context.Context, executable string, args ...string) ([]byte, error) {
		if executable != "pcs" {
			t.Fatalf("unexpected executable %s %v", executable, args)
		}
		switch strings.Join(args, " ") {
		case "cluster cib":
			return []byte(cib), nil
		case "status xml":
			return []byte(status), nil
		}
		t.Fatalf("unexpected command %s %v", executable, args)
		return nil, nil
	}}
}

func TestApplyPacemakerManagementMarksOnlyMatchingLocalKVM(t *testing.T) {
	previous := sysutil.DefaultRunner
	t.Cleanup(func() { sysutil.DefaultRunner = previous })
	sysutil.DefaultRunner = mockPacemakerDomain(t, "ha-test")

	vms := []models.VM{
		{Name: "ha-test", Hypervisor: "kvm"},
		{Name: "ha-test", Hypervisor: "incus"},
		{Name: "other", Hypervisor: "kvm"},
		{Name: "ha-test", Hypervisor: "kvm", Node: "remote"},
	}
	applyPacemakerManagement(context.Background(), vms)
	if vms[0].ManagedBy != "pacemaker" {
		t.Fatalf("local pacemaker VM not marked: %+v", vms[0])
	}
	if vms[1].ManagedBy != "" || vms[2].ManagedBy != "" || vms[3].ManagedBy != "" {
		t.Fatalf("unrelated or remote VMs must stay writable: %+v", vms)
	}
}

func TestRejectPacemakerManagedReturnsConflict(t *testing.T) {
	previous := sysutil.DefaultRunner
	t.Cleanup(func() { sysutil.DefaultRunner = previous })
	sysutil.DefaultRunner = mockPacemakerDomain(t, "ha-test")

	request := httptest.NewRequest(http.MethodPost, "/api/vms/ha-test/start", nil)

	// With the optional module off there is no pcs probe and no rejection.
	if (&Handler{}).rejectPacemakerManaged(httptest.NewRecorder(), request, "ha-test") {
		t.Fatal("module off must not reject anything")
	}

	settings, err := configstore.New(t.TempDir(), configstore.DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	if _, failed, err := settings.SetMany(map[string]any{"modules.cluster_ha": true}); err != nil || len(failed) > 0 {
		t.Fatalf("enable module: %v %v", err, failed)
	}
	handler := &Handler{settings: settings}

	recorder := httptest.NewRecorder()
	if !handler.rejectPacemakerManaged(recorder, request, "ha-test") {
		t.Fatal("pacemaker VM must be rejected")
	}
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "pacemaker_managed") {
		t.Fatalf("unexpected rejection: %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	if handler.rejectPacemakerManaged(recorder, request, "other-vm") {
		t.Fatal("unrelated VM must not be rejected")
	}
}

// The node of a Pacemaker-managed domain comes from crm_mon/pcs, not from the
// WebKVM instance serving the request.
func TestApplyPacemakerManagementUsesRunningNode(t *testing.T) {
	previous := sysutil.DefaultRunner
	t.Cleanup(func() { sysutil.DefaultRunner = previous })
	sysutil.DefaultRunner = mockPacemakerDomain(t, "ha-test")

	vms := []models.VM{{Name: "ha-test", Hypervisor: "kvm", Node: "local"}}
	applyPacemakerManagement(context.Background(), vms)
	if vms[0].ManagedBy != "pacemaker" {
		t.Fatalf("not marked as Pacemaker-managed: %+v", vms[0])
	}
	// M23: the routing key stays untouched; the crm_mon location is exposed
	// separately so GetVM/operatorFor never see a name missing from the registry.
	if vms[0].Node != "local" {
		t.Fatalf("node = %q, the routing key must not be overwritten", vms[0].Node)
	}
	if vms[0].PacemakerNode != "node-a" {
		t.Fatalf("pacemaker_node = %q, want the crm_mon node node-a", vms[0].PacemakerNode)
	}
	data, _ := json.Marshal(vms[0])
	if !strings.Contains(string(data), `"pacemaker_node":"node-a"`) {
		t.Fatalf("pacemaker_node missing from JSON: %s", data)
	}
	plain, _ := json.Marshal(models.VM{Name: "x"})
	if strings.Contains(string(plain), "pacemaker_node") {
		t.Fatalf("pacemaker_node must be omitted when unmanaged: %s", plain)
	}
}

// Migration is a lifecycle operation too: moving a Pacemaker-managed domain
// behind the cluster's back leaves two managers fighting over the same disk.
func TestMigrateVMRejectsPacemakerManaged(t *testing.T) {
	previous := sysutil.DefaultRunner
	t.Cleanup(func() { sysutil.DefaultRunner = previous })
	sysutil.DefaultRunner = mockPacemakerDomain(t, "ha-test")

	settings, err := configstore.New(t.TempDir(), configstore.DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	if _, failed, err := settings.SetMany(map[string]any{"modules.cluster_ha": true}); err != nil || len(failed) > 0 {
		t.Fatalf("enable module: %v %v", err, failed)
	}
	handler := &Handler{settings: settings}

	request := httptest.NewRequest(http.MethodPost, "/api/vms/ha-test/migrate", strings.NewReader(`{"node":"other","live":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Role", models.RoleAdmin)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "ha-test")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, rctx))

	recorder := httptest.NewRecorder()
	handler.MigrateVM(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "pacemaker_managed") {
		t.Fatalf("migrate of a Pacemaker VM: %d %s, want 409 pacemaker_managed", recorder.Code, recorder.Body.String())
	}
}
