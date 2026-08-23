package backupstore

import (
	"archive/tar"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"webkvm/internal/models"

	"github.com/robfig/cron/v3"
)

// BackupConfig is the live read-only view the Runner needs from the
// settings store. Returned by the ConfigProvider closure passed to
// NewRunnerWithConfig.
type BackupConfig struct {
	MaxFileSizeMB int
	VerifyOnWrite bool
}

// ConfigProvider returns the current backup-related settings. Called
// on every backup, so the closure can read from a live config store
// without any restart ceremony.
type ConfigProvider func() BackupConfig

// VMSource returns the list of VMs currently known to libvirt.
// Same live-reload pattern as ConfigProvider: invoked at the start
// of every RunOnce so a VM created between two backup runs is
// included the next time around. Returning an error aborts the
// run; the caller should not silently return an empty list.
type VMSource func() ([]models.VM, error)

// VMXMLSource returns the raw libvirt <domain>...</domain>
// descriptor for a single VM. Called once per in-scope VM during
// the run to populate the domain.xml entry in the per-VM
// archive. Optional — when nil, the runner writes a minimal
// placeholder <domain><name>{id}</name></domain> so the archive
// shape stays consistent. The main process wires this to
// libvirt.Connector.GetDomainXML.
type VMXMLSource func(vmID string) (string, error)

// VMSnapshotSource returns the snapshot metadata and overlay
// volume paths for a single VM. Called once per in-scope VM
// during the run. Optional — when nil, no snapshot entries
// are written to the per-VM archive (backward-compatible).
type VMSnapshotSource func(vmID string) ([]SnapshotBackup, error)

// Runner executes backup jobs. It maintains a cron ticker and
// records Jobs in the Store as it goes. Manual "backup now" calls
// go through the same RunOnce path.
type Runner struct {
	store   *Store
	dataDir string
	cron    *cron.Cron
	logger  *slog.Logger
	// mu serializes backup runs. Only one tar/upload at a time:
	// a manual "Backup now" and a cron tick are mutually exclusive
	// instead of racing on the same disks.
	mu sync.Mutex
	// config is consulted on every RunOnce so retention/verify
	// changes from the Settings page take effect on the next
	// backup. Must not be nil; NewRunnerWithConfig panics if a nil
	// closure is passed (the alternative is silently wrong).
	config ConfigProvider
	// vms lists the VMs on the host at backup time. Used to honour
	// the per-target VMFilter / VMIDs. May be nil — in that case
	// the runner falls back to backing up the whole dataDir,
	// which is the Phase 1.3 behaviour.
	vms VMSource
	// xmlSource returns the libvirt XML for a single VM.
	// Optional; nil means the per-VM archive gets a placeholder.
	xmlSource VMXMLSource
	// snapSource returns snapshot metadata and overlay volumes
	// for a single VM. Optional; nil means no snapshot entries
	// are written (backward-compatible with older connectors).
	snapSource VMSnapshotSource
}

// NewRunnerWithConfig wires the runner with a live config provider.
// The closure is invoked at the start of every RunOnce and inside
// the post-write verify pass. A nil vms source is tolerated: it
// only disables the per-VM filter, after which every target backs
// up the whole dataDir (the Phase 1.3 behaviour). To honour
// VMFilter/VMIDs the caller must pass a real VMSource, typically
// libvirt.Connector.ListDomains.
//
// xmlSource is optional; nil disables the per-VM XML lookup
// (the per-VM archive gets a placeholder domain.xml). main.go
// wires the libvirt connector's GetDomainXML in production.
//
// snapSource is optional; nil means snapshot metadata and
// overlay volumes are not included in per-VM archives
// (backward-compatible).
func NewRunnerWithConfig(store *Store, dataDir string, config ConfigProvider, vms VMSource, xmlSource VMXMLSource, snapSource VMSnapshotSource, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	if config == nil {
		panic("backupstore: NewRunnerWithConfig requires a non-nil ConfigProvider")
	}
	return &Runner{
		store:      store,
		dataDir:    dataDir,
		cron:       cron.New(),
		logger:     logger,
		config:     config,
		vms:        vms,
		xmlSource:  xmlSource,
		snapSource: snapSource,
	}
}

// Start installs cron entries for every enabled schedule.
// Re-call after any CreateSchedule/UpdateSchedule.
func (r *Runner) Start(ctx context.Context) {
	for _, sc := range r.store.ListSchedules() {
		if !sc.Enabled {
			continue
		}
		r.addSchedule(sc)
	}
	r.cron.Start()
	<-ctx.Done()
	r.cron.Stop()
}

func (r *Runner) addSchedule(sc Schedule) {
	id := sc.ID
	_, err := r.cron.AddFunc(sc.Cron, func() {
		_, _ = r.RunOnce(context.Background(), id, id)
	})
	if err != nil {
		// The store validates cron at write time (CreateSchedule /
		// UpdateSchedule), so reaching here with a parse error
		// means either the operator hand-edited schedules.json
		// or a downgrade+upgrade left a legacy value. Either
		// way, surface it loudly — the schedule will never fire.
		r.logger.Error("backup_schedule_add_failed",
			"id", id, "name", sc.Name, "cron", sc.Cron, "err", err)
	}
}

// Reload re-installs cron entries. Call after any change to schedules.
func (r *Runner) Reload() {
	// cron doesn't have a public "remove all"; the simplest is to
	// stop and start a new Cron. This is fine because the cron
	// library is goroutine-cheap.
	stopped := r.cron.Stop()
	if stopped != nil {
		// Wait for the in-flight jobs to finish.
		<-stopped.Done()
	}
	r.cron = cron.New()
	for _, sc := range r.store.ListSchedules() {
		if !sc.Enabled {
			continue
		}
		r.addSchedule(sc)
	}
	r.cron.Start()
}

// RunOnce performs a backup of dataDir into the given target. If
// scheduleID is set, the run is associated with that schedule;
// otherwise it's a manual "Backup now" call.
//
// Backups are write-only: the runner never deletes anything from
// the target path. Operators clean up old archives via the Files
// tab on the Backup page.
//
// Pre-flight failures (target missing, target disabled, target
// path unwritable) are returned as the sentinel errors in
// errors.go. The HTTP handler maps them to 404 / 409 / 400 so
// the UI gets a meaningful status code, not a generic 500.
// Anything that escapes the runner's own error wrapping
// (libvirt, tar, fs) is treated as a 500 by the handler.
func (r *Runner) RunOnce(ctx context.Context, targetID, scheduleID string) (Job, error) {
	tgt, ok := r.store.GetTarget(targetID)
	if !ok {
		return Job{}, fmt.Errorf("%w: %q", ErrTargetNotFound, targetID)
	}
	if !tgt.Enabled {
		return Job{}, fmt.Errorf("%w: %q", ErrTargetDisabled, targetID)
	}

	job := Job{
		TargetID:   targetID,
		ScheduleID: scheduleID,
		StartedAt:  time.Now().UTC(),
		Status:     "running",
	}
	job, err := r.store.RecordJob(job)
	if err != nil {
		return job, err
	}
	// job now has the generated ID; a later UpdateJob will
	// find the right entry in s.jobs.
	return r.runJob(ctx, tgt, job, scheduleID)
}

// RunOnceAsync launches a backup in a background goroutine and
// returns the running Job immediately. The UI polls
// /api/backup/jobs to render a live progress bar. Runs are
// serialized against cron ticks by r.mu inside runJob.
func (r *Runner) RunOnceAsync(targetID, scheduleID string) (Job, error) {
	tgt, ok := r.store.GetTarget(targetID)
	if !ok {
		return Job{}, fmt.Errorf("%w: %q", ErrTargetNotFound, targetID)
	}
	if !tgt.Enabled {
		return Job{}, fmt.Errorf("%w: %q", ErrTargetDisabled, targetID)
	}
	job := Job{
		TargetID:   targetID,
		ScheduleID: scheduleID,
		StartedAt:  time.Now().UTC(),
		Status:     "running",
	}
	job, err := r.store.RecordJob(job)
	if err != nil {
		return job, err
	}
	go func() {
		_, _ = r.runJob(context.Background(), tgt, job, scheduleID)
	}()
	return job, nil
}

