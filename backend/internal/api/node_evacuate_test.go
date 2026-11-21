package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"webkvm/internal/config"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// fakeMigrator records MigrateDomain calls so evacuation tests can assert
// exactly which VMs were attempted, without libvirt connections.
type fakeMigrator struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
	block chan struct{} // when non-nil, every migration waits on it
}

func (f *fakeMigrator) MigrateDomain(ctx context.Context, src, dst nodes.Node, id string, live, sharedStorage bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, id)
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err, ok := f.fail[id]; ok {
		return err
	}
	return nil
}

func (f *fakeMigrator) migrated() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func evacuateFixture(t *testing.T, vms []models.VM, mig *fakeMigrator) (*Handler, nodes.Node, nodes.Node) {
	t.Helper()
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	src, err := reg.Create("src-node", "qemu+ssh://root@10.0.0.9/system")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := reg.Create("dst-node", "qemu+ssh://root@10.0.0.10/system")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		nodes:                reg,
		cfg:                  &config.Config{DataDir: t.TempDir()},
		evacMigratorOverride: mig,
	}
	h.evacVMSeam = func(ctx context.Context, uri string) ([]models.VM, error) { return vms, nil }
	if _, err := reg.SetDraining(src.ID, true, "admin"); err != nil {
		t.Fatal(err)
	}
	return h, src, dst
}

// evacuateRequest runs the endpoint and always closes the response body.
func evacuateRequest(h *Handler, node nodes.Node, body string) (int, map[string]any) {
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/"+node.ID+"/evacuate", payload)
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", "admin")
	h.EvacuateNode(rec, withID(req, node.ID))
	code := rec.Code
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return code, out
}

// TestEvacuateNodeAsyncSuccess: the endpoint returns 202 with a job id, the
// loop migrates every migratable VM, the record ends completed and the
// per-VM outcomes are persisted to <dataDir>/evacuations.json.
func TestEvacuateNodeAsyncSuccess(t *testing.T) {
	vms := []models.VM{
		{ID: "vm-run", Name: "running", State: models.VMStateRunning},
		{ID: "vm-ha", Name: "ha", State: models.VMStateRunning, ManagedBy: "pacemaker"},
	}
	mig := &fakeMigrator{}
	h, src, dst := evacuateFixture(t, vms, mig)

	code, start := evacuateRequest(h, src, `{"target":"`+dst.Name+`"}`)
	if code != http.StatusAccepted {
		t.Fatalf("evacuate code = %d, want 202: %v", code, start)
	}
	if jobID, _ := start["job"].(string); jobID == "" {
		t.Fatal("no job id in 202 response")
	}

	em := h.evacuationManager()
	var rec evacuationRecord
	waitFor(t, func() bool {
		rec, _ = em.get(src.ID)
		return rec.Status != "running" && rec.Status != ""
	})
	if rec.Status != "completed" {
		t.Fatalf("status = %s (failed=%v skipped=%v)", rec.Status, rec.Failed, rec.Skipped)
	}
	if len(rec.Migrated) != 1 || rec.Migrated[0].ID != "vm-run" {
		t.Fatalf("migrated = %+v", rec.Migrated)
	}
	if got := mig.migrated(); len(got) != 1 || got[0] != "vm-run" {
		t.Fatalf("migrator calls = %v", got)
	}
	skipped := map[string]string{}
	for _, row := range rec.Skipped {
		skipped[row.ID] = row.Error
	}
	if skipped["vm-ha"] != "pacemaker_managed" {
		t.Fatalf("pacemaker VM skip missing: %+v", rec.Skipped)
	}

	data, err := os.ReadFile(filepath.Join(h.cfg.DataDir, "evacuations.json"))
	if err != nil {
		t.Fatalf("record not persisted: %v", err)
	}
	var recs []evacuationRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Status != "completed" {
		t.Fatalf("persisted = %+v", recs)
	}
}

// TestEvacuateNodePartial: a failed migration leaves the record in
// "partial" with the per-VM error, and the job is not marked done.
func TestEvacuateNodePartial(t *testing.T) {
	vms := []models.VM{
		{ID: "vm-ok", Name: "ok", State: models.VMStateRunning},
		{ID: "vm-bad", Name: "bad", State: models.VMStateRunning},
	}
	mig := &fakeMigrator{fail: map[string]error{"vm-bad": io.EOF}}
	h, src, dst := evacuateFixture(t, vms, mig)

	if code, _ := evacuateRequest(h, src, `{"target":"`+dst.Name+`"}`); code != http.StatusAccepted {
		t.Fatalf("evacuate code = %d", code)
	}
	em := h.evacuationManager()
	var rec evacuationRecord
	waitFor(t, func() bool {
		rec, _ = em.get(src.ID)
		return rec.Status != "running" && rec.Status != ""
	})
	if rec.Status != "partial" {
		t.Fatalf("status = %s", rec.Status)
	}
	if len(rec.Failed) != 1 || rec.Failed[0].ID != "vm-bad" || rec.Failed[0].Error == "" {
		t.Fatalf("failed = %+v", rec.Failed)
	}
	if len(rec.Migrated) != 1 {
		t.Fatalf("migrated = %+v", rec.Migrated)
	}
}

// TestEvacuateNodeNotDraining: evacuation without drain mode is rejected.
func TestEvacuateNodeNotDraining(t *testing.T) {
	mig := &fakeMigrator{}
	h, src, dst := evacuateFixture(t, nil, mig)
	if _, err := h.nodes.SetDraining(src.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	if code, _ := evacuateRequest(h, src, `{"target":"`+dst.Name+`"}`); code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", code)
	}
	if len(mig.migrated()) != 0 {
		t.Fatalf("migrator was called: %v", mig.migrated())
	}
}

