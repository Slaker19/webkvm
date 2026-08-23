package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/backupstore"
	"webkvm/internal/config"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
)

// --- Targets ---

type backupTargetCreateRequest struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Path        string   `json:"path"`
	VMFilter    string   `json:"vm_filter"`
	VMIDs       []string `json:"vm_ids"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	SSHKeyPath  string   `json:"ssh_key_path"`
	Retention   backupstore.RetentionPolicy `json:"retention"`
}

func (h *Handler) ListBackupTargets(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"targets": h.backupStore.ListTargets()})
}

func (h *Handler) CreateBackupTarget(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	var req backupTargetCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	t, err := h.backupStore.CreateTargetOpts(req.Name, req.Path,
		backupstore.TargetType(req.Type), req.VMFilter, req.VMIDs,
		backupstore.TargetOptions{
			Host:       req.Host,
			Port:       req.Port,
			Username:   req.Username,
			Password:   req.Password,
			SSHKeyPath: req.SSHKeyPath,
			Retention:  req.Retention,
		})
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "backup.target.create", t.ID, map[string]any{"name": t.Name}))
	}
	jsonResp(w, http.StatusCreated, t)
}

func (h *Handler) UpdateBackupTarget(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	// All fields are pointers: nil = "don't change", *x = "set to x".
	// The A4 fix for bug #7: the previous code used "" for "don't
	// change" on Name/Path/Type, which made it impossible to set
	// Enabled=false explicitly — the API would silently treat it as
	// "leave alone". With pointers, false is a real value.
	var req struct {
		Name       *string             `json:"name"`
		Path       *string             `json:"path"`
		Type       *string             `json:"type"`
		VMFilter   *string             `json:"vm_filter"`
		VMIDs      *[]string           `json:"vm_ids"`
		Enabled    *bool               `json:"enabled"`
		Host       *string             `json:"host"`
		Port       *int                `json:"port"`
		Username   *string             `json:"username"`
		Password   *string             `json:"password"`
		SSHKeyPath *string             `json:"ssh_key_path"`
		Retention  *backupstore.RetentionPolicy `json:"retention"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	var ttype *backupstore.TargetType
	if req.Type != nil {
		tt := backupstore.TargetType(*req.Type)
		ttype = &tt
	}
	var opts *backupstore.TargetOptions
	if req.Host != nil || req.Port != nil || req.Username != nil || req.Password != nil || req.SSHKeyPath != nil || req.Retention != nil {
		o := backupstore.TargetOptions{}
		if req.Host != nil {
			o.Host = *req.Host
		}
		if req.Port != nil {
			o.Port = *req.Port
		}
		if req.Username != nil {
			o.Username = *req.Username
		}
		if req.Password != nil {
			o.Password = *req.Password
		}
		if req.SSHKeyPath != nil {
			o.SSHKeyPath = *req.SSHKeyPath
		}
		if o.Password == "" && o.SSHKeyPath == "" && req.Password != nil {
			o.ClearSecret = true
		}
		if req.Retention != nil {
			o.Retention = *req.Retention
		}
		opts = &o
	}
	t, err := h.backupStore.UpdateTarget(id, req.Name, req.Path, ttype, req.VMFilter, req.VMIDs, req.Enabled, opts)
	if err != nil {
		status, code := backupErrorStatus(err)
		jsonResp(w, status, map[string]any{"error": err.Error(), "code": code})
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "backup.target.update", id, nil))
	}
	jsonResp(w, http.StatusOK, t)
}

func (h *Handler) DeleteBackupTarget(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	if err := h.backupStore.DeleteTarget(id); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "backup.target.delete", id, nil))
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- Schedules ---

type backupScheduleCreateRequest struct {
	Name     string `json:"name"`
	Cron     string `json:"cron"`
	TargetID string `json:"target_id"`
}

func (h *Handler) ListBackupSchedules(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"schedules": h.backupStore.ListSchedules()})
}