// runJob executes a backup for an already-registered job and
// persists the final state (status + live progress). It is shared
// by the synchronous RunOnce (cron / tests) and the asynchronous
// RunOnceAsync (the "Backup now" button). Progress is reported as
// a percentage and a human-readable message so the UI can render
// a live bar instead of a frozen spinner.
func (r *Runner) runJob(ctx context.Context, tgt Target, job Job, scheduleID string) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// report persists live progress so the UI can poll it. Stage is
	// a stable phase code the frontend translates (backend text would
	// be locked to one language). Vars carry interpolation values.
	report := func(p int, stage string, vars map[string]any) {
		job.Progress = p
		job.Stage = stage
		job.StageVars = vars
		job.Message = ""
		if uerr := r.store.UpdateJob(job); uerr != nil {
			r.logger.Error("backup_job_update_failed", "job_id", job.ID, "err", uerr)
		}
	}
	report(1, "preparing", nil)

	destDir := tgt.Path
	var staging string
	if tgt.Type == TargetSFTP {
		staging = filepath.Join(r.dataDir, "backup-staging", tgt.ID+"-"+randHex(4))
		destDir = staging
	}
	files, totalBytes, err := r.writeBackup(tgt, destDir, report)
	if err == nil && tgt.Type == TargetSFTP {
		report(90, "uploading", map[string]any{"path": tgt.Path})
		up, uErr := uploadSFTPRun(tgt, staging, files)
		_ = os.RemoveAll(staging)
		if uErr != nil {
			err = fmt.Errorf("sftp upload: %w", uErr)
		} else {
			totalBytes = up
		}
	} else if err != nil && staging != "" {
		_ = os.RemoveAll(staging)
	}
	job.EndedAt = time.Now().UTC()
	job.Size = totalBytes
	job.Filenames = make([]string, 0, len(files))
	job.Files = make([]JobFile, 0, len(files))
	for i, f := range files {
		job.Filenames = append(job.Filenames, f.Filename)
		job.Files = append(job.Files, f)
		// The singular Filename stays pointing at the config
		// tar (always the last entry written) so the existing
		// Jobs tab UI keeps showing a single primary file.
		// Filenames / Files carry the full list.
		if i == len(files)-1 && f.Kind == "config" {
			job.Filename = f.Filename
		}
	}
	if err != nil {
		job.Status = "error"
		job.Error = err.Error()
		job.Progress = 100
		job.Stage = "failed"
		job.StageVars = nil
		job.Message = ""
		if uerr := r.store.UpdateJob(job); uerr != nil {
			r.logger.Error("backup_job_update_failed", "job_id", job.ID, "err", uerr)
		}
		if scheduleID != "" {
			if serr := r.store.SetScheduleLastRun(scheduleID, "error", err.Error(), time.Time{}); serr != nil {
				r.logger.Error("backup_schedule_update_failed", "schedule_id", scheduleID, "err", serr)
			}
		}
		return job, err
	}
	job.Status = "success"
	job.Progress = 100
	job.Stage = "done"
	job.StageVars = nil
	job.Message = ""
	if uerr := r.store.UpdateJob(job); uerr != nil {
		r.logger.Error("backup_job_update_failed", "job_id", job.ID, "err", uerr)
	}

	// Optional post-write verify. The user can also click Verify
	// from the Files tab at any time, which calls VerifyBackup
	// directly without re-running the backup. We verify the
	// config tar (the primary file) only; the per-VM tars are
	// large and the SHA256 cost is non-trivial.
	if r.config().VerifyOnWrite && job.Filename != "" {
		if _, err := VerifyBackup(tgt, job.Filename); err != nil {
			r.logger.Warn("backup_verify_failed", "target", tgt.ID, "file", job.Filename, "err", err)
		}
	}

	// Automatic retention: prune old runs if the target has a
	// retention policy. This never removes the run we just wrote
	// (it is the newest) and always removes whole runs, never
	// individual files, so an in-progress run is never torn apart.
	if tgt.Retention.Enabled() {
		removed, rerr := ApplyRetention(r.store, tgt)
		if rerr != nil {
			r.logger.Warn("backup_retention_failed", "target", tgt.ID, "err", rerr)
		} else if removed > 0 {
			r.logger.Info("backup_retention_applied", "target", tgt.ID, "removed_runs", removed)
		}
	}

	if scheduleID != "" {
		if serr := r.store.SetScheduleLastRun(scheduleID, "success", "", time.Time{}); serr != nil {
			r.logger.Error("backup_schedule_update_failed", "schedule_id", scheduleID, "err", serr)
		}
	}
	return job, nil
}

// byteProgressWriter wraps an io.Writer and reports the cumulative
// percentage of bytes written against a known total. It drives the
// live per-VM progress while the (multi-GB) tar+zstd producer
// streams an archive out. Rate-limited to whole percentages so
// UpdateJob isn't hammered thousands of times.
type byteProgressWriter struct {
	w       io.Writer
	total   int64
	written int64
	base    int
	span    int
	last    int
	on      func(pct int)
}

func (p *byteProgressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if p.total > 0 {
		pct := p.base + int(float64(p.written)/float64(p.total)*float64(p.span))
		if pct > p.base+p.span {
			pct = p.base + p.span
		}
		if pct > p.last {
			p.last = pct
			p.on(pct)
		}
	}
	return n, err
}

