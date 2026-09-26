package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"webkvm/internal/backupstore"
)

// Per-VM backups for non-admins.
//
// The /api/backup/* surface is admin-only and stays that way: a target
// definition carries the hostnames, paths and usernames of every remote
// destination in the fleet, and a run against one backs up whatever that
// target's filter selects. Handing an operator that surface would leak
// infrastructure detail and let them trigger fleet-wide runs.
//
// These endpoints are the narrow version of the same capability: they
// operate on one VM the caller already has access to, they never expose
// a target's connection details, and the run scope is forced server-side
// to that single VM regardless of how the target is configured.

// vmBackupTarget is the redacted projection of a backup target shown to
// non-admins: enough to choose a destination, nothing about how to reach
// it. Host, port, username, bucket, region, endpoint and path are all
// deliberately absent.
type vmBackupTarget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// ListVMBackupTargets returns the destinations available for backing up
// a single VM. Disabled targets are omitted: offering a destination that
// can only fail is worse than not offering it.
func (h *Handler) ListVMBackupTargets(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	var out []vmBackupTarget
	for _, t := range h.backupStore.ListTargets() {
		if !t.Enabled {
			continue
		}
		out = append(out, vmBackupTarget{ID: t.ID, Name: t.Name, Type: string(t.Type)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if out == nil {
		out = []vmBackupTarget{}
	}
	jsonResp(w, http.StatusOK, map[string]any{"targets": out})
}

// vmBackupRunRequest is the body of POST /api/vms/{id}/backup.
type vmBackupRunRequest struct {
	TargetID string `json:"target_id"`
}

// BackupVMNow backs up a single VM.
//
// Authorization is layered and every layer is required: the route sits
// behind requireVMOwnership (may the caller touch this VM at all) and
// requireCapability("backups") (may the caller run backups at all). The
// scope is then pinned to this one VM by the runner, so the target's own
// filter cannot widen the run to VMs the caller cannot see.
func (h *Handler) BackupVMNow(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil || h.backupRunner == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup subsystem not initialized")
		return
	}
	vmID := chiURLParam(r, "id")
	if vmID == "" {
		jsonErr(w, http.StatusBadRequest, "vm id is required")
		return
	}

	var req vmBackupRunRequest
	if r.Body != nil {
		// An absent or empty body means "use the default target", which
		// matches the admin endpoint's behaviour.
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	targetID := strings.TrimSpace(req.TargetID)
	if targetID == "" {
		targetID = "default"
	}
	// Resolve the target before running so that an unknown id is a 404
	// rather than a job recorded against nothing.
	if t, ok := h.backupStore.GetTarget(targetID); !ok {
		jsonErr(w, http.StatusNotFound, "target not found")
		return
	} else if !t.Enabled {
		jsonErr(w, http.StatusConflict, "target is disabled")
		return
	}

	job, err := h.backupRunner.RunOnceForVM(targetID, vmID)
	if err != nil {
		status, code := backupErrorStatus(err)
		jsonResp(w, status, map[string]any{"job": job, "error": err.Error(), "code": code})
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "backup.vm_run_started", vmID, map[string]any{
			"job_id": job.ID, "target_id": targetID,
		}))
	}
	jsonResp(w, http.StatusAccepted, map[string]any{"job": job})
}

// ListVMBackupJobs returns the recent backup jobs that included this VM.
// Jobs are filtered to the VM in question so an operator sees the history
// of their own machine without learning what else the fleet backs up.
func (h *Handler) ListVMBackupJobs(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	vmID := chiURLParam(r, "id")
	jobs := h.backupStore.ListJobs(200)
	out := make([]backupstore.Job, 0, 8)
	for _, j := range jobs {
		if jobCoversVM(j, vmID) {
			out = append(out, j)
		}
		if len(out) >= 50 {
			break
		}
	}
	jsonResp(w, http.StatusOK, map[string]any{"jobs": out})
}

// jobCoversVM reports whether a recorded job wrote an archive for vmID.
//
// The per-file VMID is the authoritative record of what a run actually
// covered: Job.VMID means something else entirely (the VM produced by a
// restore-as-VM job), so matching on it would show a user restores they
// had nothing to do with.
func jobCoversVM(j backupstore.Job, vmID string) bool {
	if vmID == "" {
		return false
	}
	for _, f := range j.Files {
		if f.VMID == vmID {
			return true
		}
	}
	return false
}
