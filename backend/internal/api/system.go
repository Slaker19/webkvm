package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"webkvm/internal/backupstore"
	"webkvm/internal/compute"
	"webkvm/internal/logging"
	"webkvm/internal/safego"
)

// SystemInfo returned by /api/system/status.
type SystemInfo struct {
	Backend     BackendInfo    `json:"backend"`
	Libvirt     LibvirtInfo    `json:"libvirt"`
	Host        HostInfo       `json:"host"`
	BuildTime   string         `json:"build_time"`
	UptimeSec   int64          `json:"uptime_sec"` // backend process uptime (unchanged meaning)
	StartTime   string         `json:"start_time"`
	Pools       []PoolDiskInfo `json:"pools"`
	Latest      string         `json:"latest_version"`
	UpdateAvail bool           `json:"update_available"`
	// UpdateMode is which path POST /api/system/update would take:
	// "release" (verified GitHub asset) or "source" (rebuild the checkout).
	UpdateMode string `json:"update_mode"`

	Disk          DiskInfo      `json:"disk"`            // aggregate host disk (DATA_DIR statfs)
	Load          LoadAvg       `json:"load"`            // /proc/loadavg
	HostUptimeSec int64         `json:"host_uptime_sec"` // /proc/uptime — real host OS uptime, distinct from UptimeSec above
	Services      []ServiceInfo `json:"services"`
	Platform      PlatformInfo  `json:"platform"`
}

// DiskInfo is the aggregate host-disk stat (statfs on DATA_DIR — the
// same call GetHostStats already makes for a different response shape).
type DiskInfo struct {
	TotalBytes uint64  `json:"total_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	FreeBytes  uint64  `json:"free_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

// LoadAvg is /proc/loadavg's first three fields.
type LoadAvg struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

// ServiceInfo is one row in the "System Services" list.
type ServiceInfo struct {
	// Unit is the resolved systemd unit name actually present on this
	// host (e.g. "libvirtd.service" OR "virtqemud.service").
	Unit string `json:"unit"`
	// Key is a stable machine key for i18n lookup on the frontend,
	// independent of which real unit backs it (e.g. "libvirt", "incus",
	// "dnsmasq:<bridge>").
	Key         string `json:"key"`
	Description string `json:"description"` // systemd's own unit description
	Active      bool   `json:"active"`      // ActiveState == "active"
	State       string `json:"state"`       // raw ActiveState: active/inactive/failed/unknown
	Found       bool   `json:"found"`       // false if the unit doesn't exist on this host at all
}

// PlatformInfo is the hypervisor-platform card.
type PlatformInfo struct {
	Kernel         string         `json:"kernel"`
	QEMUVersion    string         `json:"qemu_version,omitempty"`
	LibvirtVersion string         `json:"libvirt_version,omitempty"`
	IncusVersion   string         `json:"incus_version,omitempty"` // omitted entirely when !IncusEnabled
	IncusEnabled   bool           `json:"incus_enabled"`
	NestedVirt     NestedVirtInfo `json:"nested_virt"`
	IOMMU          IOMMUInfo      `json:"iommu"`
}

type NestedVirtInfo struct {
	Supported bool   `json:"supported"`
	Vendor    string `json:"vendor"` // "intel" | "amd" | "unknown"
	Detail    string `json:"detail,omitempty"`
}

type IOMMUInfo struct {
	Enabled    bool `json:"enabled"`
	Groups     int  `json:"groups"`
	Assignable int  `json:"assignable,omitempty"`
}

type BackendInfo struct {
	Version    string `json:"version"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	Goroutines int    `json:"goroutines"`
}

type LibvirtInfo struct {
	Connected  bool   `json:"connected"`
	URI        string `json:"uri"`
	Hypervisor string `json:"hypervisor,omitempty"`
}

type HostInfo struct {
	Hostname string `json:"hostname"`
	Kernel   string `json:"kernel"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
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
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
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
	pools, _ := h.compute.ListStoragePools()
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
	// In a checkout the button rebuilds from source, where "newer" is a
	// commit, not a published release: gating it on a GitHub tag would grey
	// it out exactly where rebuilding is the point.
	if h.cfg.RepoDir != "" && pathExists(filepath.Join(h.cfg.RepoDir, ".git")) {
		si.UpdateMode = "source"
		si.UpdateAvail = true
	} else {
		si.UpdateMode = "release"
	}

	// Aggregate host disk (same statfs GetHostStats already does on DataDir).
	var dstat syscall.Statfs_t
	if err := syscall.Statfs(h.cfg.DataDir, &dstat); err == nil {
		total := dstat.Blocks * uint64(dstat.Bsize)
		free := dstat.Bavail * uint64(dstat.Bsize)
		used := total - free
		pct := 0.0
		if total > 0 {
			pct = float64(used) * 100 / float64(total)
		}
		si.Disk = DiskInfo{TotalBytes: total, UsedBytes: used, FreeBytes: free, UsedPct: pct}
	}

	si.Load = readLoadAvg()
	si.HostUptimeSec = readHostUptimeSec()
	si.Services = h.collectServiceStatuses(r.Context())
	si.Platform = h.collectPlatformInfo()

	jsonResp(w, http.StatusOK, si)
}

// readLoadAvg parses /proc/loadavg's first three fields.
func readLoadAvg() LoadAvg {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return LoadAvg{}
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return LoadAvg{}
	}
	l1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return LoadAvg{}
	}
	l5, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return LoadAvg{}
	}
	l15, err := strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return LoadAvg{}
	}
	return LoadAvg{Load1: l1, Load5: l5, Load15: l15}
}

