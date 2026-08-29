package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"webkvm/internal/logging"
)

// SystemInfo returned by /api/system/status.
type SystemInfo struct {
	Backend     BackendInfo    `json:"backend"`
	Libvirt     LibvirtInfo    `json:"libvirt"`
	Host        HostInfo       `json:"host"`
	BuildTime   string         `json:"build_time"`
	UptimeSec   int64          `json:"uptime_sec"`
	StartTime   string         `json:"start_time"`
	Pools       []PoolDiskInfo `json:"pools"`
	Latest      string         `json:"latest_version"`
	UpdateAvail bool           `json:"update_available"`
}

type BackendInfo struct {
	Version  string `json:"version"`
	GOOS     string `json:"goos"`
	GOARCH   string `json:"goarch"`
	Goroutines int   `json:"goroutines"`
}

type LibvirtInfo struct {
	Connected bool   `json:"connected"`
	URI       string `json:"uri"`
	Hypervisor string `json:"hypervisor,omitempty"`
}

type HostInfo struct {
	Hostname  string `json:"hostname"`
	Kernel    string `json:"kernel"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

type PoolDiskInfo struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	TotalBytes uint64  `json:"total_bytes"`
	FreeBytes  uint64  `json:"free_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

func (h *Handler) SystemStatus(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	si := SystemInfo{
		Backend: BackendInfo{
			Version:    h.cfg.Version,
			GOOS:       runtime.GOOS,
			GOARCH:     runtime.GOARCH,
			Goroutines: runtime.NumGoroutine(),
		},
		BuildTime: h.cfg.BuildTime,
		Libvirt: LibvirtInfo{
			Connected: h.lv.IsConnected(),
			URI:       h.cfg.LibvirtURI,
		},
		Host: HostInfo{
			Hostname: hostname,
			Kernel:   readKernel(),
			OS:       readOSRelease(),
			Arch:     runtime.GOARCH,
		},
		UptimeSec: int64(time.Since(h.StartedAt).Seconds()),
		StartTime: h.StartedAt.Format(time.RFC3339),
	}

	// Pool disk usage.
	pools, _ := h.lv.ListStoragePools()
	for _, p := range pools {
		info := PoolDiskInfo{Name: p.Name, Path: p.Path}
		var stat syscall.Statfs_t
		if err := syscall.Statfs(p.Path, &stat); err == nil {
			info.TotalBytes = stat.Blocks * uint64(stat.Bsize)
			info.FreeBytes = stat.Bavail * uint64(stat.Bsize)
			info.UsedBytes = info.TotalBytes - info.FreeBytes
			if info.TotalBytes > 0 {
				info.UsedPct = float64(info.UsedBytes) * 100 / float64(info.TotalBytes)
			}
		}
		si.Pools = append(si.Pools, info)
	}

	// Update check (best-effort, short timeout).
	latest, ok := checkLatestVersion(r.Context(), h.cfg.Version)
	if ok {
		si.Latest = latest
		si.UpdateAvail = isNewer(latest, h.cfg.Version)
	}

	jsonResp(w, http.StatusOK, si)
}

// SystemCert serves the backend's TLS certificate so the user can download
// it and add it to their trust store (removing the self-signed warning in
// the browser). The installer writes the certificate to DATA_DIR/certs/webkvm.crt.
func (h *Handler) SystemCert(w http.ResponseWriter, r *http.Request) {
	certPath := filepath.Join(h.cfg.DataDir, "certs", "webkvm.crt")
	data, err := os.ReadFile(certPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "no TLS certificate configured (server.tls_cert / server.tls_key)", http.StatusNotFound)
			return
		}
		http.Error(w, "cannot read certificate: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="webkvm.crt"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *Handler) SystemLogs(w http.ResponseWriter, r *http.Request) {
	lines := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
			lines = n
		}
	}

	// Try log sources in order of preference. Each candidate returns
	// (output, source, error) where source describes what was read
	// for the audit log; a non-nil error means "this source is not
	// available, try the next one". A successful read returns a
	// nil error and we stop.
	candidates := []func() ([]byte, string, error){
		// 1. WEBKVM_LOG_FILE: configured via the systemd unit or a
		//    drop-in. This is the canonical path in
		//    production deployments because the same code path
		//    works in both environments and the file is
		//    automatically included in /opt/webkvm backups.
		func() ([]byte, string, error) {
			if h.cfg.LogFile == "" {
				return nil, "", errLogSourceUnavailable
			}
			out, err := tailFile(h.cfg.LogFile, lines)
			if err != nil {
				return nil, "", err
			}
			return out, "file:" + h.cfg.LogFile, nil
		},
		// 2. Legacy systemd log file path (kept for older installs
		//    that have a drop-in writing to /var/log/webkvm/).
		func() ([]byte, string, error) {
			const legacy = "/var/log/webkvm/backend.log"
			if _, statErr := os.Stat(legacy); statErr != nil {
				return nil, "", errLogSourceUnavailable
			}
			out, err := tailFile(legacy, lines)
			if err != nil {
				return nil, "", err
			}
			return out, "file:" + legacy, nil
		},
		// 3. journalctl: only works when systemd is on the host
		//    and the webkvm unit is registered. Routed through a
		//    variable so tests can stub it to simulate a
		//    non-systemd environment.
		func() ([]byte, string, error) {
			if journalctlRunner == nil {
				return nil, "", errLogSourceUnavailable
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			out, err := journalctlRunner(ctx, lines)
			if err != nil {
				return nil, "", err
			}
			return out, "journalctl", nil
		},
	}

	var out []byte
	var source string
	for _, c := range candidates {
		var err error
		out, source, err = c()
		if err == nil {
			break
		}
	}
	if out == nil {
		jsonErr(w, http.StatusServiceUnavailable,
			"no log source available (set WEBKVM_LOG_FILE to a writable path, e.g. /opt/webkvm/logs/backend.log)")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Log-Source", source)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// errLogSourceUnavailable is the sentinel returned by a log-source
// candidate when it knows it can't service the request (e.g. the
// optional file wasn't configured, or a binary isn't on PATH).
// Other errors (real I/O failures) are returned as-is so the caller
// can distinguish "try the next source" from "give up".
var errLogSourceUnavailable = errors.New("log source unavailable")

// journalctlRunner is the function the SystemLogs handler calls to
// query journald. It is a package-level variable so tests can stub
// it. The default implementation shells out to /usr/bin/journalctl;
// if the binary is absent, exec returns an error and the handler
// moves on to the next source (or returns 503 if there is none).
var journalctlRunner = func(ctx context.Context, lines int) ([]byte, error) {
	return exec.CommandContext(ctx, "journalctl", "-u", "webkvm", "-n", strconv.Itoa(lines), "--no-pager").CombinedOutput()
}

// backupScriptPath is the path the SystemBackup handler invokes.
// Package-level var so tests can stub it. Default is
// /usr/local/bin/webkvm-backup.sh, installed by the systemd unit
// (scripts/webkvm.service).
var backupScriptPath = "/usr/local/bin/webkvm-backup.sh"

// backupRunner wraps exec.CommandContext so tests can replace it
// without actually shelling out.
var backupRunner = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, backupScriptPath).CombinedOutput()
}

