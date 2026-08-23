// Package backupstore is the v2 backup system: a registry of
// targets (local dir / NFS / SMB / S3-like) and a registry of
// schedules (cron expressions) that fire jobs against a target.
//
// The original backup feature (Phase 1.3) was a single hardcoded
// SMB mount and a single on-demand "Backup now" button. Backup
// v2 replaces that with a configurable multi-target, scheduled,
// retainable, restorable system. The Phase 1.3 endpoints remain
// for backward compatibility but now use a "default" target
// (auto-created from BACKUP_DEFAULT_TARGET setting or
// /mnt/webkvm-backup if nothing is set).
package backupstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// TargetType categorizes a backup destination.
type TargetType string

const (
	TargetLocal TargetType = "local" // a local directory
	TargetNFS   TargetType = "nfs"   // NFS mount, path is the local mountpoint
	TargetSMB   TargetType = "smb"   // SMB/CIFS mount, path is the local mountpoint
	TargetSFTP  TargetType = "sftp"  // remote SFTP upload; host/username + secrets
)

// TargetOptions carries the connection details for remote targets
// (currently only SFTP). Passwords/keys never live here — they are
// stored in the Store's secrets file keyed by target ID. The API
// request model maps onto this before calling the store.
type TargetOptions struct {
	Host       string
	Port       int
	Username   string
	Password   string // stored in the secrets file, never serialized
	SSHKeyPath string // optional absolute path to an SSH private key on the host
	ClearSecret bool  // when true with no credentials, removes the stored secret
	// Retention sets the automatic cleanup policy. Zero value (both
	// fields 0) means "keep everything" (manual cleanup).
	Retention RetentionPolicy
}

// TargetSecret is the credential material for a remote target,
// persisted separately from targets.json (0600) so the password is
// never written into the config backup tar.
type TargetSecret struct {
	Password   string `json:"password,omitempty"`
	SSHKeyPath string `json:"ssh_key_path,omitempty"`
}

// Target describes one backup destination.
type Target struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Type       TargetType `json:"type"`
	// Path is the local directory under which backups are written.
	// For NFS/SMB the operator is expected to have the mount set
	// up in /etc/fstab (e.g. via the storage netfs pool); the
	// scheduler writes to this path and lets the kernel's mount
	// do the work. For SFTP it is the remote directory.
	Path string `json:"path"`
	// Host/Port/Username are only meaningful for TargetSFTP.
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	// Secret holds the credentials for remote targets. It is
	// populated from the Store's secrets file and is never
	// serialized to the API (json:"-").
	Secret TargetSecret `json:"-"`
	// VMFilter selects which VMs are included in the backup.
	//   "all"     → back up every VM on the host (default).
	//   "include" → back up only the VMs in VMIDs.
	//   "exclude" → back up every VM except the ones in VMIDs.
	// Empty string is treated as "all" for backward compatibility
	// with targets written before this field existed.
	VMFilter string `json:"vm_filter"`
	// VMIDs is the list referenced by VMFilter. Ignored when
	// VMFilter is "all" or empty.
	VMIDs []string `json:"vm_ids,omitempty"`
	// Enabled lets the operator pause a target without removing
	// it. Disabled targets still appear in the UI but neither
	// manual backups nor scheduled runs touch them.
	Enabled   bool      `json:"enabled"`
	// Retention controls automatic cleanup of old runs after a
	// successful backup. Both fields are optional:
	//   - KeepLast: keep only the N most recent runs (0 = unlimited).
	//   - KeepDays: keep runs newer than N days (0 = unlimited).
	// When both are set, a run is kept if it satisfies either rule.
	// Disabled (both zero) keeps everything (manual cleanup only),
	// which is the current default.
	Retention RetentionPolicy `json:"retention,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RetentionPolicy controls automatic cleanup of old backup runs.
type RetentionPolicy struct {
	// KeepLast keeps the N most recent runs (0 = unlimited).
	KeepLast int `json:"keep_last,omitempty"`
	// KeepDays keeps runs newer than N days (0 = unlimited).
	KeepDays int `json:"keep_days,omitempty"`
}

// Enabled reports whether any retention rule is configured.
func (r RetentionPolicy) Enabled() bool {
	return r.KeepLast > 0 || r.KeepDays > 0
}

// Schedule fires a backup against a target on a cron expression.
type Schedule struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Cron       string    `json:"cron"`        // standard 5-field cron
	TargetID   string    `json:"target_id"`
	Enabled    bool      `json:"enabled"`
	LastRunAt  time.Time `json:"last_run_at,omitempty"`
	LastStatus string    `json:"last_status,omitempty"` // success | error
	LastError  string    `json:"last_error,omitempty"`
	NextRunAt  time.Time `json:"next_run_at,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Job records a single backup execution.