// TestEvacuateNodeSingleFlight: a second evacuate while one is running gets
// 409 instead of racing the first loop; cancel stops the loop from starting
// new migrations while letting the in-flight one finish.
func TestEvacuateNodeSingleFlight(t *testing.T) {
	vms := []models.VM{
		{ID: "vm-a", Name: "a", State: models.VMStateRunning},
		{ID: "vm-b", Name: "b", State: models.VMStateRunning},
	}
	block := make(chan struct{})
	mig := &fakeMigrator{block: block}
	h, src, dst := evacuateFixture(t, vms, mig)

	if code, _ := evacuateRequest(h, src, `{"target":"`+dst.Name+`"}`); code != http.StatusAccepted {
		t.Fatalf("first evacuate code = %d", code)
	}
	em := h.evacuationManager()
	waitFor(t, func() bool { return len(mig.migrated()) == 1 })

	if code, _ := evacuateRequest(h, src, `{"target":"`+dst.Name+`"}`); code != http.StatusConflict {
		t.Fatalf("second evacuate code = %d, want 409", code)
	}

	// Cancel, release the in-flight migration and confirm the remaining
	// VM was NOT migrated and the record closed as cancelled.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/"+src.ID+"/evacuate/cancel", nil)
	req.Header.Set("X-User", "admin")
	h.CancelNodeEvacuation(rec, withID(req, src.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel code = %d", rec.Code)
	}
	close(block)
	var final evacuationRecord
	waitFor(t, func() bool {
		final, _ = em.get(src.ID)
		return final.Status != "running"
	})
	if final.Status != "cancelled" {
		t.Fatalf("status = %s, want cancelled", final.Status)
	}
	if len(final.Migrated) != 1 || final.Migrated[0].ID != "vm-a" {
		t.Fatalf("migrated = %+v", final.Migrated)
	}
	if got := mig.migrated(); len(got) != 1 {
		t.Fatalf("cancel started a new migration: %v", got)
	}
	for _, row := range final.Skipped {
		if row.ID == "vm-b" && row.Error != "evacuation_cancelled" {
			t.Fatalf("pending VM row = %+v", row)
		}
	}
}

// TestEvacuationResumeSkipsMigrated: after a completed evacuation the
// migrated VMs are off the node, so a re-run only attempts the rest.
func TestEvacuationResumeSkipsMigrated(t *testing.T) {
	vms := []models.VM{
		{ID: "vm-left", Name: "left", State: models.VMStateRunning},
		{ID: "vm-stay", Name: "stay", State: models.VMStateRunning},
	}
	mig := &fakeMigrator{fail: map[string]error{"vm-stay": io.EOF}}
	h, src, dst := evacuateFixture(t, vms, mig)
	if code, _ := evacuateRequest(h, src, `{"target":"`+dst.Name+`"}`); code != http.StatusAccepted {
		t.Fatalf("code = %d", code)
	}
	em := h.evacuationManager()
	waitFor(t, func() bool {
		rec, _ := em.get(src.ID)
		return rec.Status == "partial"
	})

	// Resume: vm-left is gone from the node; only vm-stay is attempted.
	h.evacVMSeam = func(ctx context.Context, uri string) ([]models.VM, error) {
		return []models.VM{{ID: "vm-stay", Name: "stay", State: models.VMStateRunning}}, nil
	}
	if code, _ := evacuateRequest(h, src, `{"target":"`+dst.Name+`"}`); code != http.StatusAccepted {
		t.Fatalf("resume code = %d", code)
	}
	waitFor(t, func() bool {
		rec, _ := em.get(src.ID)
		return rec.Status != "running"
	})
	var stayTries, leftTries int
	for _, c := range mig.migrated() {
		switch c {
		case "vm-stay":
			stayTries++
		case "vm-left":
			leftTries++
		}
	}
	if stayTries != 2 || leftTries != 1 {
		t.Fatalf("attempts stay=%d left=%d (%v), want stay=2 left=1", stayTries, leftTries, mig.migrated())
	}
}

// TestEvacuationInterruptedOnRestart: a record persisted as "running" is
// re-marked interrupted when a new manager loads the file, so the
// single-flight guard does not deadlock the node forever.
func TestEvacuationInterruptedOnRestart(t *testing.T) {
	dir := t.TempDir()
	old := []evacuationRecord{{NodeID: "node-1", Status: "running", StartedAt: 1, UpdatedAt: 1}}
	data, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, "evacuations.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	m := newEvacuationManager(dir)
	rec, ok := m.get("node-1")
	if !ok || rec.Status != "interrupted" {
		t.Fatalf("loaded = %+v ok=%v, want interrupted", rec, ok)
	}
	if m.isRunning("node-1") {
		t.Fatal("interrupted record still blocks new evacuations")
	}
	if !m.start(&evacuationRecord{NodeID: "node-1", Status: "running"}) {
		t.Fatal("new evacuation could not start after restart")
	}
}

// TestEvacuationCorruptFileFailClosed: a corrupt persistence file must not
// crash the manager or resurrect running state.
func TestEvacuationCorruptFileFailClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "evacuations.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newEvacuationManager(dir)
	if m.isRunning("anything") {
		t.Fatal("corrupt file produced running state")
	}
	if !m.start(&evacuationRecord{NodeID: "node-1", Status: "running"}) {
		t.Fatal("start failed after corrupt file")
	}
}