// SystemBackup snapshots /opt/webkvm to the configured SMB share
// by invoking /usr/local/bin/webkvm-backup.sh. The script writes a
// JSON line to stdout describing the result; we parse it and
// return it to the caller. Long-running: a 50GB /opt/webkvm can
// take 5-10 minutes, so we use a 30-minute timeout.
//
// Requires root (the script writes to /mnt/webkvm-backup, which
// is mounted as root). Admin-only via the route middleware.
func (h *Handler) SystemBackup(w http.ResponseWriter, r *http.Request) {
	if !isRoot() {
		jsonErr(w, http.StatusForbidden, "backup requires the backend to run as root")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	out, err := backupRunner(ctx)
	if err != nil {
		// The script always prints something on stderr; include
		// the last 1KB so the operator can see why without
		// dumping the full command output.
		errMsg := strings.TrimSpace(string(out))
		if len(errMsg) > 1024 {
			errMsg = errMsg[len(errMsg)-1024:]
		}
		h.audit.Log(auditFor(r, "system.backup.failed", "webkvm-backup", map[string]interface{}{"error": errMsg}))
		jsonErr(w, http.StatusInternalServerError, "backup script failed: "+errMsg)
		return
	}

	// Script's stdout is a single JSON line. Parse it.
	var result struct {
		Filename   string `json:"filename"`
		Path       string `json:"path"`
		Size       int64  `json:"size"`
		SHA256     string `json:"sha256"`
		Timestamp  string `json:"timestamp"`
		DurationMS int64  `json:"duration_ms"`
		Host       string `json:"host"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		jsonErr(w, http.StatusInternalServerError, "backup script returned non-JSON: "+string(out))
		return
	}
	h.audit.Log(auditFor(r, "system.backup", "webkvm-backup", map[string]interface{}{
		"filename":  result.Filename,
		"size":      result.Size,
		"sha256":    result.SHA256,
		"duration":  result.DurationMS,
		"host":      result.Host,
	}))
	jsonResp(w, http.StatusOK, result)
}

// SystemListBackups returns the metadata of recent backups in the
// SMB share for this host. It only lists, never deletes, so it's
// safe to call from the UI on every page load. If the share
// isn't mounted, returns an empty list and a `mounted: false`
// flag instead of an error (the UI can then hide the restore UI).
func (h *Handler) SystemListBackups(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	if i := strings.IndexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	dir := "/mnt/webkvm-backup/webkvm-" + host
	out := struct {
		Mounted  bool       `json:"mounted"`
		Host     string     `json:"host"`
		Dir      string     `json:"dir"`
		Backups  []BackupInfo `json:"backups"`
	}{Host: host, Dir: dir}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// Most common case: share not mounted. Return empty.
		out.Mounted = false
		jsonResp(w, http.StatusOK, out)
		return
	}
	out.Mounted = true
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out.Backups = append(out.Backups, BackupInfo{
			Filename: e.Name(),
			Size:     info.Size(),
			Modified: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	// Sort newest first.
	sort.Slice(out.Backups, func(i, j int) bool {
		return out.Backups[i].Modified > out.Backups[j].Modified
	})
	jsonResp(w, http.StatusOK, out)
}

// BackupInfo is the JSON shape returned by SystemListBackups.
// Kept as a top-level type so the frontend can import it via
// the generated OpenAPI/types later.
type BackupInfo struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

func (h *Handler) SystemRestart(w http.ResponseWriter, r *http.Request) {
	if !isRoot() {
		jsonErr(w, http.StatusForbidden, "restart requires the backend to run as root")
		return
	}
	h.audit.Log(auditFor(r, "system.restart", "webkvm", nil))
	// Run restart in background so the HTTP response can return before
	// the process is killed.
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("systemctl", "restart", "webkvm").Run()
	}()
	jsonResp(w, http.StatusAccepted, map[string]string{"status": "restarting"})
}

// ApplyRestartSettings is called by the Settings page after the
// user has saved a batch of restart-required changes and clicked
// "Apply & restart". Unlike SystemRestart, this endpoint takes the
// keys being applied as audit detail so an operator can later
// answer "who restarted the server last Friday and why?".
func (h *Handler) ApplyRestartSettings(w http.ResponseWriter, r *http.Request) {
	if !isRoot() {
		jsonErr(w, http.StatusForbidden, "restart requires the backend to run as root")
		return
	}
	var req struct {
		Keys []string `json:"keys"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // 64 KB
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(req.Keys) > 100 {
		jsonErr(w, http.StatusBadRequest, "too many keys (max 100)")
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "settings.apply_restart", "webkvm", map[string]interface{}{"keys": req.Keys}))
	}
	if h.settings != nil {
		h.settings.ClearPending()
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		// systemd unit has Restart=always so it comes back up.
		_ = exec.Command("systemctl", "restart", "webkvm").Run()
	}()
	jsonResp(w, http.StatusAccepted, map[string]string{"status": "restarting"})
}