// writeBackup is the Phase II write path. Each backup run now
// produces N+1 archives in tgt.Path:
//
//   webkvm-<host>-<UTC>-<rand>-<vmname>.tar.zst  (one per in-scope VM)
//   webkvm-<host>-<UTC>-<rand>-config.tar.zst    (always; app state)
//
// The N per-VM archives are byte-compatible with the
// browser-downloaded WebKVM export (Phase II-B4), so a
// restore is the same ImportDomain path either way. The
// config archive carries the app metadata (users, config,
// schedules, pool-purposes, nodes, audit tail) so a disk
// loss doesn't leave the operator re-creating the
// deployment from scratch.
//
// Returns the per-file list (filenames + sizes + kind +
// vm_id) and the total bytes written. The caller is
// expected to populate Job.Filenames / Job.Files /
// Job.Size and to delete any half-written files on a
// partial failure.
//
// Selection rules (unchanged from Phase I):
//
//   1. Resolve which VMs are in scope from tgt.VMFilter /
//      tgt.VMIDs, asking the VMSource. A nil VMSource is
//      allowed and produces 0 VM archives (config-only).
//   2. For each in-scope VM, include the file backing of
//      every disk whose Device != "cdrom" and whose Source
//      is under r.dataDir. CDROMs and out-of-tree disks
//      are skipped. Files larger than MaxFileSizeMB are
//      also skipped (the size filter is now Go-side in
//      the producer; the old --exclude=*.%dM-and-larger
//      GNU tar flag was a no-op).
//   3. Always include the app-state config tar.
//
// The per-disk size cap defaults to 1 TiB (effectively
// "unlimited" for any reasonable VM). The previous default
// of 100 MiB was inherited from the Phase I bash script
// and dropped every real VM disk on the floor — a 32 GB
// qcow2 was always over the cap. The A6 commit raises the
// default so an out-of-the-box install produces a usable
// backup; operators who actually want to skip huge disks
// can lower the cap via `backup.max_file_size_mb` in
// config.json (or the future Settings page).
func (r *Runner) writeBackup(tgt Target, destDir string, onProgress ...func(int, string, map[string]any)) ([]JobFile, int64, error) {
	var progress func(int, string, map[string]any)
	if len(onProgress) > 0 {
		progress = onProgress[0]
	}
	cfg := r.config()
	maxSizeMB := cfg.MaxFileSizeMB
	if maxSizeMB <= 0 {
		maxSizeMB = 1 << 20 // 1 TiB in MiB; effectively unlimited
	}
	maxSize := int64(maxSizeMB) * 1024 * 1024

	// Defense in depth (A2): re-validate the path before any
	// I/O. Catches hand-edited targets.json that pointed at a
	// system root. SFTP targets write to a staging dir under
	// dataDir, which is always allowed.
	if err := ValidateTargetPath(destDir, r.dataDir); err != nil {
		r.logger.Error("backup_path_denied", "target", tgt.ID, "path", destDir, "err", err)
		return nil, 0, err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		r.logger.Error("backup_mkdir_failed", "target", tgt.ID, "path", destDir, "err", err)
		return nil, 0, fmt.Errorf("%w: mkdir: %v", ErrTargetPathUnwritable, err)
	}

	// One timestamp + random suffix for the whole run. Every
	// file in this run shares them so the operator can group
	// by `<UTC>-<rand>` and see "this is one backup".
	host, _ := os.Hostname()
	host = strings.ReplaceAll(host, " ", "_")
	tsNano := time.Now().UTC().Format("20060102T150405.000000000Z")
	suffix := randHex(6)

	// Run-level context with a 30-minute cap. A single backup
	// run is allowed up to 30 min total; aborts cleanly on
	// timeout and removes the half-written files. (Raised from
	// 10 min: multi-VM runs with a large disk can take longer,
	// and since backups now run in the background the HTTP
	// request no longer shares this context.)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Resolve the in-scope VMs up front. A failure aborts the
	// whole run rather than silently writing a config-only
	// archive.
	scope, err := r.resolveScope(tgt)
	if err != nil {
		return nil, 0, err
	}

	// A8: pre-flight free-space check. Estimates the total
	// bytes the run will write (sum of disk file sizes plus
	// the data-dir tree plus a 20% zstd overhead) and
	// compares with the available space on tgt.Path. Fails
	// fast with ErrDiskFull instead of letting the producer
	// fill the disk and die with ENOSPC 60s into a 5 GB copy
	// (the v3 .130 incident where the target pointed at
	// /tmp = tmpfs 3.6 GB).
	//
	// Best-effort: Statfs can fail on weird mounts (NFS
	// quirks, FUSE, etc.) and a missing file in the
	// estimate is silently skipped. The check is a guard
	// rail, not a guarantee — the real write can still hit
	// ENOSPC if another process fills the disk in between.
	need, estErr := r.estimateRunBytes(scope)
	if estErr != nil {
		r.logger.Warn("backup_space_estimate_failed", "target", tgt.ID, "err", estErr)
	} else {
		var stat syscall.Statfs_t
		if serr := syscall.Statfs(destDir, &stat); serr == nil {
			free := int64(stat.Bavail) * int64(stat.Bsize)
			if need > free {
				r.logger.Error("backup_disk_full",
					"target", tgt.ID,
					"path", destDir,
					"need_bytes", need,
					"free_bytes", free,
				)
				return nil, 0, fmt.Errorf("%w: need %d, have %d", ErrDiskFull, need, free)
			}
			r.logger.Info("backup_space_ok",
				"target", tgt.ID,
				"need_bytes", need,
				"free_bytes", free,
			)
		} else {
			r.logger.Warn("backup_space_statfs_failed", "target", tgt.ID, "path", destDir, "err", serr)
		}
	}

	// 1) Per-VM archives.
	var files []JobFile
	var totalBytes int64
	vmTotal := len(scope)
	if vmTotal == 0 {
		vmTotal = 1
	}
	for i, vm := range scope {
		if err := ctx.Err(); err != nil {
			return nil, totalBytes, err
		}
		// Sanitise the VM name into something that survives
		// the filesystem (no slashes, no leading dots, all
		// lowercase-ish). The producer does not impose any
		// constraint on the name; the runner does.
		name := sanitizeVMName(vm.ID)
		filename, outPath, err := r.allocateOutputPath(destDir, host, tsNano, suffix, name+".tar.zst")
		if err != nil {
			r.logger.Error("backup_create_failed", "target", tgt.ID, "path", outPath, "err", err)
			return files, totalBytes, fmt.Errorf("%w: create: %v", ErrTargetPathUnwritable, err)
		}
		// Fetch the VM's libvirt XML. If the source is nil
		// (test path), use a placeholder so the archive
		// shape stays consistent.
		xml := "<domain type='kvm'><name>" + xmlEscape(vm.ID) + "</name></domain>"
		if r.xmlSource != nil {
			real, xerr := r.xmlSource(vm.ID)
			if xerr != nil {
				r.logger.Warn("backup_xml_fetch_failed", "vm", vm.ID, "err", xerr)
				// Continue with the placeholder; the user
				// gets a config-only-ish archive for this VM
				// (disks but no real XML).
			} else {
				xml = real
			}
		}
		// Collect snapshot metadata and overlay volumes.
		// The producer writes snapshots/<name>.xml and
		// snapshots/<name>/<basename> entries for each.
		var snaps []SnapshotBackup
		if r.snapSource != nil {
			var snapErr error
			snaps, snapErr = r.snapSource(vm.ID)
			if snapErr != nil {
				r.logger.Warn("backup_snapshot_fetch_failed", "vm", vm.ID, "err", snapErr)
			}
		}

		// Per-VM size cap. Filter the disks down to those
		// under r.dataDir (the producer would do this anyway
		// via os.Stat, but excluding them here keeps the
		// manifest clean).
		disks := filterInTreeDisks(vm.Disks, r.dataDir)
		vmBackup := VMBackup{ID: vm.ID, DomainXML: xml, Disks: disks, Snapshots: snaps}
		f, err := os.Create(outPath)
		if err != nil {
			r.logger.Error("backup_create_failed", "target", tgt.ID, "path", outPath, "err", err)
			return files, totalBytes, fmt.Errorf("%w: create: %v", ErrTargetPathUnwritable, err)
		}
		// Estimate the uncompressed size of this VM's disks so the
		// progress bar moves while the archive streams out (a big
		// VM would otherwise sit frozen at its base percentage for
		// minutes).
		var est int64
		for _, d := range vm.Disks {
			if d.Source == "" {
				continue
			}
			if dff, derr := os.Stat(d.Source); derr == nil {
				est += dff.Size()
			}
		}
		if est == 0 {
			est = 1
		}
		var out io.Writer = f
		var vmNextBase, vmSpan int
		var vmVars map[string]any
		if progress != nil {
			base := i * 70 / vmTotal
			vmNextBase = (i + 1) * 70 / vmTotal
			vmSpan = vmNextBase - base
			if vmSpan < 1 {
				vmSpan = 1
			}
			vmVars = map[string]any{"name": vm.ID, "i": i + 1, "n": len(scope)}
			// Report the phase immediately so the bar isn't stuck on
			// "preparing" while the first ~1GB streams out.
			progress(base, "compress_vm", vmVars)
			out = &byteProgressWriter{
				w:     f,
				total: est,
				base:  base,
				span:  vmSpan,
				last:  base,
				on: func(pct int) {
					progress(pct, "compress_vm", vmVars)
				},
			}
		}
		res, werr := ProduceVMArchive(ctx, vmBackup, ProducerOpts{
			Compression:   "zstd",
			DiskSizeLimit: maxSize,
		}, out)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(outPath)
			err := werr
			if err == nil {
				err = cerr
			}
			return files, totalBytes, fmt.Errorf("write vm %s: %w", vm.ID, err)
		}
		if progress != nil {
			progress(vmNextBase, "compress_vm", vmVars)
		}
		files = append(files, JobFile{
			Filename: filename,
			Size:     res.TotalBytes,
			Kind:     "vm",
			VMID:     vm.ID,
		})
		totalBytes += res.TotalBytes
	}

	// 2) The config tar (always; even when no VMs were in
	// scope the operator still gets the app state).
	if progress != nil {
		progress(72, "config", nil)
	}
	cfgFilename, cfgOutPath, err := r.allocateOutputPath(destDir, host, tsNano, suffix, "config.tar.zst")
	if err != nil {
		r.logger.Error("backup_create_failed", "target", tgt.ID, "path", cfgOutPath, "err", err)
		return files, totalBytes, fmt.Errorf("%w: create: %v", ErrTargetPathUnwritable, err)
	}
	cfgSize, cfgErr := r.writeConfigArchive(ctx, cfgOutPath, tgt)
	if cfgErr != nil {
		_ = os.Remove(cfgOutPath)
		return files, totalBytes, fmt.Errorf("write config: %w", cfgErr)
	}
	files = append(files, JobFile{
		Filename: cfgFilename,
		Size:     cfgSize,
		Kind:     "config",
	})
	totalBytes += cfgSize

	// 2b) Global "latest config" snapshot under config/ so the UI
	// can show the app configuration apart from per-VM archives.
	// Best-effort: a failure here doesn't fail the run (the run's
	// own config tar is already on disk).
	if progress != nil {
		progress(80, "config_copy", nil)
	}
	if err := copyConfigGlobal(cfgOutPath, destDir); err != nil {
		r.logger.Warn("backup_config_global_failed", "target", tgt.ID, "err", err)
	}

	r.logger.Info("backup_run_complete",
		"target", tgt.ID,
		"files", len(files),
		"total_bytes", totalBytes,
		"vms", len(scope),
	)
	return files, totalBytes, nil
}