// readHostUptimeSec parses /proc/uptime (first field, seconds, float) —
// the real host OS uptime, not this backend process's own uptime.
func readHostUptimeSec() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(secs)
}

// readNestedVirt inspects the KVM module's "nested" parameter, trying
// both Intel and AMD module names (whichever is loaded for this host's
// CPU vendor). Returns Supported=false with a Detail string when
// undetectable (e.g. module not loaded) rather than erroring the whole
// status endpoint.
func readNestedVirt() NestedVirtInfo {
	vendor := "unknown"
	if cpuinfo, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		s := string(cpuinfo)
		if strings.Contains(s, "GenuineIntel") {
			vendor = "intel"
		} else if strings.Contains(s, "AuthenticAMD") {
			vendor = "amd"
		}
	}
	modParam := map[string]string{
		"intel": "/sys/module/kvm_intel/parameters/nested",
		"amd":   "/sys/module/kvm_amd/parameters/nested",
	}[vendor]
	if modParam == "" {
		return NestedVirtInfo{Vendor: vendor, Detail: "unknown CPU vendor; cannot locate the KVM module parameter"}
	}
	data, err := os.ReadFile(modParam)
	if err != nil {
		return NestedVirtInfo{Vendor: vendor, Detail: "kvm_" + vendor + " module not loaded (or nested virtualization unsupported)"}
	}
	val := strings.TrimSpace(string(data))
	supported := val == "1" || strings.EqualFold(val, "Y")
	return NestedVirtInfo{Supported: supported, Vendor: vendor}
}

// readIOMMU counts entries under /sys/kernel/iommu_groups. An empty or
// unreadable directory means IOMMU/VFIO is off (either unsupported by
// the hardware, or supported but not activated via
// intel_iommu=on/amd_iommu=on on the kernel command line — this can't
// distinguish those two cases without parsing dmesg, which isn't worth
// the fragility for a status page).
func readIOMMU() IOMMUInfo {
	entries, err := os.ReadDir("/sys/kernel/iommu_groups")
	if err != nil {
		return IOMMUInfo{}
	}
	return IOMMUInfo{Enabled: len(entries) > 0, Groups: len(entries)}
}

