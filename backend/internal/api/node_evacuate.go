package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/nodes"
)

// Evacuation runs as a background job (submitJob) instead of blocking the
// HTTP request: live migrations outlast any reasonable client timeout and
// used to die with the request. The record below is persisted to
// <dataDir>/evacuations.json after every per-VM event so a WebKVM restart
// leaves an auditable trail (status "interrupted") and re-running the
// evacuation resumes the remaining VMs: successfully migrated VMs are no
// longer on the source node, so re-listing skips them naturally.
type evacuationRecord struct {
	NodeID     string               `json:"node_id"`
	NodeName   string               `json:"node_name"`
	TargetName string               `json:"target_name,omitempty"`
	Status     string               `json:"status"` // running|completed|partial|cancelled|interrupted
	StartedBy  string               `json:"started_by,omitempty"`
	StartedAt  int64                `json:"started_at"`
	UpdatedAt  int64                `json:"updated_at"`
	JobID      string               `json:"job_id,omitempty"`
	Migrated   []nodeEvacuateResult `json:"migrated,omitempty"`
	Failed     []nodeEvacuateResult `json:"failed,omitempty"`
	Skipped    []nodeEvacuateResult `json:"skipped,omitempty"`
	Note       string               `json:"note,omitempty"`
}

// domainMigrator is the seam the evacuation loop runs through so tests can
// inject a fake without libvirt connections; *libvirt.Pool satisfies it.
type domainMigrator interface {
	MigrateDomain(ctx context.Context, src, dst nodes.Node, id string, live, sharedStorage bool) error
}

type evacuationManager struct {
	mu      sync.Mutex
	path    string // "" keeps the state in memory only (unit tests)
	records map[string]*evacuationRecord
	cancels map[string]context.CancelFunc
}

func newEvacuationManager(dataDir string) *evacuationManager {
	m := &evacuationManager{
		path:    filepath.Join(dataDir, "evacuations.json"),
		records: make(map[string]*evacuationRecord),
		cancels: make(map[string]context.CancelFunc),
	}
	m.loadInterrupted()
	return m
}

// loadInterrupted marks evacuation records that were "running" when the
// process last stopped: they are not running anymore, and leaving them
// "running" would block a new evacuation forever (single-flight guard).
func (m *evacuationManager) loadInterrupted() {
	if m.path == "" {
		return
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	var recs []*evacuationRecord
	if json.Unmarshal(data, &recs) != nil {
		return // fail closed: ignore a corrupt file rather than crash
	}
	changed := false
	for _, rec := range recs {
		if rec.Status == "running" {
			rec.Status = "interrupted"
			rec.Note = "webkvm_restarted_running"
			rec.UpdatedAt = time.Now().Unix()
			changed = true
		}
		m.records[rec.NodeID] = rec
	}
	if changed {
		m.save()
	}
}

// save writes the whole record set atomically; the caller holds m.mu.
func (m *evacuationManager) save() {
	if m.path == "" {
		return
	}
	recs := make([]*evacuationRecord, 0, len(m.records))
	for _, rec := range m.records {
		recs = append(recs, rec)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, m.path)
	}
}

func (m *evacuationManager) get(nodeID string) (evacuationRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[nodeID]
	if !ok {
		return evacuationRecord{}, false
	}
	return *rec, true
}

func (m *evacuationManager) isRunning(nodeID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[nodeID]
	return ok && rec.Status == "running"
}

// start claims the single-flight slot for a node; false when an evacuation
// is already running there.
func (m *evacuationManager) start(rec *evacuationRecord) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.records[rec.NodeID]; ok && prev.Status == "running" {
		return false
	}
	now := time.Now().Unix()
	rec.StartedAt = now
	rec.UpdatedAt = now
	m.records[rec.NodeID] = rec
	m.save()
	return true
}

func (m *evacuationManager) setCancel(nodeID string, cancel context.CancelFunc) {
	m.mu.Lock()
	m.cancels[nodeID] = cancel
	m.mu.Unlock()
}

func (m *evacuationManager) setJobID(nodeID, jobID string) {
	m.mu.Lock()
	if rec, ok := m.records[nodeID]; ok {
		rec.JobID = jobID
		m.save()
	}
	m.mu.Unlock()
}