// estimateRunBytes adds up the approximate bytes the run will
// write to disk. It is used by the A8 pre-flight free-space
// check to fail fast before the producer starts streaming.
//
// Components:
//
//   - Sum of every in-scope disk file's size (per-VM tar).
//     Files we cannot stat (gone, race, missing) are skipped
//     rather than failing the check; the run will just produce
//     a smaller archive.
//   - Approximate size of the config tar: the data-dir tree
//     minus the disk pool. This is usually <10 MB (users.json,
//     targets.json, schedules.json, audit.log) but a chatty
//     audit.log can grow large over time.
//   - 20% buffer for zstd overhead and the tar framing.
//
// The estimate is intentionally a high-end guess — better to
// abort on a tight disk than to ENOSPC 60 s into a copy.
func (r *Runner) estimateRunBytes(scope []models.VM) (int64, error) {
	const overheadPct = 20
	var total int64
	for _, vm := range scope {
		for _, d := range vm.Disks {
			if d.Source == "" {
				continue
			}
			fi, err := os.Stat(d.Source)
			if err != nil {
				// Gone or unreadable; the producer will
				// handle it. Skip the estimate entry.
				continue
			}
			total += fi.Size()
		}
	}
	// Approximate the config tar by walking r.dataDir and
	// adding every regular file's size, excluding the disk
	// pool. The disk pool is already counted above.
	if r.dataDir != "" {
		diskPool := filepath.Join(r.dataDir, "pools", "webkvm-disks")
		_ = filepath.WalkDir(r.dataDir, func(path string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				// Skip the disk pool subtree.
				if path == diskPool {
					return filepath.SkipDir
				}
				return nil
			}
			fi, err := de.Info()
			if err != nil {
				return nil
			}
			total += fi.Size()
			return nil
		})
	}
	// Add the overhead.
	total = total * (100 + overheadPct) / 100
	return total, nil
}

// resolveScope returns the in-scope VMs for tgt after applying
// tgt.VMFilter / tgt.VMIDs. Returns an empty slice (not an
// error) when VMSource is nil or no VM matches; the caller
// still writes the config tar.
func (r *Runner) resolveScope(tgt Target) ([]models.VM, error) {
	if r.vms == nil {
		r.logger.Warn("backup_no_vm_source", "target", tgt.ID,
			"reason", "VMSource is nil; archive will be config-only")
		return nil, nil
	}
	all, err := r.vms()
	if err != nil {
		return nil, fmt.Errorf("list vms: %w", err)
	}
	filter := tgt.VMFilter
	if filter == "" {
		filter = "all"
	}
	included := make(map[string]struct{}, len(tgt.VMIDs))
	for _, id := range tgt.VMIDs {
		included[id] = struct{}{}
	}
	var out []models.VM
	switch filter {
	case "all":
		out = all
	case "include":
		for _, vm := range all {
			if _, ok := included[vm.ID]; ok {
				out = append(out, vm)
			}
		}
	case "exclude":
		for _, vm := range all {
			if _, drop := included[vm.ID]; !drop {
				out = append(out, vm)
			}
		}
	default:
		return nil, fmt.Errorf("unknown vm_filter %q", filter)
	}
	return out, nil
}

// writeConfigArchive writes the app-state config tar to
// outPath. The contents are the small metadata files at the
// top of r.dataDir (users, config, audit, nodes, pool-
// purposes, etc.) plus the backup store's own targets /
// schedules. Everything goes through a zstd-compressed tar
// produced in-process (no shell-out). audit.log is dropped
// when it's grown past 10 MB — the operator can re-export
// the full audit log from the Audit page.
func (r *Runner) writeConfigArchive(ctx context.Context, outPath string, tgt Target) (int64, error) {
	f, err := os.Create(outPath)
	if err != nil {
		return 0, fmt.Errorf("create: %w", err)
	}
	cw, cclose, err := newCompressor(f, "zstd", 0)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(outPath)
		return 0, err
	}
	tw := tar.NewWriter(cw)
	counter := &countingWriter{w: f}
	_ = counter // counter only needed for size; we stat the file at the end
	// Files to include: the dataDir top-level metadata + the
	// backup subdir's state. Errors reading a single file
	// are skipped (with a warn log) so a corrupt users.json
	// doesn't kill the whole config tar.
	type pick struct {
		arcName string
		srcPath string
	}
	entries, err := r.collectConfigEntries()
	if err != nil {
		_ = tw.Close()
		_ = cclose()
		_ = f.Close()
		_ = os.Remove(outPath)
		return 0, err
	}
	var manifestLines []string
	for _, p := range entries {
		if err := ctx.Err(); err != nil {
			_ = tw.Close()
			_ = cclose()
			_ = f.Close()
			_ = os.Remove(outPath)
			return 0, err
		}
		info, ierr := os.Stat(p.srcPath)
		if ierr != nil {
			r.logger.Warn("backup_config_file_stat_failed", "path", p.srcPath, "err", ierr)
			continue
		}
		// audit.log cap: drop entries larger than 10 MB so
		// the backup tar doesn't bloat with the full history.
		if p.arcName == "audit.log" && info.Size() > 10*1024*1024 {
			r.logger.Warn("backup_audit_log_skipped", "size", info.Size())
			manifestLines = append(manifestLines, fmt.Sprintf("%s  SKIPPED (audit.log > 10MB; re-export from the Audit page)", p.arcName))
			continue
		}
		if err := writeFileToTar(tw, p.srcPath, p.arcName); err != nil {
			r.logger.Warn("backup_config_file_write_failed", "path", p.srcPath, "err", err)
			manifestLines = append(manifestLines, fmt.Sprintf("%s  FAILED (%v)", p.arcName, err))
			continue
		}
		manifestLines = append(manifestLines, fmt.Sprintf("%s  %d", p.arcName, info.Size()))
	}
	// Manifest
	manifest := "kind=config\n"
	manifest += "host=" + tgt.ID + "\n"
	manifest += "created=" + time.Now().UTC().Format(time.RFC3339) + "\n"
	for _, line := range manifestLines {
		manifest += line + "\n"
	}
	if err := writeTarEntry(tw, "manifest.txt", []byte(manifest), 0o644); err != nil {
		_ = tw.Close()
		_ = cclose()
		_ = f.Close()
		_ = os.Remove(outPath)
		return 0, err
	}
	if err := tw.Close(); err != nil {
		_ = cclose()
		_ = f.Close()
		_ = os.Remove(outPath)
		return 0, err
	}
	if err := cclose(); err != nil {
		_ = f.Close()
		_ = os.Remove(outPath)
		return 0, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(outPath)
		return 0, err
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// copyConfigGlobal copies the run's config tar to
// {destDir}/config/webkvm-config-latest.tar.zst — a stable
// "latest configuration" snapshot the UI can show separately from
// per-VM archives. For SFTP targets destDir is the staging dir;
// uploadSFTPRun pushes the config/ subtree on upload.
func copyConfigGlobal(srcPath, destDir string) error {
	dir := filepath.Join(destDir, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, "webkvm-config-latest.tar.zst")
	return copyFile(srcPath, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, cErr := io.Copy(out, in)
	clErr := out.Close()
	if cErr != nil {
		return cErr
	}
	return clErr
}

// configGlobalRel is the stable relative path of the "latest
// configuration" snapshot inside a target (or its SFTP remote dir).
const configGlobalRel = "config/webkvm-config-latest.tar.zst"

// LatestConfigInfo returns info about the global configuration
// snapshot for a target, if present. The bool is false when no
// snapshot has been written yet.
func LatestConfigInfo(tgt Target) (BackupFile, bool, error) {
	if tgt.Type == TargetSFTP {
		client, conn, err := dialSFTP(tgt)
		if err != nil {
			return BackupFile{}, false, err
		}
		defer client.Close()
		defer conn.Close()
		info, err := client.Stat(remoteJoin(tgt, configGlobalRel))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return BackupFile{}, false, nil
			}
			return BackupFile{}, false, err
		}
		return BackupFile{TargetID: tgt.ID, Filename: configGlobalRel, Size: info.Size(), Modified: info.ModTime().UTC()}, true, nil
	}
	p := filepath.Join(tgt.Path, configGlobalRel)
	info, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return BackupFile{}, false, nil
		}
		return BackupFile{}, false, err
	}
	return BackupFile{TargetID: tgt.ID, Filename: configGlobalRel, Size: info.Size(), Modified: info.ModTime().UTC()}, true, nil
}