// collectPlatformInfo reads QEMU/libvirt versions off the same
// *libvirt.Connect host.go's GetHostInfo already uses, but NOT the same
// field mapping: for the QEMU driver, conn.GetVersion() returns the
// version of the running HYPERVISOR (QEMU), while conn.GetLibVersion()
// returns the libvirt library's own version — confirmed live against
// `virsh version` ("Using library: 12.7.0" / "Running hypervisor: QEMU
// 11.1.1"), where host.go's older code mislabels the former as
// LibvirtVersion. The capabilities-XML <qemu><version> extraction is
// kept only as a fallback for when GetVersion() itself fails.
func (h *Handler) collectPlatformInfo() PlatformInfo {
	p := PlatformInfo{Kernel: readKernel(), IncusEnabled: h.cfg.IncusEnabled}
	if conn := h.lv.Get(); conn != nil {
		if libVer, err := conn.GetLibVersion(); err == nil {
			p.LibvirtVersion = fmt.Sprintf("%d.%d.%d", libVer/1000000, (libVer/1000)%1000, libVer%1000)
		}
		if hvVer, err := conn.GetVersion(); err == nil {
			p.QEMUVersion = fmt.Sprintf("%d.%d.%d", hvVer/1000000, (hvVer/1000)%1000, hvVer%1000)
		} else if out, err := conn.GetCapabilities(); err == nil {
			if v := extractQEMUVersion(out); v != "" {
				p.QEMUVersion = v
			}
		}
	}
	if p.IncusEnabled {
		if combined, ok := h.compute.(*compute.Combined); ok {
			if sec := combined.Secondary(); sec != nil {
				if vr, ok := sec.(interface{ ServerInfo() (string, error) }); ok {
					if v, err := vr.ServerInfo(); err == nil {
						p.IncusVersion = v
					}
					// err != nil (daemon down): leave IncusVersion empty; the
					// services list independently reports the incus.service
					// state, so a stopped daemon is still visible.
				}
			}
		}
	}
	p.NestedVirt = readNestedVirt()
	p.IOMMU = readIOMMU()
	if p.IOMMU.Enabled && h.compute != nil {
		if pf, err := h.compute.GetHostPCIPreflight(); err == nil {
			p.IOMMU.Assignable = pf.GroupsAssignable
		}
	}
	return p
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
// without actually shelling out. mount overrides the script's
// BACKUP_MOUNT env var (see backupMountCandidate); an empty mount
// leaves the script's own default ("/mnt/webkvm-backup") in place,
// so its own `mountpoint -q` preflight check remains the single
// source of truth for "is this share actually usable".
var backupRunner = func(ctx context.Context, mount string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, backupScriptPath)
	if mount != "" {
		cmd.Env = append(os.Environ(), "BACKUP_MOUNT="+mount)
	}
	return cmd.CombinedOutput()
}

// isMountpoint reports whether path is currently a real mountpoint,
// via the same `mountpoint` binary scripts/webkvm-backup.sh already
// requires — never guesses from path conventions alone.
func isMountpoint(path string) bool {
	if path == "" {
		return false
	}
	return exec.Command("mountpoint", "-q", path).Run() == nil
}

// backupMountCandidate picks where the host-config quick-backup
// (scripts/webkvm-backup.sh) should write, so the feature works
// without requiring its own dedicated /etc/fstab entry when the
// operator has already set up an NFS/SMB storage pool or backup
// target. Preference order: the feature's original manually-mounted
// convention (so an existing fstab-based setup keeps working exactly
// as before); the first backup target (NFS/SMB) whose path is
// currently mounted, since backup targets are the natural home for
// this kind of file; then the first storage pool whose path is
// currently mounted. Returns "" if none qualify.
func (h *Handler) backupMountCandidate() string {
	const legacy = "/mnt/webkvm-backup"
	if isMountpoint(legacy) {
		return legacy
	}
	if h.backupStore != nil {
		for _, t := range h.backupStore.ListTargets() {
			if (t.Type == backupstore.TargetSMB || t.Type == backupstore.TargetNFS) && isMountpoint(t.Path) {
				return t.Path
			}
		}
	}
	if h.lv != nil {
		if pools, err := h.lv.ListStoragePools(); err == nil {
			for _, p := range pools {
				if isMountpoint(p.Path) {
					return p.Path
				}
			}
		}
	}
	return ""
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

	out, err := backupRunner(ctx, h.backupMountCandidate())
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
		"filename": result.Filename,
		"size":     result.Size,
		"sha256":   result.SHA256,
		"duration": result.DurationMS,
		"host":     result.Host,
	}))
	jsonResp(w, http.StatusOK, result)
}