func (h *Handler) CreateBackupSchedule(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	var req backupScheduleCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	sc, err := h.backupStore.CreateSchedule(req.Name, req.Cron, req.TargetID)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.backupRunner != nil {
		h.backupRunner.Reload()
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "backup.schedule.create", sc.ID, map[string]any{"name": sc.Name}))
	}
	jsonResp(w, http.StatusCreated, sc)
}

func (h *Handler) UpdateBackupSchedule(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	// All fields are pointers: nil = "don't change", *x = "set to x".
	// A4 fix for bug #4: matches the new UpdateTarget convention
	// so a future "edit schedule" UI doesn't have to special-case
	// boolean/zero values.
	var req struct {
		Name     *string `json:"name"`
		Cron     *string `json:"cron"`
		TargetID *string `json:"target_id"`
		Enabled  *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	sc, err := h.backupStore.UpdateSchedule(id, req.Name, req.Cron, req.TargetID, req.Enabled)
	if err != nil {
		status, code := backupErrorStatus(err)
		jsonResp(w, status, map[string]any{"error": err.Error(), "code": code})
		return
	}
	if h.backupRunner != nil {
		h.backupRunner.Reload()
	}
	jsonResp(w, http.StatusOK, sc)
}

func (h *Handler) DeleteBackupSchedule(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	if err := h.backupStore.DeleteSchedule(id); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.backupRunner != nil {
		h.backupRunner.Reload()
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- Jobs ---

func (h *Handler) ListBackupJobs(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"jobs": h.backupStore.ListJobs(50)})
}

// --- Backups (files on disk) ---

func (h *Handler) ListBackupsOnTarget(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	t, ok := h.backupStore.GetTarget(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "target not found")
		return
	}
	files, err := backupstore.ListBackupsOnTarget(t)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Include the stable "latest configuration" snapshot, if any,
	// so the UI can show it separately from per-VM archives.
	config, hasConfig, err := backupstore.LatestConfigInfo(t)
	if err != nil {
		// Non-fatal: the backups list is the important part.
		config = backupstore.BackupFile{}
		hasConfig = false
	}
	resp := map[string]any{"backups": files}
	if hasConfig {
		resp["config"] = config
	}
	jsonResp(w, http.StatusOK, resp)
}

// BackupNowRequest is the body for POST /api/backup/targets/{id}/run.
type BackupNowRequest struct{}

// BackupNow triggers a manual backup. The target may be the URL
// param id; if empty, the default target is used.
//
// The HTTP status is mapped from the runner's sentinel errors:
//   ErrTargetNotFound        → 404
//   ErrTargetDisabled        → 409
//   ErrTargetPathUnwritable  → 400
//   anything else            → 500
// The job is returned in the body on every error path so the UI
// can still render what was attempted.
func (h *Handler) BackupNow(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil || h.backupRunner == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup subsystem not initialized")
		return
	}
	id := chiURLParam(r, "id")
	if id == "" {
		id = "default"
	}
	job, err := h.backupRunner.RunOnceAsync(id, "")
	if err != nil {
		status, code := backupErrorStatus(err)
		jsonResp(w, status, map[string]any{"job": job, "error": err.Error(), "code": code})
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "backup.run_started", id, map[string]any{"job_id": job.ID}))
	}
	jsonResp(w, http.StatusAccepted, map[string]any{"job": job})
}

// backupErrorStatus turns a runner/store error into the right
// HTTP status and a stable "code" string the UI can switch on.
// Centralised so every backup endpoint (BackupNow, RestoreBackup,
// future) maps errors identically.
func backupErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, backupstore.ErrTargetNotFound):
		return http.StatusNotFound, "target_not_found"
	case errors.Is(err, backupstore.ErrTargetDisabled):
		return http.StatusConflict, "target_disabled"
	case errors.Is(err, backupstore.ErrScheduleNotFound):
		return http.StatusNotFound, "schedule_not_found"
	case errors.Is(err, backupstore.ErrInvalidCron):
		return http.StatusBadRequest, "invalid_cron"
	case errors.Is(err, backupstore.ErrTargetPathUnwritable):
		return http.StatusBadRequest, "target_path_unwritable"
	case errors.Is(err, backupstore.ErrDiskFull):
		return http.StatusInsufficientStorage, "disk_full"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