// append records one per-VM outcome, persists it and mirrors the record
// into the live job so Task Center polling sees progress move.
func (m *evacuationManager) append(nodeID, kind string, row nodeEvacuateResult) {
	m.mu.Lock()
	rec, ok := m.records[nodeID]
	if !ok {
		m.mu.Unlock()
		return
	}
	rec.UpdatedAt = time.Now().Unix()
	switch kind {
	case "migrated":
		rec.Migrated = append(rec.Migrated, row)
	case "failed":
		rec.Failed = append(rec.Failed, row)
	default:
		rec.Skipped = append(rec.Skipped, row)
	}
	jobID := rec.JobID
	snapshot := *rec
	m.save()
	m.mu.Unlock()
	m.publish(jobID, snapshot, 0, 0)
}

// publish updates the live job entry (Result and, optionally, progress)
// under jobsMu. The record copy avoids racing on the shared struct.
func (m *evacuationManager) publish(jobID string, rec evacuationRecord, done, total int) {
	if jobID == "" {
		return
	}
	jobsMu.Lock()
	defer jobsMu.Unlock()
	j, ok := isoJobs[jobID]
	if !ok {
		return
	}
	j.UpdatedAt = time.Now().Unix()
	j.Result = rec
	if total > 0 {
		j.Progress = float64(done) / float64(total) * 100
		j.Message = fmt.Sprintf("%d/%d", done, total)
	}
}

// finish marks the record terminal and releases the cancel hook.
func (m *evacuationManager) finish(nodeID, status, note string) {
	m.mu.Lock()
	rec, ok := m.records[nodeID]
	if !ok {
		m.mu.Unlock()
		return
	}
	rec.Status = status
	rec.Note = note
	rec.UpdatedAt = time.Now().Unix()
	jobID := rec.JobID
	snapshot := *rec
	m.save()
	delete(m.cancels, nodeID)
	m.mu.Unlock()
	m.publish(jobID, snapshot, 0, 0)
}