// DeleteBackupConfig removes the global configuration snapshot.
// The per-run config tars are untouched.
func DeleteBackupConfig(tgt Target) error {
	if tgt.Type == TargetSFTP {
		client, conn, err := dialSFTP(tgt)
		if err != nil {
			return err
		}
		defer client.Close()
		defer conn.Close()
		if err := client.Remove(remoteJoin(tgt, configGlobalRel)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return os.ErrNotExist
			}
			return err
		}
		return nil
	}
	err := os.Remove(filepath.Join(tgt.Path, configGlobalRel))
	if err != nil && os.IsNotExist(err) {
		return os.ErrNotExist
	}
	return err
}

// collectConfigEntries lists the files that go into the
// config tar. Returns archive-name + on-disk-path pairs.
// Filters out the ISO pool (huge) and any subdir; the
// backup store's own JSON files are pulled in by the
// second pass.
// isExcludedConfigFile reports whether a dataDir top-level file must
// never be included in the configuration backup. Credential-bearing
// files and the bootstrap admin password are excluded so a leaked
// backup cannot reveal secrets; *.tmp files are transient.
func isExcludedConfigFile(name string) bool {
	switch name {
	case "notify-secrets.json", "sftp-secrets.json", "admin-password.initial":
		return true
	}
	return strings.HasSuffix(name, ".tmp")
}

func (r *Runner) collectConfigEntries() ([]struct {
	arcName string
	srcPath string
}, error) {
	var out []struct {
		arcName string
		srcPath string
	}
	// Top-level dataDir entries (skip subdirs: pools/, backup/).
	entries, err := os.ReadDir(r.dataDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			// Special case: backup/ contains the targets and
			// schedules JSONs which we DO want.
			if e.Name() == "backup" {
				continue
			}
			continue
		}
		// NEVER ship credentials or the initial password in the
		// config backup: a leaked backup must not expose the SFTP
		// password, webhook/SMTP secrets or the bootstrap admin
		// password. Also skip transient temp files.
		if isExcludedConfigFile(e.Name()) {
			continue
		}
		out = append(out, struct {
			arcName string
			srcPath string
		}{arcName: e.Name(), srcPath: filepath.Join(r.dataDir, e.Name())})
	}
	// backup/targets.json + backup/schedules.json (skip jobs.json
	// — the operator can look at the Jobs tab for the current
	// run log).
	backupDir := filepath.Join(r.dataDir, "backup")
	for _, name := range []string{"targets.json", "schedules.json"} {
		out = append(out, struct {
			arcName string
			srcPath string
		}{arcName: "backup/" + name, srcPath: filepath.Join(backupDir, name)})
	}
	return out, nil
}

// filterInTreeDisks returns the subset of disks whose Source
// is under root (a path the producer can read and the
// restore can reproduce). Out-of-tree disks are dropped
// with a warn log; they're rare (hand-edited domain XML
// pointing at /var/lib/libvirt/images, etc.) and including
// them would change the archive layout on restore.
func filterInTreeDisks(disks []models.DiskInfo, root string) []models.DiskInfo {
	var out []models.DiskInfo
	for _, d := range disks {
		if d.Device == "cdrom" || d.Source == "" {
			continue
		}
		if strings.Contains(d.Source, "/pools/ISOS/") || strings.HasSuffix(d.Source, ".iso") {
			continue
		}
		rel, err := filepath.Rel(root, d.Source)
		if err != nil || strings.HasPrefix(rel, "..") {
			// out-of-tree; drop
			continue
		}
		out = append(out, d)
	}
	return out
}

// sanitizeVMName turns a VM name into a filename-safe
// component. VM names are already libvirt-validated
// (alphanumeric + `_-.`), but the runner is the last line
// of defence — slash would let a hand-crafted VM escape
// the target dir.
func sanitizeVMName(name string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", "..", "_", " ", "_")
	return r.Replace(name)
}

// xmlEscape is a tiny escape for the placeholder domain.xml.
// Only used when r.xmlSource is nil (test path) — the libvirt
// connector is the only production caller.
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// allocateOutputPath picks a unique (filename, fullPath) inside
// tgtDir for one archive in a multi-file run. The naming
// scheme is "webkvm-<host>-<UTC-nano>-<rand6hex>-<name>.tar.zst"
// where the random suffix is shared across all files in a run
// (passed in as `runSuffix`) and `<name>` distinguishes the
// per-VM tars from the config tar. O_EXCL creation; retry on
// EEXIST (extremely unlikely — only the `<name>` would
// collide, and VM names are unique per host).
//
// The pre-create matters: the runner needs to fail fast on
// permission/ROFS errors before invoking tar, so the user sees
// "create: read-only file system" rather than the producer's
// mid-write failure.
func (r *Runner) allocateOutputPath(tgtDir, host, tsNano, runSuffix, name string) (string, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		// The per-attempt variation is the run-suffix; if
		// it ever collides we don't change it (the whole
		// run is one suffix), we just retry the O_EXCL. In
		// practice the only EEXIST here would be a
		// previously-aborted run that left a file with the
		// same name on disk — extremely unlikely with the
		// nanosecond + random suffix in play.
		filename := fmt.Sprintf("webkvm-%s-%s-%s-%s", host, tsNano, runSuffix, name)
		outPath := filepath.Join(tgtDir, filename)
		f, err := os.OpenFile(outPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return filename, outPath, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", outPath, err
		}
	}
	return "", "", fmt.Errorf("could not allocate a unique output path in %s after 16 attempts", tgtDir)
}