type Job struct {
	ID         string    `json:"id"`
	ScheduleID string    `json:"schedule_id,omitempty"` // empty for manual
	TargetID   string    `json:"target_id"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
	// Filename is the primary archive for this run. In Phase II
	// it points at the config tar (the one file that always
	// exists). Kept as a string (singular) for backward compat
	// with the Jobs tab UI that already renders "filename".
	Filename string `json:"filename,omitempty"`
	// Size is the total bytes written across all files in
	// this run, not just the primary one. The UI's "size"
	// column sums the per-file sizes; the Job-level value is
	// for the audit log and for ad-hoc operators.
	Size int64 `json:"size,omitempty"`
	// Filenames lists every archive written by this run. For
	// Phase II this is at least one entry (the config tar)
	// plus one per in-scope VM. The Files field carries the
	// per-file kind/size/vm_id detail.
	Filenames []string `json:"filenames,omitempty"`
	Files     []JobFile `json:"files,omitempty"`
	Status    string    `json:"status"` // running | success | error
	Error     string    `json:"error,omitempty"`
	// Progress 0-100 and a human-readable Message are updated
	// while the job runs so the UI can render a live progress bar.
	Progress int    `json:"progress,omitempty"`
	Message  string `json:"message,omitempty"`
	// Stage is a stable phase code (preparing, compress_vm, config,
	// uploading, restore_copying, done, failed...) the UI translates
	// into the active locale instead of showing backend text. StageVars
	// carries the interpolation values (VM name, counters, path).
	Stage     string         `json:"stage,omitempty"`
	StageVars map[string]any `json:"stage_vars,omitempty"`
	// VMID / VMName carry the result of a restore-as-VM job so the
	// UI can navigate to the freshly imported VM on completion.
	VMID   string `json:"vm_id,omitempty"`
	VMName string `json:"vm_name,omitempty"`
	// Destination is where a bulk restore (run / config / files)
	// extracted the archives to, e.g. dataDir/restore-<ts>. Shown
	// in the notification when the job completes.
	Destination string `json:"destination,omitempty"`
}

// JobFile is one archive in a multi-file backup run. The Kind
// tells the restore UI which kind of archive this is: "config"
// for the app-state tar, "vm" for a per-VM WebKVM archive.
type JobFile struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Kind     string `json:"kind"`            // "config" | "vm"
	VMID     string `json:"vm_id,omitempty"` // empty for Kind=="config"
}

// Store is the on-disk + in-memory registry for targets, schedules, and jobs.
type Store struct {
	mu        sync.RWMutex
	dataDir   string
	targets   map[string]*Target
	schedules map[string]*Schedule
	jobs      map[string]*Job
	secrets   map[string]TargetSecret
}

// File is the on-disk shape. The store is split into three files so
// we don't have to rewrite the job log on every target mutation.
type fileTargets struct {
	Version int      `json:"version"`
	Targets []*Target `json:"targets"`
}
type fileSchedules struct {
	Version   int         `json:"version"`
	Schedules []*Schedule `json:"schedules"`
}
type fileJobs struct {
	Version int   `json:"version"`
	Jobs    []*Job `json:"jobs"`
}

// New loads (or creates) the backup store at {dataDir}/backup/.
// Creates a "default" local target at {dataDir}/backups/ if missing.
func New(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "backup")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dataDir:   dir,
		targets:   map[string]*Target{},
		schedules: map[string]*Schedule{},
		jobs:      map[string]*Job{},
		secrets:   map[string]TargetSecret{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	sweepStuckJobs(s)
	s.ensureDefault()
	if err := s.loadSecrets(); err != nil {
		return nil, err
	}
	if err := s.saveTargets(); err != nil {
		return nil, err
	}
	if err := s.saveSchedules(); err != nil {
		return nil, err
	}
	return s, nil
}

// sweepStuckJobs marks every job left in "running" state on disk
// as errored. The runner records the job as "running" before
// kicking off tar and updates it to "success" / "error" once the
// command returns. A process crash, OOM kill, or `kill -9` between
// those two updates leaves the job stuck — and the UI shows it as
// "Running" forever. We sweep those on every New() so the next
// restart recovers automatically.
//
// A defensive sub-case is also swept: a job with status="running"
// AND a non-zero EndedAt is contradictory (EndedAt is set on
// terminal status only). The previous code `continue`d on this
// case, leaving the contradictory record in place forever — a
// silent failure. We now treat it as stuck and normalise it.
func sweepStuckJobs(s *Store) {
	var swept int
	for id, j := range s.jobs {
		if j.Status != "running" {
			continue
		}
		j.Status = "error"
		if j.EndedAt.IsZero() {
			j.EndedAt = time.Now().UTC()
		}
		if j.Error == "" {
			j.Error = "aborted_by_restart: job was running when the backend stopped"
		}
		s.jobs[id] = j
		swept++
	}
	if swept == 0 {
		return
	}
	if err := s.saveJobs(); err != nil {
		// In-memory state is already fixed; the next restart
		// will retry the sweep. Log so the operator knows
		// disk and memory are out of sync.
		slog.Default().Warn("backup_sweep_persist_failed", "err", err, "swept", swept)
	}
}

func (s *Store) load() error {
	for name, ptr := range map[string]any{
		"targets.json":   &fileTargets{},
		"schedules.json": &fileSchedules{},
		"jobs.json":      &fileJobs{},
	} {
		path := filepath.Join(s.dataDir, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, ptr); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		switch v := ptr.(type) {
		case *fileTargets:
			for _, t := range v.Targets {
				s.targets[t.ID] = t
			}
		case *fileSchedules:
			for _, sc := range v.Schedules {
				s.schedules[sc.ID] = sc
			}
		case *fileJobs:
			for _, j := range v.Jobs {
				s.jobs[j.ID] = j
			}
		}
	}
	return nil
}

func (s *Store) ensureDefault() {
	for _, t := range s.targets {
		if t.Name == "default" {
			return
		}
	}
	// Auto-create a local default at {dataDir}/default/. This is
	// the same path the Phase 1.3 /mnt/webkvm-backup used to write
	// to, so existing manual backups remain visible in the UI.
	defaultPath := filepath.Join(s.dataDir, "default")
	_ = os.MkdirAll(defaultPath, 0o755)
	s.targets["default"] = &Target{
		ID:         "default",
		Name:       "default",
		Type:       TargetLocal,
		Path:       defaultPath,
		Enabled:    true,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
}

func (s *Store) saveTargets() error {
	list := make([]*Target, 0, len(s.targets))
	for _, t := range s.targets {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	return saveJSON(filepath.Join(s.dataDir, "targets.json"), fileTargets{Version: 1, Targets: list})
}

func (s *Store) saveSchedules() error {
	list := make([]*Schedule, 0, len(s.schedules))
	for _, sc := range s.schedules {
		list = append(list, sc)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	return saveJSON(filepath.Join(s.dataDir, "schedules.json"), fileSchedules{Version: 1, Schedules: list})
}

func (s *Store) saveJobs() error {
	// Cap the jobs log at 200 entries (newest first) to keep the
	// file bounded. The user can look at backups.json on disk for
	// the full history.
	all := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		all = append(all, j)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].StartedAt.After(all[j].StartedAt) })
	if len(all) > 200 {
		all = all[:200]
	}
	return saveJSON(filepath.Join(s.dataDir, "jobs.json"), fileJobs{Version: 1, Jobs: all})
}

func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- Remote-target secrets ---

// secretsPath is the on-disk credentials file for remote backup
// targets. Kept separate from targets.json (which is included in
// the config backup tar) so passwords never end up inside a
// backup archive. Mode 0600.
func (s *Store) secretsPath() string {
	return filepath.Join(s.dataDir, "sftp-secrets.json")
}

func (s *Store) loadSecrets() error {
	data, err := os.ReadFile(s.secretsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var file struct {
		Version int                      `json:"version"`
		Secrets map[string]TargetSecret `json:"secrets"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse sftp-secrets.json: %w", err)
	}
	for id, sec := range file.Secrets {
		s.secrets[id] = sec
	}
	return nil
}