// VerifyBackup computes sha256 of a backup file.
func (h *Handler) VerifyBackup(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		jsonErr(w, http.StatusBadRequest, "filename query param required")
		return
	}
	t, ok := h.backupStore.GetTarget(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "target not found")
		return
	}
	b, err := backupstore.VerifyBackup(t, filename)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, b)
}

// RestoreBackup extracts a backup archive into a fresh directory.
// Phase II accepts two request shapes:
//
//   {"filename": "webkvm-...-vm-1.tar.zst"}    — single file
//   {"run": "20260625T120000.000000000Z-aabbcc"} — every file in
//                                                  that backup run
//
// The handler passes the request's context so a client disconnect
// aborts the tar. Sentinel errors from the runner are mapped to
// the right HTTP status (see backupErrorStatus).
func (h *Handler) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	var req struct {
		Filename string   `json:"filename"`
		Run      string   `json:"run"`
		// Config restores the stable "latest configuration"
		// snapshot (config/webkvm-config-latest.tar.zst).
		Config bool `json:"config"`
		// Files is accepted in addition to Filename/Run
		// so future clients can pass an explicit list. For
		// now, only the first entry is used; the others
		// are ignored. This is here so a Phase III UI
		// that wants a checkbox list doesn't need a
		// breaking change.
		Files []string `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.Filename == "" && req.Run == "" && len(req.Files) == 0 && !req.Config {
		jsonErr(w, http.StatusBadRequest, "filename, run, files, or config is required")
		return
	}
	t, ok := h.backupStore.GetTarget(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "target not found")
		return
	}

	// op runs the restore with a progress callback. Bulk restores
	// (a whole run, the config snapshot, explicit files) can take
	// minutes on multi-GB archives, so the actual work happens in
	// a background job and the handler returns 202 immediately —
	// the UI polls /api/backup/jobs and renders a live bar.
	op := func(onProg func(int, string, map[string]any)) (backupstore.RestoreResult, error) {
		switch {
		case req.Run != "":
			return backupstore.RestoreRun(context.Background(), t, req.Run, h.cfg.DataDir, nil, onProg)
		case req.Config:
			// Restore the stable config snapshot: extract from
			// the config/ subdirectory using its basename.
			cfgTarget := t
			cfgTarget.Path = filepath.Join(t.Path, "config")
			return backupstore.RestoreRun(context.Background(), cfgTarget, "", h.cfg.DataDir,
				[]string{"webkvm-config-latest.tar.zst"}, onProg)
		case req.Filename != "":
			return backupstore.RestoreRun(context.Background(), t, "", h.cfg.DataDir,
				[]string{req.Filename}, onProg)
		default:
			return backupstore.RestoreRun(context.Background(), t, "", h.cfg.DataDir, req.Files, onProg)
		}
	}

	job := backupstore.Job{
		TargetID:  id,
		Filename:  req.Filename,
		StartedAt: time.Now().UTC(),
		Status:    "running",
		Progress:  1,
		Stage:     "restore_preparing",
	}
	job, err := h.backupStore.RecordJob(job)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "record job: "+err.Error())
		return
	}

	go func() {
		// j is a copy so the response's serialization of `job`
		// doesn't race with the goroutine's mutations.
		j := job
		res, rerr := op(func(pct int, stage string, vars map[string]any) {
			j.Progress = pct
			j.Stage = stage
			j.StageVars = vars
			_ = h.backupStore.UpdateJob(j)
		})
		if rerr != nil {
			j.Status = "error"
			j.Error = rerr.Error()
			j.Progress = 100
			j.Stage = "failed"
			j.StageVars = nil
			_ = h.backupStore.UpdateJob(j)
			if h.audit != nil {
				h.audit.Log(auditFor(r, "backup.restore_failed", "unknown", map[string]any{
					"target": id,
					"error":  rerr.Error(),
				}))
			}
			return
		}
		j.Status = "success"
		j.Progress = 100
		j.Stage = "done"
		j.StageVars = nil
		j.Destination = res.Destination
		_ = h.backupStore.UpdateJob(j)
		if h.audit != nil {
			h.audit.Log(auditFor(r, "backup.restore", id, map[string]any{
				"to":         res.Destination,
				"file_count": len(res.Files),
			}))
		}
	}()

	jsonResp(w, http.StatusAccepted, map[string]any{
		"status": "restoring",
		"job_id": job.ID,
	})
}

// RestoreAsVM is the operator-friendly restore: it takes a backup
// archive already on the target's directory and creates a new VM
// in libvirt from it — exactly what POST /api/vms/import does for
// a freshly uploaded archive, but without the re-upload round-trip.
//
// Request body:
//
//	{
//	  "filename": "webkvm-host-20260626T152618-...-7be64cc4-....tar.zst",
//	  "name":     "ubuntu-1-restored",   // optional; default derived
//	  "pool":     "webkvm-disks"          // optional; default config.DiskPoolName
//	}
//
// The new VM is registered in libvirt with the domain XML and
// disk from the archive. The response includes the new VM's uuid
// and resolved name. The source archive is NOT deleted; the
// operator can keep it as an additional safety copy or remove it
// from the Files tab when ready.
//
// This handler is the fix for the "Restore button just extracts
// to a dir, doesn't actually restore" UX bug surfaced in the v4
// release: the old POST /restore endpoint kept its extract-only
// semantics (moved to the legacy code path) and a new endpoint
// with import-like semantics was added.
func (h *Handler) RestoreAsVM(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	if h.lv == nil {
		jsonErr(w, http.StatusServiceUnavailable, "libvirt connector not initialized")
		return
	}
	id := chiURLParam(r, "id")
	var req struct {
		Filename  string  `json:"filename"`
		Name      string  `json:"name"`
		Pool      string  `json:"pool"`
		Network   string  `json:"network"`
		VCPUs     int     `json:"vcpus"`
		RAMMB     int     `json:"ram_mb"`
		Autostart *bool   `json:"autostart"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.Filename == "" {
		jsonErr(w, http.StatusBadRequest, "filename is required")
		return
	}
	if req.Pool == "" {
		req.Pool = config.DiskPoolName
	}
	importOpts := libvirt.ImportOpts{
		Network:   req.Network,
		VCPUs:     req.VCPUs,
		RAMMB:     req.RAMMB,
		Autostart: req.Autostart,
	}
	t, ok := h.backupStore.GetTarget(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "target not found")
		return
	}
	// ValidBackupFilename also enforces the regex shape, so a
	// caller can't pass "../../etc/passwd" and have us open
	// something outside the target's directory.
	if !backupstore.ValidBackupFilename(req.Filename) {
		jsonErr(w, http.StatusBadRequest, "invalid filename format")
		return
	}
	sourcePath := filepath.Join(t.Path, req.Filename)
	cleanup := func() {}
	if t.Type == backupstore.TargetSFTP {
		sp, sz, c, serr := backupstore.StageFileForRestore(t, req.Filename, h.cfg.DataDir)
		if serr != nil {
			jsonErr(w, http.StatusInternalServerError, "stage remote backup: "+serr.Error())
			return
		}
		if c != nil {
			cleanup = c
		}
		sourcePath = sp
		_ = sz
	}
	defer cleanup()
	fi, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			jsonErr(w, http.StatusNotFound, "backup file not found on target")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "stat backup: "+err.Error())
		return
	}

	// Pre-flight free-space check: the disk inside the archive is
	// at least as large as the compressed file we're about to
	// stream into the destination pool. Fail fast with a clear
	// message instead of a half-written qcow2.
	if poolPath, perr := h.lv.GetPoolPath(req.Pool); perr == nil {
		var st syscall.Statfs_t
		if serr := syscall.Statfs(poolPath, &st); serr == nil {
			free := int64(st.Bavail) * int64(st.Bsize)
			if fi.Size() > free {
				jsonErr(w, http.StatusInsufficientStorage,
					fmt.Sprintf("not enough free space in pool %s: need ~%d bytes, have %d", req.Pool, fi.Size(), free))
				return
			}
		}
	}

	// Quota check: a restore-as-VM creates a new VM for the calling
	// user (admins exempt). Estimate resources from the request / file.
	if owner, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		vcpus := req.VCPUs
		if vcpus == 0 {
			vcpus = 2
		}
		ram := req.RAMMB
		if ram == 0 {
			ram = 2048
		}
		diskGB := bytesToGB(fi.Size())
		if err := h.checkQuota(owner, 1, int64(vcpus), int64(ram), diskGB); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
	}

	job := backupstore.Job{
		TargetID:  id,
		Filename:  req.Filename,
		StartedAt: time.Now().UTC(),
		Status:    "running",
		Progress:  1,
		Stage:     "restore_preparing",
	}
	job, err = h.backupStore.RecordJob(job)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "record job: "+err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "vm.restore", "pending", map[string]any{
			"action":   "start",
			"target":   id,
			"filename": req.Filename,
			"size":     fi.Size(),
			"pool":     req.Pool,
			"name":     req.Name,
		}))
	}

	go func() {
		defer cleanup()
		// j is a copy so the response's serialization of `job`
		// doesn't race with the goroutine's mutations.
		j := job
		opts := importOpts
		opts.SourceSize = fi.Size()
		opts.OnProgress = func(pct int, stage string, vars map[string]any) {
			j.Progress = pct
			j.Stage = stage
			j.StageVars = vars
			_ = h.backupStore.UpdateJob(j)
		}
		uuid, resolvedName, warnings, format, ierr := h.importLocalArchive(
			sourcePath, req.Filename, fi.Size(),
			strings.TrimSpace(req.Name), req.Pool, false, opts,
		)
		if ierr != nil {
			j.Status = "error"
			j.Error = ierr.Error()
			j.Progress = 100
			j.Stage = "failed"
			j.StageVars = nil
			_ = h.backupStore.UpdateJob(j)
			if h.audit != nil {
				h.audit.Log(auditFor(r, "vm.restore_failed", "unknown", map[string]any{
					"target":   id,
					"filename": req.Filename,
					"error":    ierr.Error(),
				}))
			}
			return
		}
		j.Status = "success"
		j.Progress = 100
		j.Stage = "done"
		j.StageVars = nil
		j.VMID = uuid
		j.VMName = resolvedName
		_ = h.backupStore.UpdateJob(j)
		if h.audit != nil {
			h.audit.Log(auditFor(r, "vm.restore", uuid, map[string]any{
				"action":   "done",
				"target":   id,
				"filename": req.Filename,
				"name":     resolvedName,
				"size":     fi.Size(),
				"warnings": len(warnings),
			}))
		}
		_ = format
	}()

	jsonResp(w, http.StatusAccepted, map[string]any{
		"status":   "restoring",
		"job_id":   job.ID,
		"target":   id,
		"filename": req.Filename,
	})
}