// SystemListBackups returns the metadata of recent backups in the
// SMB/NFS share for this host (see backupMountCandidate for how that
// share is chosen). It only lists, never deletes, so it's safe to
// call from the UI on every page load. If no share is mounted,
// returns an empty list and a `mounted: false` flag instead of an
// error (the UI can then hide the restore UI).
func (h *Handler) SystemListBackups(w http.ResponseWriter, r *http.Request) {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	if i := strings.IndexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	out := struct {
		Mounted bool         `json:"mounted"`
		Host    string       `json:"host"`
		Dir     string       `json:"dir"`
		Backups []BackupInfo `json:"backups"`
	}{Host: host}

	mount := h.backupMountCandidate()
	if mount == "" {
		jsonResp(w, http.StatusOK, out)
		return
	}
	// The share itself being mounted is "mounted: true" even before
	// this host has ever written into its per-host subdirectory —
	// otherwise the UI would keep reporting "not mounted" forever on
	// a freshly-configured destination that simply hasn't had its
	// first backup run yet.
	out.Mounted = true
	dir := mount + "/webkvm-" + host
	out.Dir = dir

	entries, err := os.ReadDir(dir)
	if err != nil {
		// Real mount, but this host's subdirectory doesn't exist yet.
		jsonResp(w, http.StatusOK, out)
		return
	}
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
		defer safego.Recover("system_restart")
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
		defer safego.Recover("system_restart")
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

// updateLogPath is where the updater writes its progress; the same path is
// returned to the UI so the operator can tail it.
const updateLogPath = "/var/log/webkvm/update.log"

// SystemUpdate starts a self-update in the background.
//
// Two update paths, one script (packaging/standalone/update.sh, installed as
// webkvm-update) — which one runs is detected, never asked of the operator:
//
//   - release (default): download the published GitHub release binary, verify
//     it against SHA256SUMS, install it, health-check and roll back on
//     failure. Needs no checkout and no Go/Node toolchain, which is what a
//     standalone install (install.sh) promises.
//   - source (--source): git pull + rebuild inside REPO_DIR. Only selected
//     when REPO_DIR really is a checkout, so a bare DATA_DIR/source directory
//     (the install.sh default) can never select it.
func (h *Handler) SystemUpdate(w http.ResponseWriter, r *http.Request) {
	if !isRoot() {
		jsonErr(w, http.StatusForbidden, "update requires the backend to run as root")
		return
	}
	// Opt-in gate: running an installer as root is an intentional root-RCE
	// path for a compromised admin session; require the operator to
	// consciously enable it via env (install.sh and scripts/webkvm.service
	// now set it, so the button works out of the box).
	if os.Getenv("WEBKVM_ALLOW_UPDATE") != "1" {
		jsonErr(w, http.StatusForbidden, "system update is disabled; set WEBKVM_ALLOW_UPDATE=1 in the service environment to enable it")
		return
	}
	updater, sourceMode, err := findUpdater(h.cfg.RepoDir, updaterPaths(h.cfg.RepoDir))
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	// Checked before the 202, not inside the goroutine: without it the
	// updater cannot be launched at all (see runUpdater), and answering
	// "updating" would send the operator off to tail a log that no one is
	// ever going to write.
	if err := updaterLaunchable(); err != nil {
		jsonErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	mode := "release"
	var args []string
	if sourceMode {
		mode = "source"
		args = append(args, "--source")
	}
	h.audit.Log(auditFor(r, "system.update", "webkvm", map[string]interface{}{
		"repo": h.cfg.RepoDir, "updater": updater, "mode": mode,
	}))
	// Detached: the updater stops this very service before replacing the
	// binary, so nothing here may wait on it.
	go func() {
		defer safego.Recover("system_update")
		launchUpdater(updater, args, h.cfg.RepoDir)
	}()
	jsonResp(w, http.StatusAccepted, map[string]string{
		"status":  "updating",
		"mode":    mode,
		"updater": updater,
		"log":     updateLogPath,
	})
}

// updaterPaths is defaultUpdaterPaths, kept as a variable so tests can point
// the search at a scratch tree instead of the machine's real /usr/local/bin.
var updaterPaths = defaultUpdaterPaths

// defaultUpdaterPaths lists where the updater script may live, in priority
// order. webkvm-update is what install.sh deploys; the copy inside a checkout
// is the fallback for a dev tree that has not been reinstalled.
func defaultUpdaterPaths(repoDir string) []string {
	paths := []string{}
	if p, err := exec.LookPath("webkvm-update"); err == nil {
		paths = append(paths, p)
	}
	paths = append(paths, "/usr/local/bin/webkvm-update", "/usr/bin/webkvm-update")
	if repoDir != "" {
		paths = append(paths, filepath.Join(repoDir, "packaging", "standalone", "update.sh"))
	}
	return paths
}

// findUpdater returns the first existing updater script from candidates and
// whether the source mode applies (REPO_DIR is a git checkout).
func findUpdater(repoDir string, candidates []string) (path string, sourceMode bool, err error) {
	sourceMode = repoDir != "" && pathExists(filepath.Join(repoDir, ".git"))
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, serr := os.Stat(c); serr == nil && !st.IsDir() {
			return c, sourceMode, nil
		}
	}
	return "", false, errors.New("updater not found (install.sh deploys it as webkvm-update); reinstall with install.sh to restore it")
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// updaterLaunchable reports whether the updater can actually be started. A
// package var so tests do not depend on the host having systemd.
var updaterLaunchable = func() error {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return errors.New("systemd-run not found; the updater needs it to survive the service restart it performs")
	}
	return nil
}

// launchUpdater starts the update in the background. A package var so tests
// can capture the chosen path and mode instead of really invoking systemd.
var launchUpdater = runUpdater

// runUpdater starts the updater detached from this service's cgroup.
//
// The unit runs with KillMode=control-group, and the updater itself calls
// `systemctl stop webkvm` before replacing the binary: a plain child process
// would be SIGTERMed by that stop, leaving the old binary in place and the
// update silently half-done. A transient systemd unit puts the updater in its
// own cgroup so the stop/restart cycle cannot reach it, and it still gets to
// run the health check and roll back. The leading sleep gives the 202 response
// time to reach the browser before we take the service away from it.
//
// There is deliberately no in-process fallback. A plain child would be killed
// by its own `systemctl stop` between the backup and the install, leaving the
// service stopped, the binary unreplaced and nobody left to restart or roll
// back — strictly worse than not starting at all. Without systemd-run the
// launch is refused and journalled.
func runUpdater(updater string, args []string, repoDir string) {
	// The redirect below would fail silently (no file, no trace at all)
	// if the log directory were missing, e.g. a hand-rolled unit.
	if err := os.MkdirAll(filepath.Dir(updateLogPath), 0o750); err != nil {
		exec.Command("logger", "-t", "webkvm-update", "cannot create "+filepath.Dir(updateLogPath)+": "+err.Error()).Run()
	}
	cmdline := strings.Join(append([]string{shellQuote(updater)}, args...), " ")
	if repoDir != "" {
		cmdline = "export WEBKVM_REPO_DIR=" + shellQuote(repoDir) + "; " + cmdline
	}
	script := "sleep 2; " + cmdline + " >>" + shellQuote(updateLogPath) + " 2>&1"

	// Re-checked here, not just in the handler: this runs detached, and by
	// now the 202 is long gone, so a refusal has to leave a trace.
	if err := updaterLaunchable(); err != nil {
		_ = exec.Command("logger", "-t", "webkvm-update", "refusing to update: "+err.Error()).Run()
		return
	}
	_ = startTransientUnit(script) // already journalled on failure
}

// startTransientUnit launches the updater as its own systemd unit and
// returns nil once it is queued. Any launch failure is journalled: the
// updater only starts writing update.log itself, so a unit that never
// started would otherwise fail without a trace.
func startTransientUnit(script string) error {
	unit := fmt.Sprintf("webkvm-update-%d", time.Now().UnixNano())
	out, err := exec.Command("systemd-run", "--quiet", "--no-block", "--collect",
		"--unit="+unit, "/bin/bash", "-c", script).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	_ = exec.Command("logger", "-t", "webkvm-update", "launch failed: "+msg).Run()
	return errors.New(msg)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

// latestReleaseAPI is the GitHub endpoint for the newest published
// release. Keep it in sync with RELEASE_API in packaging/standalone/
// install.sh and update.sh.
const latestReleaseAPI = "https://api.github.com/repos/Slaker19/webkvm/releases/latest"

// checkLatestVersion asks the GitHub API for the latest release. It is
// best-effort and never blocks the response for more than ~2s.
func checkLatestVersion(ctx context.Context, _ string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", latestReleaseAPI, nil)
	if err != nil {
		return "", false
	}
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