func (s *Store) saveSecrets() error {
	return saveJSON(s.secretsPath(), struct {
		Version int                      `json:"version"`
		Secrets map[string]TargetSecret `json:"secrets"`
	}{Version: 1, Secrets: s.secrets})
}

// SetTargetSecret stores (or clears) the credentials for a remote
// target. An empty TargetSecret removes the entry.
func (s *Store) SetTargetSecret(id string, sec TargetSecret) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sec.Password == "" && sec.SSHKeyPath == "" {
		delete(s.secrets, id)
	} else {
		s.secrets[id] = sec
	}
	return s.saveSecrets()
}

// getSecret returns the credentials for a remote target. Used by
// the SFTP sink when dialing; the caller is expected to hold a
// copy of the Target already.
func (s *Store) getSecret(id string) (TargetSecret, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sec, ok := s.secrets[id]
	return sec, ok
}

// --- Targets ---

func (s *Store) ListTargets() []Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Target, 0, len(s.targets))
	for _, t := range s.targets {
		c := *t
		if sec, ok := s.secrets[t.ID]; ok {
			c.Secret = sec
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Store) GetTarget(id string) (Target, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.targets[id]
	if !ok {
		return Target{}, false
	}
	c := *t
	if sec, ok := s.secrets[id]; ok {
		c.Secret = sec
	}
	return c, true
}

// CreateTarget registers a new backup destination. vmFilter must be
// one of "all", "include", or "exclude"; an empty string is treated
// as "all". When vmFilter is "include", vmIDs must contain at least
// one VM ID. When vmFilter is "exclude", an empty vmIDs list is
// equivalent to "all".
//
// The path is validated against the deny-list in path_safety.go
// (rejects /etc, /usr, /boot, /proc, /sys, etc.). This is the A2
// fix for bug #6: the previous code did os.MkdirAll(path, 0755)
// without any check, so a target pointing at a system directory
// would happily try to write into it. Validation now happens
// before any I/O so a bad path is rejected with 400.
func (s *Store) CreateTarget(name, path string, ttype TargetType, vmFilter string, vmIDs []string) (Target, error) {
	return s.CreateTargetOpts(name, path, ttype, vmFilter, vmIDs, TargetOptions{})
}

// CreateTargetOpts is CreateTarget plus remote-target connection
// options. For TargetSFTP the opts carry host/port/username and any
// credential material; the path is the remote directory.
func (s *Store) CreateTargetOpts(name, path string, ttype TargetType, vmFilter string, vmIDs []string, opts TargetOptions) (Target, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Target{}, errors.New("name is required")
	}
	if path == "" {
		return Target{}, errors.New("path is required")
	}
	if ttype == TargetSFTP {
		// The path for an SFTP target is a remote directory, not a
		// local one — skip the local deny-list/creation checks.
		if opts.Host == "" {
			return Target{}, errors.New("host is required for sftp targets")
		}
		if opts.Username == "" {
			return Target{}, errors.New("username is required for sftp targets")
		}
		if opts.Password == "" && opts.SSHKeyPath == "" {
			return Target{}, errors.New("a password or ssh key path is required for sftp targets")
		}
	} else {
		if err := ValidateTargetPath(path, s.dataDir); err != nil {
			return Target{}, err
		}
		switch ttype {
		case TargetLocal, TargetNFS, TargetSMB:
		case "":
			ttype = TargetLocal
		default:
			return Target{}, fmt.Errorf("unsupported target type %q", ttype)
		}
	}
	filter, err := normalizeVMFilter(vmFilter)
	if err != nil {
		return Target{}, err
	}
	if filter == "include" && len(vmIDs) == 0 {
		return Target{}, errors.New("vm_filter=include requires at least one vm_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.targets {
		if t.Name == name {
			return Target{}, fmt.Errorf("target %q already exists", name)
		}
	}
	id := newID("t")
	port := opts.Port
	if port == 0 {
		port = 22
	}
	t := &Target{
		ID:        id,
		Name:      name,
		Type:      ttype,
		Path:      path,
		VMFilter:  filter,
		VMIDs:     vmIDs,
		Enabled:   true,
		Host:      opts.Host,
		Port:      port,
		Username:  opts.Username,
		Retention: opts.Retention,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	s.targets[id] = t
	if ttype == TargetSFTP {
		if opts.Password != "" || opts.SSHKeyPath != "" {
			s.secrets[id] = TargetSecret{Password: opts.Password, SSHKeyPath: opts.SSHKeyPath}
			if err := s.saveSecrets(); err != nil {
				delete(s.targets, id)
				return Target{}, err
			}
		}
	} else {
		if err := os.MkdirAll(path, 0o755); err != nil { // lgtm[go/path-injection] - path validated via ValidateTargetPath above
			delete(s.targets, id)
			return Target{}, fmt.Errorf("create path: %w", err)
		}
	}
	if err := s.saveTargets(); err != nil {
		delete(s.targets, id)
		return Target{}, err
	}
	return *t, nil
}

// normalizeVMFilter canonicalizes the filter string. Empty maps to
// "all"; anything else must be one of the three known values.
func normalizeVMFilter(f string) (string, error) {
	switch strings.TrimSpace(f) {
	case "", "all", "include", "exclude":
		return f, nil
	default:
		return "", fmt.Errorf("vm_filter must be one of all|include|exclude, got %q", f)
	}
}

// UpdateTarget patches an existing target. Every field is a
// pointer so the caller distinguishes "don't change" (nil)
// from "set to the zero value" (*x). This is the A4 fix for
// bug #7 — the previous code mixed two conventions:
//
//   * empty string / nil pointer  → "don't change"
//   * non-empty / non-nil         → "set to this"
//
// A future "edit target" UI building on the same pattern would
// have to choose between "send a partial update" and "send
// every field" and would inevitably get it wrong on the
// boolean/zero-value fields (Enabled=false is a legitimate
// value, but indistinguishable from "don't change" under the
// old convention).
//
// The handler always uses pointers; the JSON omits a field by
// leaving it nil. Setting it to "" or false is explicit. The
// "default" target's Path is still pinned, matching the
// CreateTarget auto-bootstrap behaviour.
func (s *Store) UpdateTarget(
	id string,
	name, path *string,
	ttype *TargetType,
	vmFilter *string,
	vmIDs *[]string,
	enabled *bool,
	opts *TargetOptions,
) (Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[id]
	if !ok {
		return Target{}, ErrTargetNotFound
	}
	if id == "default" && path != nil && *path != t.Path {
		return Target{}, errors.New("cannot change the path of the default target")
	}
	if name != nil {
		t.Name = strings.TrimSpace(*name)
	}
	if path != nil {
		if t.Type == TargetSFTP {
			t.Path = *path
		} else {
			if err := ValidateTargetPath(*path, s.dataDir); err != nil {
				return Target{}, err
			}
			t.Path = *path
		}
	}
	if ttype != nil {
		t.Type = *ttype
	}
	if opts != nil {
		if opts.Host != "" {
			t.Host = opts.Host
		}
		if opts.Port != 0 {
			t.Port = opts.Port
		}
		if opts.Username != "" {
			t.Username = opts.Username
		}
		// Credential updates: non-empty sets the value, empty string
		// explicitly clears it. A nil-ptr on the request side is
		// "don't touch" and is resolved by the caller to a *string.
		if opts.Password != "" || opts.SSHKeyPath != "" {
			s.secrets[id] = TargetSecret{Password: opts.Password, SSHKeyPath: opts.SSHKeyPath}
			if err := s.saveSecrets(); err != nil {
				return Target{}, err
			}
		} else if opts.ClearSecret {
			delete(s.secrets, id)
			if err := s.saveSecrets(); err != nil {
				return Target{}, err
			}
		}
		t.Retention = opts.Retention
	}
	if vmFilter != nil {
		filter, err := normalizeVMFilter(*vmFilter)
		if err != nil {
			return Target{}, err
		}
		// "include" with an empty list would silently demote the
		// target to "all", which is misleading. Refuse it.
		if filter == "include" && (vmIDs == nil || len(*vmIDs) == 0) {
			return Target{}, errors.New("vm_filter=include requires at least one vm_id")
		}
		t.VMFilter = filter
	}
	if vmIDs != nil {
		t.VMIDs = *vmIDs
	}
	if enabled != nil {
		t.Enabled = *enabled
	}
	t.UpdatedAt = time.Now().UTC()
	if err := s.saveTargets(); err != nil {
		return Target{}, err
	}
	return *t, nil
}

func (s *Store) DeleteTarget(id string) error {
	if id == "default" {
		return errors.New("cannot delete the default target")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.targets[id]; !ok {
		return nil
	}
	delete(s.targets, id)
	delete(s.secrets, id)
	if err := s.saveTargets(); err != nil {
		return err
	}
	return s.saveSecrets()
}

// DeleteBackupFile removes a single archive from the target's path.
// The filename is matched against a strict pattern (webkvm-<host>-
// <UTC timestamp>.tar.gz) to prevent path traversal — without this
// guard a caller could pass "../../etc/passwd" and we'd happily
// delete whatever the backend user can reach.
//
// This is the ONLY way backups get cleaned up now: the runner
// never auto-deletes. Operators are expected to manage disk usage
// by hand from the Files tab on the Backup page.
func DeleteBackupFile(tgt Target, filename string) error {
	if !ValidBackupFilename(filename) {
		return fmt.Errorf("invalid filename %q: expected webkvm-<host>-<UTC>.tar.gz", filename)
	}
	if tgt.Type == TargetSFTP {
		return sftpDelete(tgt, filename)
	}
	path := filepath.Join(tgt.Path, filename)
	// Resolve symlinks and confirm we still live under tgt.Path.
	// filepath.EvalSymlinks returns an error if the file doesn't
	// exist, which is the right thing — we shouldn't be removing
	// things that aren't there.
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	realTgt, err := filepath.EvalSymlinks(tgt.Path)
	if err != nil {
		// Target path may not exist if no backup has ever run.
		// We treat that as "file not found" rather than a hard
		// error, matching ListBackupsOnTarget's behaviour.
		return os.ErrNotExist
	}
	if !strings.HasPrefix(real, realTgt+string(os.PathSeparator)) && real != realTgt {
		return fmt.Errorf("file %q escapes target path", filename)
	}
	// Check the resolved path is not a symlink (TOCTOU protection).
	if fi, err := os.Lstat(real); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove symlink: %s", real)
	}
	return os.Remove(real)
}

// DeleteBackupRun removes every archive in a single backup run from
// a target. The run is identified by its "<ts26>-<randHex>" suffix
// (shared by every file in the run). Returns the number of files
// removed. For SFTP targets the removal happens on the remote host.
func DeleteBackupRun(tgt Target, runSuffix string) (int, error) {
	if !isRunSuffix(runSuffix) {
		return 0, fmt.Errorf("invalid run suffix %q", runSuffix)
	}
	if tgt.Type == TargetSFTP {
		return sftpDeleteRun(tgt, runSuffix)
	}
	entries, err := os.ReadDir(tgt.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.Contains(e.Name(), runSuffix) {
			continue
		}
		if !ValidBackupFilename(e.Name()) {
			continue
		}
		if err := DeleteBackupFile(tgt, e.Name()); err == nil {
			removed++
		}
	}
	return removed, nil
}

// ValidBackupFilename enforces the shape used by Runner.writeBackup.
// Three formats are accepted so files written by previous versions
// of the binary remain deletable from the Files tab:
//
//   1. Legacy: webkvm-<host>-<UTC>.tar.gz where UTC is 16 chars
//      (YYYYMMDDTHHMMSSZ). Produced by writeBackup before the
//      A3 commit.
//   2. Phase I: webkvm-<host>-<UTC-nano>-<randHex>.tar.gz where
//      UTC-nano is 26 chars (YYYYMMDDTHHMMSS.NNNNNNNNNZ) and
//      randHex is 6 or 12 lowercase hex chars (the runner
//      calls randHex(n) and hex-encodes, so n=3 → 6 chars,
//      n=6 → 12 chars; both have shipped over time).
//   3. Current (Phase II): webkvm-<host>-<UTC-nano>-<randHex>-
//      <name>.tar.zst where <name> is the VM name (or "config"
//      for the app-state tar). The producer output is zstd-
//      compressed; the runner produces one per in-scope VM plus
//      one config tar per backup run.
//
// The validator is regex-based so the <name> in Phase II can
// contain dashes (libvirt allows "ubuntu-22-04"). All three
// patterns share the leading "webkvm-" prefix and a known
// extension; the middle is matched structurally.
func ValidBackupFilename(name string) bool {
	for _, re := range validFilenameRegexes {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// validFilenameRegexes is the ordered list of patterns the
// validator accepts. Compiled once at package init; the
// patterns are stable across releases.
//
// Phase II: webkvm-<host>-<ts26>-<randHex>-<name>.tar.zst
//   <name> matches [A-Za-z0-9._-]+ so a hand-crafted ".."
//   or path-traversal in the runner-supplied VM id is
//   rejected by the validator.
//
// randHex is 6 or 12 hex chars. The original 6-char
// design was the v2 plan; an early A3 commit accidentally
// bumped randHex from 3 bytes to 6 bytes (12 hex) without
// updating this regex, which made every Phase II archive
// written since fail validation. The v8 release fixes
// the regex to accept both, with the randHex(6) format
// (12 hex) being the common case in production.
var validFilenameRegexes = []*regexp.Regexp{
	// Stable "latest configuration" snapshot (config/webkvm-config-latest.tar.zst).
	// Kept in the list so Verify/Restore can target it by basename.
	regexp.MustCompile(`^webkvm-config-latest\.tar\.zst$`),
	// Phase II: webkvm-<host>-<ts26>-<randHex>-<name>.tar.zst
	// The <name> is constrained to a libvirt-safe character
	// set (which the runner also enforces via sanitizeVMName
	// before it lands on disk).
	regexp.MustCompile(`^webkvm-[A-Za-z0-9._-]+-\d{8}T\d{6}\.\d{9}Z-[0-9a-f]{6,12}-[A-Za-z0-9._-]+\.tar\.zst$`),
	// Phase I: webkvm-<host>-<ts26>-<randHex>.tar.gz
	regexp.MustCompile(`^webkvm-[A-Za-z0-9._-]+-\d{8}T\d{6}\.\d{9}Z-[0-9a-f]{6,12}\.tar\.gz$`),
	// Legacy: webkvm-<host>-<ts16>.tar.gz
	regexp.MustCompile(`^webkvm-[A-Za-z0-9._-]+-\d{8}T\d{6}Z\.tar\.gz$`),
}

// isSafeName is kept around because it's a useful guard
// elsewhere in the package (sanitizeVMName relies on the
// same character set). The filename validator uses the
// regex above; this helper is the "should I let this
// string become a filename component" check for callers
// that don't want to round-trip the full regex.
func isSafeName(s string) bool {
	if s == "" || strings.Contains(s, "..") {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func isRandomSuffix(s string) bool {
	if len(s) != 6 && len(s) != 12 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func isNanoTimestamp(s string) bool {
	// YYYYMMDDTHHMMSS.NNNNNNNNNZ is 26 chars: 8 + 1 + 6 + 1 + 9 + 1.
	if len(s) != 26 || !strings.Contains(s, "T") || !strings.Contains(s, ".") {
		return false
	}
	for i, c := range s {
		switch i {
		case 8:
			if c != 'T' {
				return false
			}
		case 15:
			if c != '.' {
				return false
			}
		case 25:
			if c != 'Z' {
				return false
			}
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func isLegacyTimestamp(s string) bool {
	// YYYYMMDDTHHMMSSZ is 16 chars: 8 + 1 + 6 + 1.
	if len(s) != 16 || !strings.Contains(s, "T") {
		return false
	}
	for i, c := range s {
		switch i {
		case 8:
			if c != 'T' {
				return false
			}
		case 15:
			if c != 'Z' {
				return false
			}
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// --- Schedules ---

func (s *Store) ListSchedules() []Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Schedule, 0, len(s.schedules))
	for _, sc := range s.schedules {
		out = append(out, *sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Store) GetSchedule(id string) (Schedule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, ok := s.schedules[id]
	if !ok {
		return Schedule{}, false
	}
	return *sc, true
}

func (s *Store) CreateSchedule(name, cronExpr, targetID string) (Schedule, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Schedule{}, errors.New("name is required")
	}
	if cronExpr == "" {
		return Schedule{}, errors.New("cron is required")
	}
	if err := validateCron(cronExpr); err != nil {
		return Schedule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.targets[targetID]; !ok {
		return Schedule{}, ErrTargetNotFound
	}
	for _, sc := range s.schedules {
		if sc.Name == name {
			return Schedule{}, fmt.Errorf("schedule %q already exists", name)
		}
	}
	id := newID("s")
	sc := &Schedule{
		ID:        id,
		Name:      name,
		Cron:      cronExpr,
		TargetID:  targetID,
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	s.schedules[id] = sc
	if err := s.saveSchedules(); err != nil {
		delete(s.schedules, id)
		return Schedule{}, err
	}
	return *sc, nil
}

// UpdateSchedule patches a schedule. Every field is a pointer
// so the caller distinguishes "don't change" (nil) from "set
// to the zero value" (*x). This is the A4 fix for bug #4 —
// the previous code mixed two conventions: empty string /
// nil pointer meant "don't change", and a future edit-schedule
// UI would have hit the same Enabled=false ambiguity the
// A4 commit fixes for UpdateTarget.
func (s *Store) UpdateSchedule(
	id string,
	name, cronExpr, targetID *string,
	enabled *bool,
) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.schedules[id]
	if !ok {
		return Schedule{}, ErrScheduleNotFound
	}
	if name != nil {
		sc.Name = strings.TrimSpace(*name)
	}
	if cronExpr != nil {
		if err := validateCron(*cronExpr); err != nil {
			return Schedule{}, err
		}
		sc.Cron = *cronExpr
	}
	if targetID != nil {
		if _, ok := s.targets[*targetID]; !ok {
			return Schedule{}, ErrTargetNotFound
		}
		sc.TargetID = *targetID
	}
	if enabled != nil {
		sc.Enabled = *enabled
	}
	sc.UpdatedAt = time.Now().UTC()
	if err := s.saveSchedules(); err != nil {
		return Schedule{}, err
	}
	return *sc, nil
}

// validateCron parses expr with cron.ParseStandard (the same
// parser the runner uses). Before this check, a garbage
// expression was accepted by CreateSchedule, persisted to disk,
// and then silently dropped by Runner.addSchedule — the user
// saw a "schedule added" toast and a row in the table, but the
// schedule never fired. This is the "fail in silence" pattern
// (bug #2 in the A1 batch). Validate at write time so the
// user gets a 400 with a clear message.
func validateCron(expr string) error {
	if _, err := cron.ParseStandard(expr); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCron, err)
	}
	return nil
}

func (s *Store) DeleteSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.schedules, id)
	return s.saveSchedules()
}

// SetScheduleLastRun updates the LastRunAt / LastStatus / NextRunAt
// after a fire. Caller is expected to have a parsed cron schedule
// for the NextRunAt. Returns an error if the schedule isn't in
// the registry or if persisting the update fails — the previous
// signature silently swallowed both, leaving the operator's UI
// showing the previous run's status after a disk-full incident.
func (s *Store) SetScheduleLastRun(id string, status, errMsg string, nextRun time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.schedules[id]
	if !ok {
		return ErrScheduleNotFound
	}
	sc.LastRunAt = time.Now().UTC()
	sc.LastStatus = status
	sc.LastError = errMsg
	sc.NextRunAt = nextRun.UTC()
	if err := s.saveSchedules(); err != nil {
		return err
	}
	return nil
}

// --- Jobs ---

func (s *Store) ListJobs(limit int) []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		all = append(all, *j)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].StartedAt.After(all[j].StartedAt) })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all
}

func (s *Store) RecordJob(j Job) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.ID == "" {
		j.ID = newID("j")
	}
	if j.StartedAt.IsZero() {
		j.StartedAt = time.Now().UTC()
	}
	s.jobs[j.ID] = &j
	if err := s.saveJobs(); err != nil {
		return j, err
	}
	// Return the canonical job (with the generated ID) so the
	// caller can later call UpdateJob on the same Job value.
	// Without this the caller's local copy still has ID == "" and
	// UpdateJob returns "job not found", leaving the job stuck
	// at status="running" on disk.
	return j, nil
}

func (s *Store) UpdateJob(j Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[j.ID]; !ok {
		return errors.New("job not found")
	}
	s.jobs[j.ID] = &j
	return s.saveJobs()
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