// TestBackupTarget verifies that a target's destination is
// reachable and writable WITHOUT saving it. The body is the same as
// CreateBackupTarget. For local/NFS/SMB targets it probes the path;
// for SFTP it actually dials the remote host. Used by the "Test"
// button in the Add Target dialog.
func (h *Handler) TestBackupTarget(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	var req backupTargetCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	ttype := backupstore.TargetType(req.Type)
	if ttype == backupstore.TargetSFTP {
		if req.Host == "" {
			jsonErr(w, http.StatusBadRequest, "host is required")
			return
		}
		if req.Username == "" {
			jsonErr(w, http.StatusBadRequest, "username is required")
			return
		}
		msg, err := backupstore.TestSFTP(req.Host, req.Port, req.Username, req.Password, req.SSHKeyPath, req.Path)
		if err != nil {
			jsonResp(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		jsonResp(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
		return
	}
	if req.Path == "" {
		jsonErr(w, http.StatusBadRequest, "path is required")
		return
	}
	if err := backupstore.ValidateTargetPath(req.Path, h.cfg.DataDir); err != nil {
		jsonResp(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	// Local/NFS/SMB: probe the path for existence + writability.
	if err := os.MkdirAll(req.Path, 0o755); err != nil { // lgtm[go/path-injection] - validated above
		jsonResp(w, http.StatusOK, map[string]any{"ok": false, "message": "cannot create path: " + err.Error()})
		return
	}
	probe := filepath.Join(req.Path, ".webkvm-test") // lgtm[go/path-injection] - req.Path validated above
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil { // lgtm[go/path-injection]
		jsonResp(w, http.StatusOK, map[string]any{"ok": false, "message": "path not writable: " + err.Error()})
		return
	}
	_ = os.Remove(probe) // lgtm[go/path-injection]
	jsonResp(w, http.StatusOK, map[string]any{"ok": true, "message": "path exists and is writable"})
}

// DeleteBackupRun removes every archive in a single run from a
// target. The run suffix is the "<ts26>-<randHex>" portion shared by
// all files in a backup run.
func (h *Handler) DeleteBackupRun(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	suffix := chiURLParam(r, "suffix")
	if suffix == "" {
		jsonErr(w, http.StatusBadRequest, "run suffix is required")
		return
	}
	t, ok := h.backupStore.GetTarget(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "target not found")
		return
	}
	removed, err := backupstore.DeleteBackupRun(t, suffix)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "backup.run.delete", id, map[string]any{"run": suffix, "removed": removed}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

// DeleteBackupConfig removes the stable "latest configuration"
// snapshot for a target. The per-run config tars are untouched.
func (h *Handler) DeleteBackupConfig(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	t, ok := h.backupStore.GetTarget(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "target not found")
		return
	}
	if err := backupstore.DeleteBackupConfig(t); err != nil {
		if os.IsNotExist(err) {
			jsonErr(w, http.StatusNotFound, "config snapshot not found")
			return
		}
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "backup.config.delete", id, nil))
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// DeleteBackupFile removes one archive from a target's path. This
// is the only way an operator can clean up disk usage now: there
// is no retention policy, no auto-prune, no scheduled cleanup.
// The filename is matched against the runner's strict
// webkvm-<host>-<UTC>.tar.gz pattern server-side so a request for
// "../../etc/passwd" gets a 400, not a surprise deletion.
func (h *Handler) DeleteBackupFile(w http.ResponseWriter, r *http.Request) {
	if h.backupStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "backup store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	filename := chiURLParam(r, "filename")
	if filename == "" {
		jsonErr(w, http.StatusBadRequest, "filename is required")
		return
	}
	t, ok := h.backupStore.GetTarget(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "target not found")
		return
	}
	if err := backupstore.DeleteBackupFile(t, filename); err != nil {
		// 404 for missing file is more useful to the UI than 500:
		// the file may have been deleted from another tab, and
		// the user just wants a quiet refresh.
		if os.IsNotExist(err) {
			jsonErr(w, http.StatusNotFound, "file not found")
			return
		}
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "backup.file.delete", id, map[string]any{"filename": filename}))
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}