// ApplyLiveSettings is called by the Settings page after the user
// saves a batch of hot-reloadable values (logging.level,
// backup.retention_*, auth.token_ttl, etc.). The endpoint
// immediately applies them in-process — no restart. The audit log
// records who triggered the apply.
func (h *Handler) ApplyLiveSettings(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		jsonErr(w, http.StatusServiceUnavailable, "settings store not initialized")
		return
	}
	var req struct {
		Keys []string `json:"keys"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // 64 KB
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(req.Keys) > 100 {
		jsonErr(w, http.StatusBadRequest, "too many keys (max 100)")
		return
	}
	applied := []string{}
	for _, k := range req.Keys {
		switch k {
		case "logging.level":
			logging.SetLevel(h.settings.GetString("logging.level"))
			applied = append(applied, k)
		case "backup.retention_count", "backup.retention_days", "backup.verify_on_write":
			// These are picked up on the next RunOnce; nothing to
			// do here. We still report them as applied so the UI
			// stops showing the "live values" badge.
			applied = append(applied, k)
		case "auth.token_ttl", "auth.allow_api_tokens", "server.trust_proxy", "server.trusted_cidrs":
			// TokenTTL/AllowAPITokens are read per-request by the
			// auth.Manager; trust_proxy/trusted_cidrs are read
			// per-request by the rate limiter and the client-IP
			// resolver. Nothing to do here either.
			applied = append(applied, k)
		}
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "settings.apply_live", "webkvm", map[string]interface{}{"keys": applied}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"applied": applied})
}

func (h *Handler) SystemUpdate(w http.ResponseWriter, r *http.Request) {
	if !isRoot() {
		jsonErr(w, http.StatusForbidden, "update requires the backend to run as root")
		return
	}
	if h.cfg.RepoDir == "" {
		jsonErr(w, http.StatusServiceUnavailable, "REPO_DIR not set; cannot auto-update")
		return
	}
	if _, err := os.Stat(filepath.Join(h.cfg.RepoDir, ".git")); err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "repo not found at "+h.cfg.RepoDir)
		return
	}
	h.audit.Log(auditFor(r, "system.update", "webkvm", map[string]interface{}{"repo": h.cfg.RepoDir}))
	// Run update in background, log progress to /var/log/webkvm/update.log
	go func() {
		log, _ := os.OpenFile("/var/log/webkvm/update.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if log != nil {
			defer log.Close()
		}
		runStep := func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Dir = h.cfg.RepoDir
			out, err := cmd.CombinedOutput()
			if log != nil {
				log.WriteString("$ " + name + " " + strings.Join(args, " ") + "\n")
				log.Write(out)
				if err != nil {
					log.WriteString("\nERROR: " + err.Error() + "\n")
				}
			}
			return err
		}
		if err := runStep("git", "pull", "--ff-only"); err != nil {
			return
		}
		if err := runStep("make", "build"); err != nil {
			return
		}
		if err := runStep("make", "install-systemd"); err != nil {
			return
		}
		_ = exec.Command("systemctl", "restart", "webkvm").Run()
	}()
	jsonResp(w, http.StatusAccepted, map[string]string{"status": "updating", "log": "/var/log/webkvm/update.log"})
}

// --- helpers ---

// isRoot reports whether the current process is uid 0. Package
// var so tests can stub it.
var isRoot = func() bool {
	return os.Geteuid() == 0
}

func readKernel() string {
	out, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func readOSRelease() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return runtime.GOOS
}

func tailFile(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Slurp the last ~64KB which is plenty for `n` log lines and avoids
	// streaming complexity for what's normally a small log file.
	const maxRead = 64 * 1024
	fi, _ := f.Stat()
	offset := int64(0)
	if fi != nil && fi.Size() > maxRead {
		offset = fi.Size() - maxRead
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return nil, err
	}
	all, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(all), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// checkLatestVersion asks the GitHub API for the latest release. It is
// best-effort and never blocks the response for more than ~2s.
func checkLatestVersion(ctx context.Context, _ string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/repos/webkvm/webkvm/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	cl := &http.Client{Timeout: 3 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", false
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false
	}
	return strings.TrimPrefix(body.TagName, "v"), true
}

// isNewer returns true when `latest` is a higher semver than `current`.
// Treats non-numeric suffixes leniently.
func isNewer(latest, current string) bool {
	if current == "dev" || current == "" {
		return latest != ""
	}
	l := parseSemver(latest)
	c := parseSemver(current)
	for i := 0; i < 3; i++ {
		if l[i] > c[i] {
			return true
		}
		if l[i] < c[i] {
			return false
		}
	}
	return false
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 4)
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n := 0
		for _, c := range parts[i] {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out
}

// (no libvirt import needed in this file; uses h.lv methods)
