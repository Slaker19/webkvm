package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

func TestBuildDrainPlan(t *testing.T) {
	vms := []models.VM{
		{ID: "run", Name: "running", State: models.VMStateRunning},
		{ID: "off", Name: "stopped", State: models.VMStateShutoff},
		{ID: "ha", Name: "ha", State: models.VMStateRunning, ManagedBy: "pacemaker"},
		{ID: "ct", Name: "container", State: models.VMStateRunning, Type: "container", Hypervisor: "incus"},
		{ID: "gpu", Name: "gpu", State: models.VMStateRunning, GPU: true},
		{ID: "stale", Name: "stale", State: models.VMStateRunning, Stale: true},
	}
	plan := buildDrainPlan(vms)
	if len(plan) != len(vms) {
		t.Fatalf("plan rows = %d, want %d", len(plan), len(vms))
	}
	byID := map[string]nodeDrainVM{}
	for _, p := range plan {
		byID[p.ID] = p
	}
	if !byID["run"].Migratable || byID["run"].Action != "live_migrate" {
		t.Errorf("running = %+v", byID["run"])
	}
	if !byID["off"].Migratable || byID["off"].Action != "offline_shared_storage" {
		t.Errorf("stopped = %+v", byID["off"])
	}
	for id, reason := range map[string]string{
		"ha": "pacemaker_managed", "ct": "incus_instance", "gpu": "gpu_passthrough", "stale": "stale_listing",
	} {
		if byID[id].Migratable || byID[id].Reason != reason {
			t.Errorf("%s = %+v, want skip %s", id, byID[id], reason)
		}
	}
}

func TestDrainAndUndrainNodeAPI(t *testing.T) {
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	n, err := reg.Create("remote-drain", "qemu+ssh://root@10.0.0.9/system")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{nodes: reg}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/"+n.ID+"/drain", nil)
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", "admin")
	h.DrainNode(rec, withID(req, n.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("drain code = %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := reg.Get(n.ID)
	if !got.Draining {
		t.Fatal("node was not marked draining")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/nodes/"+n.ID+"/undrain", nil)
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", "admin")
	h.UndrainNode(rec, withID(req, n.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("undrain code = %d: %s", rec.Code, rec.Body.String())
	}
	got, _ = reg.Get(n.ID)
	if got.Draining {
		t.Fatal("node is still draining")
	}
}

func TestRejectDrainingPlacement(t *testing.T) {
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	var local nodes.Node
	for _, n := range reg.List() {
		if n.IsLocal() {
			local = n
		}
	}
	if local.ID == "" {
		t.Fatal("local node missing")
	}
	if _, err := reg.SetDraining(local.ID, true, "admin"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{nodes: reg}
	rec := httptest.NewRecorder()
	if !h.rejectDrainingPlacement(rec, "") {
		t.Fatal("local placement should be rejected while draining")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "node_draining" {
		t.Fatalf("code = %v, want node_draining", body["code"])
	}
}

func TestDrainTargetsExcludeDrainingAndUnsupported(t *testing.T) {
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	ok, _ := reg.Create("ok", "qemu+ssh://root@10.0.0.9/system")
	busy, _ := reg.Create("busy", "qemu+ssh://root@10.0.0.10/system")
	incus, _ := reg.CreateTyped("incus", "https://incus.example:8443", nodes.BackendIncus, "", "", "")
	if _, err := reg.SetDraining(busy.ID, true, "admin"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{nodes: reg}
	var src nodes.Node
	for _, n := range reg.List() {
		if n.IsLocal() {
			src = n
		}
	}
	targets := h.drainTargets(src)
	var ids []string
	for _, n := range targets {
		ids = append(ids, n.ID)
	}
	if len(ids) != 1 || ids[0] != ok.ID {
		t.Fatalf("targets = %v, want [%s] (busy=%s incus=%s)", ids, ok.ID, busy.ID, incus.ID)
	}
}