// randHex returns a hex string of n bytes (2n chars) of
// cryptographic random data. Used for filename uniqueness in
// allocateOutputPath.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := cryptorand.Read(b); err != nil {
		// crypto/rand failure is exceptional; fall back to
		// time-based pseudo-randomness so the function never
		// returns the empty string.
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// applyRetention was removed in the manual-cleanup rewrite.
// Backups persist on disk until the operator deletes them via
// the Files tab on the Backup page. The implementation used to
// live here and was tied to Target.RetentionCount/RetentionDays,
// which no longer exist.

// ListBackupsOnTarget is a convenience for the UI: lists every
// backup archive in the target's path, newest first.
//
// Two extensions are accepted:
//   - .tar.gz  → Phase I and earlier (gzip). Operators may still
//                have old archives on disk from before Phase II.
//   - .tar.zst → Phase II (zstd, the default since the producer
//                rewrite). One per VM plus one config tar per run.
//
// A previous version of this function filtered on .tar.gz only,
// which silently hid every Phase II archive from the Files tab
// and made the UI say "No backup files yet" even though the
// per-VM tars were on disk. The "looks empty" report is what
// surfaced this bug.
func ListBackupsOnTarget(tgt Target) ([]BackupFile, error) {
	if tgt.Type == TargetSFTP {
		return sftpList(tgt)
	}
	entries, err := os.ReadDir(tgt.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]BackupFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tar.zst") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupFile{
			TargetID: tgt.ID,
			Filename: e.Name(),
			Size:     info.Size(),
			Modified: info.ModTime().UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// retentionRun groups a backup run's suffix with the newest
// modified time among its files and whether it is deletable.
type retentionRun struct {
	Suffix     string
	NewestTime time.Time
}

// ApplyRetention prunes old backup runs from a target according to
// its RetentionPolicy. It is safe by construction:
//
//   - Runs are removed as whole units (DeleteBackupRun), never
//     individual files, so a partial/in-progress run is never left
//     in a torn state.
//   - The config/ subdirectory and its "latest" snapshot are never
//     touched (they carry the app state, not a dated run).
//   - Runs without a parseable suffix (legacy files, foreign files)
//     are ignored, never deleted.
//   - KeepLast and KeepDays are OR-ed: a run survives if either rule
//     says to keep it. With both disabled, nothing is deleted.
//
// It returns the number of runs removed.
func ApplyRetention(store *Store, tgt Target) (int, error) {
	if !tgt.Retention.Enabled() {
		return 0, nil
	}
	files, err := ListBackupsOnTarget(tgt)
	if err != nil {
		return 0, err
	}
	// Group files into runs by their suffix, tracking each run's
	// newest file time and total size.
	byRun := map[string]retentionRun{}
	for _, f := range files {
		suf := runSuffixFromFilename(f.Filename)
		if suf == "" {
			continue // legacy / foreign file: never managed
		}
		rr := byRun[suf]
		rr.Suffix = suf
		if f.Modified.After(rr.NewestTime) {
			rr.NewestTime = f.Modified
		}
		byRun[suf] = rr
	}
	if len(byRun) == 0 {
		return 0, nil
	}

	// Sort runs newest-first for deterministic KeepLast handling.
	runs := make([]retentionRun, 0, len(byRun))
	for _, rr := range byRun {
		runs = append(runs, rr)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].NewestTime.After(runs[j].NewestTime) })

	now := time.Now().UTC()
	removed := 0
	for i, rr := range runs {
		// KeepLast: keep the N newest runs.
		keepByLast := tgt.Retention.KeepLast > 0 && i < tgt.Retention.KeepLast
		// KeepDays: keep runs newer than N days.
		keepByDays := tgt.Retention.KeepDays > 0 &&
			now.Sub(rr.NewestTime) <= time.Duration(tgt.Retention.KeepDays)*24*time.Hour
		if keepByLast || keepByDays {
			continue
		}
		if _, derr := DeleteBackupRun(tgt, rr.Suffix); derr != nil {
			return removed, derr
		}
		removed++
	}
	return removed, nil
}

// runSuffixFromFilename extracts the "<ts26>-<rand6|12>" run suffix
// from a backup filename, or "" if the name doesn't carry one.
func runSuffixFromFilename(name string) string {
	if !ValidBackupFilename(name) {
		return ""
	}
	// Shape: webkvm-<host>-<ts26>-<rand6|12>-<name>.tar.(gz|zst)
	// The suffix is the third dash-separated field after "webkvm-<host>".
	rest := strings.TrimPrefix(name, "webkvm-")
	parts := strings.SplitN(rest, "-", 3)
	if len(parts) < 3 {
		return ""
	}
	// parts[0]=host, parts[1]=ts26, parts[2]=rand6...-name.tar.zst
	ts := parts[1]
	third := parts[2]
	idx := strings.Index(third, "-")
	if idx < 0 {
		return ""
	}
	randPart := third[:idx]
	if !isNanoTimestamp(ts) || !isRandomSuffix(randPart) {
		return ""
	}
	return ts + "-" + randPart
}