// cancel asks a running evacuation to stop starting new migrations; the
// migration already in flight is allowed to finish. False when the node
// has no running evacuation.
func (m *evacuationManager) cancel(nodeID string) bool {
	m.mu.Lock()
	cancel := m.cancels[nodeID]
	m.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// evacuationManager lazily builds the manager on first use; prod handlers
// always carry cfg, unit tests may not (memory-only state then). Guarded
// by a package mutex instead of sync.Once so Handler value copies stay
// vet-clean.
var evacInitMu sync.Mutex

func (h *Handler) evacuationManager() *evacuationManager {
	if h.evac != nil {
		return h.evac
	}
	evacInitMu.Lock()
	defer evacInitMu.Unlock()
	if h.evac == nil {
		dir := ""
		if h.cfg != nil {
			dir = h.cfg.DataDir
		}
		h.evac = newEvacuationManager(dir)
	}
	return h.evac
}

// evacMigrator returns the migration seam: the test override when set,
// otherwise the real libvirt pool (nil when remote connections are down).
func (h *Handler) evacMigrator() domainMigrator {
	if h.evacMigratorOverride != nil {
		return h.evacMigratorOverride
	}
	if h.libvirtPool == nil {
		return nil
	}
	return h.libvirtPool
}

// schedScope returns the node scope for per-VM local state (schedules):
// a remote dispatch resolves to that node's ID, the local handler to the
// local node's ID, and a bare "local" fallback keeps unit tests working.
func (h *Handler) schedScope() string {
	if h.isRemote() {
		return h.remote.node.ID
	}
	if h.nodes != nil {
		for _, n := range h.nodes.List() {
			if n.IsLocal() {
				return n.ID
			}
		}
	}
	return "local"
}

// GetNodeEvacuation returns the latest evacuation record for a node.// GET /api/nodes/{id}/evacuate
func (h *Handler) GetNodeEvacuation(w http.ResponseWriter, r *http.Request) {
	n, ok := h.drainNodeFromRequest(r)
	if !ok {
		jsonErrCode(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	rec, ok := h.evacuationManager().get(n.ID)
	if !ok {
		jsonErrCode(w, http.StatusNotFound, "no_evacuation", "no evacuation recorded for this node")
		return
	}
	jsonResp(w, http.StatusOK, rec)
}

// CancelNodeEvacuation stops a running evacuation: no new migrations are
// started and the record is closed as "cancelled" once the in-flight one
// settles. POST /api/nodes/{id}/evacuate/cancel
func (h *Handler) CancelNodeEvacuation(w http.ResponseWriter, r *http.Request) {
	n, ok := h.drainNodeFromRequest(r)
	if !ok {
		jsonErrCode(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if !h.evacuationManager().cancel(n.ID) {
		jsonErrCode(w, http.StatusConflict, "evacuation_not_running", "no evacuation is running for this node")
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "node.evacuate_cancel", n.ID, map[string]any{"name": n.Name}))
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

// runEvacuation is the migration loop for one evacuation. Runs in a
// background job goroutine; the HTTP request that triggered it is gone.
func (h *Handler) runEvacuation(em *evacuationManager, stopCtx context.Context, mig domainMigrator, src, dst nodes.Node, plan nodeDrainPlan, sharedStorage bool, total int) (any, error) {
	cancelled, failed := false, false
	done := 0
	for _, vm := range plan.VMs {
		row := nodeEvacuateResult{ID: vm.ID, Name: vm.Name}
		if !vm.Migratable {
			row.Status = "skipped"
			row.Error = vm.Reason
			em.append(src.ID, "skipped", row)
			continue
		}
		if stopCtx.Err() != nil {
			// Only stop starting new migrations; one already in flight
			// keeps going to completion, never killed mid-copy.
			cancelled = true
			row.Status = "cancelled"
			row.Error = "evacuation_cancelled"
			em.append(src.ID, "skipped", row)
			continue
		}
		live := vm.Action == "live_migrate"
		shared := sharedStorage
		if vm.Action == "offline_shared_storage" {
			if !sharedStorage {
				row.Status = "skipped"
				row.Error = "shared_storage_required"
				em.append(src.ID, "skipped", row)
				continue
			}
			live = false
		}
		// Per-VM timeout on a context that outlives a cancel request:
		// WithoutCancel drops the gate's cancellation but keeps values.
		mctx, mcancel := context.WithTimeout(context.WithoutCancel(stopCtx), 30*time.Minute)
		err := mig.MigrateDomain(mctx, src, dst, vm.ID, live, shared)
		mcancel()
		done++
		if err != nil {
			failed = true
			row.Status = "failed"
			row.Error = err.Error()
			em.append(src.ID, "failed", row)
			addJobLog(em.jobIDFor(src.ID), fmt.Sprintf("failed %s (%s): %s", vm.Name, vm.ID, err.Error()))
			continue
		}
		row.Status = "migrated"
		em.append(src.ID, "migrated", row)
		addJobLog(em.jobIDFor(src.ID), fmt.Sprintf("migrated %s (%s) to %s", vm.Name, vm.ID, dst.Name))
		if h.audit != nil {
			h.audit.Log(audit.Entry{
				Action: "vm.migrate", Resource: vm.ID, Detail: map[string]any{
					"from_node": src.Name, "to_node": dst.Name, "live": live, "shared_storage": shared,
				},
			})
		}
	}
	em.publish(em.jobIDFor(src.ID), em.mustGet(src.ID), done, total)
	status := "completed"
	switch {
	case cancelled:
		status = "cancelled"
	case failed:
		status = "partial"
	}
	note := ""
	if cancelled {
		note = "cancelled_by_user"
	}
	em.finish(src.ID, status, note)
	rec, _ := em.get(src.ID)
	return rec, nil
}

// jobIDFor reads the recorded job ID for a node; helper keeps the loop
// readable without exposing the record map.
func (m *evacuationManager) jobIDFor(nodeID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.records[nodeID]; ok {
		return rec.JobID
	}
	return ""
}

func (m *evacuationManager) mustGet(nodeID string) evacuationRecord {
	rec, _ := m.get(nodeID)
	return rec
}
