package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/config"
	"webkvm/internal/ha"
	"webkvm/internal/models"
)

// A refused HA enable after creation must keep the VM and say why, not fail
// the create: the instance already exists and the operator can retry.
func TestCreatedVMHAFailureKeepsVM(t *testing.T) {
	r := &wlRunner{}
	setupWL(t, r, goodFacts(), false)
	h := &Handler{cfg: &config.Config{DataDir: t.TempDir()}}
	resp := map[string]any{"id": "web", "name": "web"}
	req := httptest.NewRequest("POST", "/api/vms", nil)

	h.addCreatedHAResult(req, models.VM{Name: "web", Hypervisor: "kvm"}, &models.CreateHAOptions{}, resp)

	if resp["ha_warning"] == nil || resp["ha_warning"] == "" {
		t.Fatalf("expected ha_warning, got %v", resp)
	}
	if _, ok := resp["ha"]; ok {
		t.Fatalf("ha must not be reported as enabled: %v", resp)
	}
	if resp["id"] != "web" || resp["name"] != "web" {
		t.Fatalf("created VM fields changed: %v", resp)
	}
	if r.has("cib-push") {
		t.Fatal("refused HA must not touch the CIB")
	}
}

func TestCreatedVMWithoutHAOptionsSkipsHA(t *testing.T) {
	h := &Handler{}
	resp := map[string]any{}
	h.addCreatedHAResult(httptest.NewRequest("POST", "/", nil), models.VM{Name: "web"}, nil, resp)
	if len(resp) != 0 {
		t.Fatalf("no HA requested, response must not change: %v", resp)
	}
}

// HA requested for a VM created on a remote node must be refused up front,
// not silently dropped after the VM exists elsewhere.
func TestCreateVMHARejectedForRemoteNode(t *testing.T) {
	h := &Handler{}
	body := bytes.NewReader([]byte(`{"name":"remote-ha","node":"remote-kvm","ha":{}}`))
	req := httptest.NewRequest(http.MethodPost, "/api/vms", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	rec := httptest.NewRecorder()

	h.CreateVM(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestHAWorkloadKeyForInstanceKind(t *testing.T) {
	kvm := haWorkloadKeyFor(models.VM{Name: "web", Hypervisor: "kvm"})
	if kvm.Kind != ha.KindKVM || kvm.Name != "web" || kvm.Project != "" {
		t.Fatalf("kvm key = %+v", kvm)
	}
	incus := haWorkloadKeyFor(models.VM{Name: "c1", Hypervisor: "incus"})
	if incus.Kind != ha.KindIncus || incus.Name != "c1" || incus.Project != "default" {
		t.Fatalf("incus key = %+v", incus)
	}
}

func TestCreatedVMHASuccessSetsFlag(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true}
	h, _ := setupWLHandler(t, r, goodFacts(), true)
	resp := map[string]any{"id": "web", "name": "web"}

	h.addCreatedHAResult(httptest.NewRequest("POST", "/", nil),
		models.VM{Name: "web", Hypervisor: "kvm"}, &models.CreateHAOptions{}, resp)

	if resp["ha"] != true {
		t.Fatalf("ha flag not set: %v", resp)
	}
	if _, ok := resp["ha_warning"]; ok {
		t.Fatalf("unexpected warning on success: %v", resp)
	}
	if !r.has("cib-push") {
		t.Fatal("HA enable did not reach the CIB")
	}
}