// BackupFile is one archive on disk.
type BackupFile struct {
	TargetID string    `json:"target_id"`
	Filename string    `json:"filename"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// Sha256 is filled by VerifyBackup if called.
	Sha256 string `json:"sha256,omitempty"`
}

// VerifyBackup reads the archive and computes its sha256 (cheap;
// just a read pass). The result is returned on the BackupFile and
// also returned to the caller for any further use.
func VerifyBackup(tgt Target, filename string) (BackupFile, error) {
	if tgt.Type == TargetSFTP {
		return sftpVerify(tgt, filename)
	}
	path := filepath.Join(tgt.Path, filename)
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(tgt.Path)+string(os.PathSeparator)) {
		return BackupFile{}, fmt.Errorf("invalid backup path %q", filename)
	}
	f, err := os.Open(path)
	if err != nil {
		return BackupFile{}, err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	info, err := os.Stat(path) // lgtm[go/path-injection] - path validated above to be within tgt.Path
	if err != nil {
		return BackupFile{}, err
	}
	return BackupFile{
		TargetID: tgt.ID,
		Filename: filename,
		Size:     info.Size(),
		Modified: info.ModTime().UTC(),
		Sha256:   hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// RestorePlan describes what the restore will produce before
// any bytes are written. The HTTP handler returns it as JSON
// so the operator sees "you're about to extract 3 archives
// into /opt/webkvm/restore-<ts>/" before clicking confirm.
type RestorePlan struct {
	// Destination is the top-level restore directory.
	Destination string `json:"destination"`
	// Files is the per-archive restore plan. Each entry names
	// the source file and the subdirectory the contents go
	// into. SourceKind is "config" or "vm" (Phase II only);
	// empty for the legacy single-file restores.
	Files []RestoreFilePlan `json:"files"`
}

// RestoreFilePlan is one archive's restore plan.
type RestoreFilePlan struct {
	Filename   string `json:"filename"`
	Subdir     string `json:"subdir"` // path under Destination
	SourceKind string `json:"source_kind,omitempty"`
	VMID       string `json:"vm_id,omitempty"`
	Size       int64  `json:"size"`
}

// RestoreResult is what RestoreRun / RestoreBackup return.
// Destination is the top-level restore directory; Files
// lists the per-archive results (the subdir + the manifest).
type RestoreResult struct {
	Destination string                   `json:"destination"`
	Files       []RestoreFileResult      `json:"files"`
}

// RestoreFileResult is one archive's restore outcome.
type RestoreFileResult struct {
	Filename string `json:"filename"`
	Subdir   string `json:"subdir"`
	// Manifest is the path of the per-archive manifest
	// file the restore writes (so the operator can see
	// what came out without re-walking the tree).
	Manifest string `json:"manifest"`
}

// RestoreBackup extracts a single archive by name. Kept for
// the single-file restore path (legacy / Phase I / per-VM
// pick). For multi-file restores use RestoreRun. Returns
// the per-archive RestoreFileResult; Destination in the
// returned RestoreResult is the directory the archive's
// contents were extracted into.
//
// Guards (A2 fix for bug #8, extended in Phase II):
//
//  1. The target's path is validated against the deny-list.
//  2. The source archive's compressed size is checked
//     up-front (MaxRestoreSourceBytes).
//  3. The tar command runs under a context timeout
//     (MaxRestoreDuration).
//  4. After extraction, the size of the restored tree is
//     summed and capped (MaxRestoreExtractedBytes). The
//     zip-bomb defence.
//
// All guards surface as ErrTargetPathUnwritable or a wrapped
// error; the HTTP handler maps the sentinel to 400 and the
// rest to 500.
func RestoreBackup(ctx context.Context, tgt Target, filename, dataDir string) (RestoreResult, error) {
	if err := ValidateTargetPath(tgt.Path, dataDir); err != nil {
		return RestoreResult{}, err
	}
	if !ValidBackupFilename(filename) {
		return RestoreResult{}, fmt.Errorf("invalid filename %q: expected webkvm-<host>-<UTC>[...].tar.{gz,zst}", filename)
	}
	return RestoreRun(ctx, tgt, /*runSuffix=*/ "", dataDir, []string{filename})
}

// RestoreRun extracts every archive in the named run. The
// run is identified either by a filename (legacy single-file
// restore) or by a run-suffix ("<UTC>-<rand>") that names
// every file produced by a single backup run. In the run
// case, every file matching the suffix is extracted.
//
// The output layout is:
//
//   dataDir/restore-<ts>/<subdir>/...   (one subdir per file)
//
// where <subdir> is "config" for the app-state tar and the
// VM's name for each per-VM tar. For legacy single-file
// restores the subdir is the file's basename without the
// extension.
//
// The function is idempotent at the file level: re-running
// a restore overwrites the same directory. It is NOT
// idempotent at the archive level: an interrupted restore
// leaves a partial directory and a re-run will see a
// different "<ts>" name (so a re-run is always fresh).
func RestoreRun(ctx context.Context, tgt Target, runSuffix, dataDir string, filenames []string, onProgress ...func(int, string, map[string]any)) (RestoreResult, error) {
	var progress func(int, string, map[string]any)
	if len(onProgress) > 0 {
		progress = onProgress[0]
	}
	if tgt.Type == TargetSFTP {
		staging, picks, err := stageSFTPFiles(tgt, runSuffix, filenames, dataDir)
		if err != nil {
			return RestoreResult{}, err
		}
		defer os.RemoveAll(staging)
		tgt.Path = staging
		runSuffix = ""
		filenames = picks
	}
	if err := ValidateTargetPath(tgt.Path, dataDir); err != nil {
		return RestoreResult{}, err
	}
	if runSuffix == "" && len(filenames) == 0 {
		return RestoreResult{}, errors.New("restore: must specify run or filenames")
	}
	// Discover files: either the explicit list (legacy
	// single-file restore) or all files in the target dir
	// whose name contains the run-suffix.
	files, err := pickRestoreFiles(tgt, runSuffix, filenames)
	if err != nil {
		return RestoreResult{}, err
	}
	if len(files) == 0 {
		return RestoreResult{}, fmt.Errorf("restore: no files matched run=%q", runSuffix)
	}
	// One ts for the whole restore so the operator can
	// find it under dataDir/restore-<ts>/.
	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	destination := filepath.Join(dataDir, "restore-"+ts)
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return RestoreResult{}, err
	}
	res := RestoreResult{Destination: destination}
	if progress != nil {
		progress(1, "restore_preparing", nil)
	}
	for i, f := range files {
		subdir, kind, vmid := restoreSubdirFor(f.name)
		subPath := filepath.Join(destination, subdir)
		if err := os.MkdirAll(subPath, 0o755); err != nil {
			return res, err
		}
		if progress != nil {
			progress(2+int(float64(i)/float64(len(files))*95),
				"restore_extract",
				map[string]any{"i": i + 1, "n": len(files), "name": f.name})
		}
		manifest, err := extractOne(ctx, tgt, f, subPath)
		if err != nil {
			// A failure on one file aborts the whole
			// restore and removes the run directory so
			// the operator doesn't end up with a
			// half-extracted tree.
			_ = os.RemoveAll(destination)
			return RestoreResult{}, err
		}
		res.Files = append(res.Files, RestoreFileResult{
			Filename: f.name,
			Subdir:   subdir,
			Manifest: manifest,
		})
		_ = kind
		_ = vmid
	}
	// Run-level manifest.
	type runEntry struct {
		Filename string `json:"filename"`
		Subdir   string `json:"subdir"`
		Size     int64  `json:"size"`
	}
	var entries []runEntry
	for i, f := range files {
		entries = append(entries, runEntry{
			Filename: f.name,
			Subdir:   res.Files[i].Subdir,
			Size:     f.size,
		})
	}
	mb, _ := json.MarshalIndent(entries, "", "  ")
	_ = os.WriteFile(filepath.Join(destination, "RESTORE_MANIFEST.json"), mb, 0o644)
	return res, nil
}

// pickRestoreFiles returns the list of files to extract.
// In run-suffix mode it scans the target's directory and
// keeps every file whose name contains the suffix. In
// filenames mode it just returns the list after validation.
type restoreFile struct {
	name string
	size int64
}

func pickRestoreFiles(tgt Target, runSuffix string, filenames []string) ([]restoreFile, error) {
	if runSuffix != "" {
		// Sanitise the runSuffix. We accept only the
		// "ts-rand" portion of a filename (the middle),
		// matched by the same regex used elsewhere.
		if !isRunSuffix(runSuffix) {
			return nil, fmt.Errorf("restore: invalid run suffix %q", runSuffix)
		}
		entries, err := os.ReadDir(tgt.Path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("target path %s does not exist", tgt.Path)
			}
			return nil, err
		}
		var out []restoreFile
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.Contains(name, runSuffix) {
				continue
			}
			if !ValidBackupFilename(name) {
				// Skip files that look like backups but
				// don't pass the format check.
				continue
			}
			info, _ := e.Info()
			out = append(out, restoreFile{name: name, size: info.Size()})
		}
		return out, nil
	}
	// filenames mode.
	var out []restoreFile
	for _, n := range filenames {
		if !ValidBackupFilename(n) {
			return nil, fmt.Errorf("invalid filename %q", n)
		}
		info, err := os.Stat(filepath.Join(tgt.Path, n))
		if err != nil {
			return nil, err
		}
		out = append(out, restoreFile{name: n, size: info.Size()})
	}
	return out, nil
}

// restoreSubdirFor maps a filename to (subdir, kind, vm_id).
// Phase II: the subdir is the <name> component (config or
// the VM name). Legacy: the subdir is the filename without
// extension.
func restoreSubdirFor(name string) (string, string, string) {
	// Try the Phase II pattern first: webkvm-<host>-<ts>-<rand>-<name>.tar.zst
	// The <name> is whatever's between the last <rand>- and the .tar.zst.
	if strings.HasSuffix(name, ".tar.zst") {
		// last 6 chars (before .tar.zst) are the rand.
		// The name is whatever's between the char after
		// the last '-' before the rand and the .tar.zst.
		base := strings.TrimSuffix(name, ".tar.zst")
		// base ends in 6-char rand; walk back to find
		// the '-' before it.
		if len(base) >= 7 && base[len(base)-7] == '-' && isRandomSuffix(base[len(base)-6:]) {
			nameStart := len(base) - 6 - 1 // position of '-'
			// The 26-char ts precedes the rand: find
			// the '-' before it and the part between
			// the two separators is the <name>.
			if nameStart >= 27 && base[nameStart-27] == '-' && isNanoTimestamp(base[nameStart-26:nameStart]) {
				subdir := base[nameStart+1:]
				kind := "vm"
				vmid := subdir
				if subdir == "config" {
					kind = "config"
					vmid = ""
				}
				return subdir, kind, vmid
			}
		}
	}
	// Legacy: the subdir is the basename without extension.
	subdir := strings.TrimSuffix(name, ".tar.gz")
	if subdir == name {
		subdir = strings.TrimSuffix(name, ".tar.zst")
	}
	return subdir, "", ""
}

// isRunSuffix validates a "<ts26>-<randHex>" string. The
// validator is the same as the one we apply to the
// timestamp + rand components in validFilenameRegexes.
//
// randHex can be 6 or 12 chars (randHex(3) and randHex(6)
// in the runner both shipped; the regex accepts both
// for retro-compat — see the comment on
// validFilenameRegexes in store.go).
func isRunSuffix(s string) bool {
	// "<ts26>-<rand6>" is 33 chars; "<ts26>-<rand12>" is 39.
	if len(s) != 33 && len(s) != 39 {
		return false
	}
	if !isNanoTimestamp(s[:26]) {
		return false
	}
	if s[26] != '-' {
		return false
	}
	return isRandomSuffix(s[27:])
}

// extractOne is the per-archive restore primitive. It
// applies the size cap, runs tar with a timeout, and
// writes a per-archive manifest. The destination is the
// subdir already created by RestoreRun.
//
// Tar is invoked with the right decompression flag based
// on the file extension. .tar.gz uses the built-in -z;
// .tar.zst requires GNU tar >= 1.31 with --zstd (the
// default in most modern distros) or piping through zstd.
// We use --zstd when available; otherwise we pipe through
// the zstd CLI. We pick whichever is on PATH to keep
// the restore self-contained.
func extractOne(ctx context.Context, tgt Target, f restoreFile, destDir string) (string, error) {
	src := filepath.Join(tgt.Path, f.name)
	if MaxRestoreSourceBytes > 0 && f.size > MaxRestoreSourceBytes {
		return "", fmt.Errorf("source archive %s is %d bytes, exceeds cap of %d", f.name, f.size, MaxRestoreSourceBytes)
	}
	tctx, cancel := context.WithTimeout(ctx, MaxRestoreDuration)
	defer cancel()
	cmd, err := buildExtractCmd(tctx, src, destDir, f.name)
	if err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(destDir)
		if tctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("tar %s: timeout after %v: %s", f.name, MaxRestoreDuration, string(out))
		}
		return "", fmt.Errorf("tar %s: %v: %s", f.name, err, string(out))
	}
	// Post-extract size cap (zip-bomb defence).
	var total int64
	_ = filepath.Walk(destDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	if MaxRestoreExtractedBytes > 0 && total > MaxRestoreExtractedBytes {
		_ = os.RemoveAll(destDir)
		return "", fmt.Errorf("extracted %s: size %d bytes, exceeds cap of %d (zip-bomb guard)",
			f.name, total, MaxRestoreExtractedBytes)
	}
	// Per-archive manifest.
	type fileEntry struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	var files []fileEntry
	_ = filepath.Walk(destDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		files = append(files, fileEntry{Path: p, Size: info.Size()})
		return nil
	})
	mb, _ := json.MarshalIndent(files, "", "  ")
	manifest := filepath.Join(destDir, "RESTORE_MANIFEST.json")
	_ = os.WriteFile(manifest, mb, 0o644)
	return manifest, nil
}

// buildExtractCmd assembles the right tar command for the
// archive at src. The decision tree:
//
//   .tar.gz  → tar -xzf (built-in gzip)
//   .tar.zst → if tar supports --zstd, use it directly;
//              otherwise pipe through `zstd -d` into `tar -x`
//
// We probe tar's --zstd support by trying `tar --zstd --help`
// once and caching the result (in a process-wide variable)
// to keep the restore path hot. A tar without --zstd is rare
// in 2024+ but the fallback keeps us honest.
var tarSupportsZstd *bool

func buildExtractCmd(ctx context.Context, src, destDir, name string) (*exec.Cmd, error) {
	if strings.HasSuffix(name, ".tar.gz") {
		return exec.CommandContext(ctx, "tar", "-xzf", src, "-C", destDir), nil
	}
	if strings.HasSuffix(name, ".tar.zst") {
		if tarSupportsZstd == nil {
			test := exec.Command("tar", "--zstd", "--help")
			ok := test.Run() == nil
			tarSupportsZstd = &ok
		}
		if *tarSupportsZstd {
			return exec.CommandContext(ctx, "tar", "--zstd", "-xf", src, "-C", destDir), nil
		}
		// Fallback: pipe zstd -d into tar -x. This is a
		// two-process pipeline; the timeout applies to the
		// whole pipeline via the context.
		zstd, err := exec.LookPath("zstd")
		if err != nil {
			return nil, fmt.Errorf("zstd compression detected but neither tar --zstd nor zstd CLI is available: %w", err)
		}
		zw := exec.CommandContext(ctx, zstd, "-d", "-c", src)
		tw := exec.CommandContext(ctx, "tar", "-x", "-C", destDir)
		tw.Stdin, _ = zw.StdoutPipe()
		// CombinedOutput-style: collect stderr from both.
		zw.Stderr = os.Stderr
		tw.Stderr = os.Stderr
		if err := zw.Start(); err != nil {
			return nil, err
		}
		if err := tw.Start(); err != nil {
			_ = zw.Wait()
			return nil, err
		}
		// We can't return a single Cmd that runs both, so
		// we wrap the pair in a custom type. Easier: just
		// return the tar and let the caller wait on both.
		// For now, fall back to: invoke zstd -d on the
		// source into a temp file, then tar -xf that file.
		// This is slower (one extra file write) but
		// doesn't need a custom command type. The temp file
		// is created next to the extraction destination (a
		// real disk, e.g. dataDir/restore-<ts>) — never the
		// system temp dir (/tmp is often a tiny tmpfs that
		// overflows on multi-GB restores).
		tmp, err := os.CreateTemp(filepath.Dir(destDir), "webkvm-restore-*.tar")
		if err != nil {
			return nil, err
		}
		tmpPath := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // discard; we'll re-create
		zstdCmd := exec.CommandContext(ctx, zstd, "-dk", "-o", tmpPath, src)
		if out, err := zstdCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("zstd -d: %v: %s", err, string(out))
		}
		return exec.CommandContext(ctx, "tar", "-xf", tmpPath, "-C", destDir), nil
	}
	return nil, fmt.Errorf("unsupported archive extension for %s", name)
}

// Caps for restore. Sourced as vars (not consts) so tests can
// dial them down to small values without rebuilding the world.
// Defaults:
//   - Source archive: 100 GB compressed. A real backup of a
//     50 GB VM compresses to 10-30 GB; 100 GB is plenty.
//   - Extracted: 500 GB. A restore that fills a 500 GB tree
//     is almost certainly wrong; refuse before the disk fills.
//   - Duration: 30 min. A tar -xzf on local disk completes
//     in seconds; the cap is the "NFS got slow" backstop.
var (
	MaxRestoreSourceBytes    int64         = 100 << 30 // 100 GiB
	MaxRestoreExtractedBytes int64         = 500 << 30 // 500 GiB
	MaxRestoreDuration       time.Duration = 30 * time.Minute
)
