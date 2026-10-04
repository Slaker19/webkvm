package api

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/diskprobe"
	"webkvm/internal/models"
	"webkvm/internal/safego"

	"github.com/go-chi/chi/v5"
)

// ---- Job Tracking (thread-safe) ----
var (
	isoJobs = make(map[string]*models.DownloadJob)
	jobsMu  sync.RWMutex
)

// Job GC policy: finished jobs (completed/error) older than
// jobTTL are purged by a dedicated goroutine every jobSweepInterval.
// queued/running jobs are NEVER purged, no matter how old they get — an
// in-flight download keeps its entry until it reaches a terminal state.
const (
	jobTTL           = 24 * time.Hour
	jobSweepInterval = 5 * time.Minute
)

var poolPathDenyList = []string{
	"/etc",
	"/proc",
	"/sys",
	"/boot",
	"/dev",
	"/var/lib/libvirt",
	"/var/log",
	"/root",
	"/home",
}

var poolPathAllowRE = regexp.MustCompile(`^/[a-zA-Z0-9/._ -]+$`)

// defaultPoolDirName turns a pool name into the folder name used when
// the pool is created on the system disk without an explicit path.
//
// The name arrives from the client and becomes a path segment, so it is
// checked rather than escaped: a name that cannot be a folder name is
// an error the operator should see now, not a silently mangled folder
// they will not recognise later. Refusing is also what keeps "..", "/"
// and friends from walking out of the pools directory.
func defaultPoolDirName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." ||
		name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("pool name %q cannot be used as a folder name; give an explicit path", name)
	}
	if !poolDirNameRE.MatchString(name) {
		return "", fmt.Errorf("pool name %q has characters that cannot be used as a folder name (allowed: letters, digits, dot, dash, underscore); give an explicit path", name)
	}
	return name, nil
}

var poolDirNameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func validatePoolPath(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("must be an absolute path")
	}
	if !poolPathAllowRE.MatchString(p) {
		return fmt.Errorf("path contains invalid characters; allowed: [a-zA-Z0-9/._ -]")
	}
	for _, deny := range poolPathDenyList {
		if p == deny || strings.HasPrefix(p, deny+"/") {
			return fmt.Errorf("path %q is reserved (denied: %s)", p, deny)
		}
	}
	return nil
}

func storeJob(j *models.DownloadJob) {
	jobsMu.Lock()
	j.UpdatedAt = time.Now().Unix()
	isoJobs[j.ID] = j
	jobsMu.Unlock()
}

func getJob(id string) (models.DownloadJob, bool) {
	jobsMu.RLock()
	defer jobsMu.RUnlock()
	j, ok := isoJobs[id]
	if !ok {
		return models.DownloadJob{}, false
	}
	return *j, true
}

// updateJob records a job's progress. The last argument is the job's
// text for this step; it is filed as the error only when the status
// says the job failed, and as commentary otherwise. Call sites pass
// both kinds of string, and routing every one of them into Error left
// successful jobs looking broken to anything reading that field.
func updateJob(id string, progress float64, status string, text string) {
	updateJobFull(id, progress, status, text, 0, 0, 0, 0)
}

func updateJobFull(id string, progress float64, status string, text string, bytesDone, bytesTotal, speedBps, etaSeconds int64) {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	if j, ok := isoJobs[id]; ok {
		j.Progress = progress
		j.Status = status
		if status == "error" {
			j.Error = text
		} else {
			j.Message = text
			// A retry that succeeds must not keep displaying the
			// failure that preceded it.
			j.Error = ""
		}
		j.UpdatedAt = time.Now().Unix()
		if bytesDone > 0 || bytesTotal > 0 {
			j.BytesDone = bytesDone
			j.BytesTotal = bytesTotal
		}
		if speedBps > 0 {
			j.SpeedBps = speedBps
		}
		if etaSeconds >= 0 {
			j.ETASeconds = etaSeconds
		}
	}
}

// pruneExpiredJobs removes jobs that reached a terminal state
// (completed/error) more than ttl ago. queued/running jobs are never
// touched. Thread-safe. Returns the number of purged jobs.
func pruneExpiredJobs(now time.Time, ttl time.Duration) int {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	cutoff := now.Add(-ttl).Unix()
	n := 0
	for id, j := range isoJobs {
		// Two terminal-status conventions coexist: download/appliance
		// jobs use "completed", while submitJob (clone/snapshot/deploy/
		// probe) uses "done". Purging only "completed"/"error" leaked
		// every "done" job forever — an unbounded map growth.
		terminal := j.Status == "completed" || j.Status == "done" || j.Status == "error"
		if terminal && j.UpdatedAt > 0 && j.UpdatedAt <= cutoff {
			delete(isoJobs, id)
			n++
		}
	}
	return n
}

// StartJobSweeper launches the background garbage collector for finished
// download/appliance jobs. It ticks every interval and purges only
// terminal jobs older than ttl; the loop stops when ctx is cancelled
// (server shutdown). log is optional and receives a single line per run
// that actually purged something.
func StartJobSweeper(ctx context.Context, interval, ttl time.Duration, log func(msg string, args ...any)) {
	go func() {
		defer safego.Recover("job_sweeper")
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n := pruneExpiredJobs(time.Now(), ttl)
				if n > 0 && log != nil {
					log("jobs_purged", "count", n, "ttl", ttl.String())
				}
			}
		}
	}()
}

// progressReportingWriter wraps dst and reports the cumulative number of
// bytes written via report(n). It lets long-running downloads surface live
// progress to the job tracker instead of jumping from 0% to 100%.
type progressReportingWriter struct {
	w       io.Writer
	total   int64
	written int64
	report  func(n int64)
}

func (p *progressReportingWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if n > 0 {
		p.report(p.written)
	}
	return n, err
}

// safeISOFilename rejects names that contain path separators or `..`
// components up front, then returns filepath.Base of the result. This
// means a filename like "../../etc/passwd" is rejected with a clear
// error rather than silently rewritten to "passwd".
func safeISOFilename(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("filename is required")
	}
	if strings.ContainsAny(trimmed, "/\\") {
		return "", fmt.Errorf("filename must not contain path separators")
	}
	if strings.Contains(trimmed, "..") {
		return "", fmt.Errorf("filename must not contain '..'")
	}
	// Reject control characters and null bytes.
	if strings.ContainsAny(trimmed, "\x00\n\r\t") {
		return "", fmt.Errorf("filename contains invalid characters")
	}
	cleaned := filepath.Base(trimmed)
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("invalid filename")
	}
	return cleaned, nil
}

// safeDownloadURL blocks requests aimed at loopback, private, or
// link-local addresses (RFC1918, IPv4 link-local including cloud
// metadata 169.254.169.254, IPv6 ULA, etc). Returns nil if the URL
// is safe, otherwise an error.
func safeDownloadURL(raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL must include a host")
	}
	// Quick textual check for the common local names.
	switch strings.ToLower(host) {
	case "localhost", "ip6-localhost", "ip6-loopback":
		return fmt.Errorf("URL host is not allowed")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("could not resolve host: %w", err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("URL resolves to a blocked address: %s", ip)
		}
	}
	return nil
}

// isBlockedIP returns true if ip is loopback, private, link-local,
// multicast, or otherwise unsuitable for outbound HTTP from a
// server-side fetch.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// 169.254.0.0/16 (cloud metadata) and 100.64.0.0/10 (CGNAT) aren't
	// covered by the standard library categorisation in all versions.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
	}
	return false
}

// secureDownloadWithProgress downloads url to destPath using a
// DNS-rebind-safe transport, reporting progress via updateJob(jobID, ...)
// as it goes. This is the single entry point every server-side fetch must
// use: ISO downloads, appliance/OVA images and cloud base images all share
// it, so the SSRF protections (blocked address ranges, redirect
// re-validation) can never drift apart between call sites again.
func secureDownloadWithProgress(jobID, url, destPath string, timeout time.Duration, maxBytes int64) (int64, error) {
	if err := safeDownloadURL(url); err != nil {
		return 0, err
	}
	return downloadWithClient(newSafeDownloadClient(timeout), jobID, url, destPath, maxBytes)
}

// newSafeDownloadClient builds the DNS-rebind-safe http.Client shared by
// every download call site: it resolves the host itself, refuses every
// blocked address (loopback/private/link-local/CGNAT), dials the approved
// IP directly (so no second lookup can re-open a rebinding window), and
// re-validates each redirect target.
func newSafeDownloadClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.LookupIP(host)
			if err != nil {
				return nil, fmt.Errorf("resolve blocked: %w", err)
			}
			var lastErr error
			for _, ip := range ips {
				if isBlockedIP(ip) {
					continue
				}
				conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if derr == nil {
					return conn, nil
				}
				lastErr = derr
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("connection to %s is not allowed", host)
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-http scheme blocked")
			}
			if err := safeDownloadURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}

// downloadWithClient performs the GET and the progress-reporting copy with
// an already-built client. Split out from secureDownloadWithProgress so
// tests can inject a plain client against httptest.NewServer, which is
// unavoidably on 127.0.0.1 and would otherwise always be refused by the
// loopback check in safeDownloadURL.
func downloadWithClient(client *http.Client, jobID, url, destPath string, maxBytes int64) (int64, error) {
	if strings.Contains(destPath, "..") {
		return 0, fmt.Errorf("invalid destination path: traversal not allowed")
	}
	resp, err := client.Get(url)
	if err != nil {
		return 0, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Read one byte beyond the advertised limit so an oversized response is
	// detected instead of being silently truncated and marked completed.
	limitReader := io.LimitReader(resp.Body, maxBytes+1)
	dst, err := os.Create(destPath)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer dst.Close()

	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	updateJobFull(jobID, 0, "downloading", "", 0, total, 0, 0)
	// Progress updates are throttled to ~4/s so a fast download can't
	// hammer the job mutex.
	var lastReport time.Time
	var lastBytes int64
	var smoothedSpeed float64

	prog := &progressReportingWriter{
		w:     dst,
		total: total,
		report: func(n int64) {
			now := time.Now()
			if n < total && now.Sub(lastReport) < 250*time.Millisecond {
				return
			}
			var speedBps int64
			var etaSeconds int64
			if !lastReport.IsZero() {
				elapsed := now.Sub(lastReport).Seconds()
				if elapsed > 0 {
					currentSpeed := float64(n-lastBytes) / elapsed
					if smoothedSpeed == 0 {
						smoothedSpeed = currentSpeed
					} else {
						smoothedSpeed = smoothedSpeed*0.7 + currentSpeed*0.3
					}
					speedBps = int64(smoothedSpeed)
					if speedBps > 0 && total > n {
						etaSeconds = (total - n) / speedBps
					}
				}
			}
			lastReport = now
			lastBytes = n

			pct := 0.0
			if total > 0 {
				pct = float64(n) / float64(total) * 100
				if pct > 100 {
					pct = 100
				}
			}
			updateJobFull(jobID, pct, "downloading", "", n, total, speedBps, etaSeconds)
		},
	}
	written, err := io.Copy(prog, limitReader)
	if err != nil {
		_ = os.Remove(destPath)
		return written, fmt.Errorf("download interrupted: %w", err)
	}
	if written > maxBytes {
		_ = dst.Close()
		_ = os.Remove(destPath)
		return written, fmt.Errorf("download exceeds the %d byte limit", maxBytes)
	}
	return written, nil
}

func (h *Handler) ListPools(w http.ResponseWriter, r *http.Request) {
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// V13-DATA-02: scope the list to the caller's AllowedPools so the
	// deploy modal (and every other consumer) only ever sees eligible
	// pools. Admins and unrestricted users see everything.
	if user, role, _ := audit.FromRequest(r); role != models.RoleAdmin && user != "" {
		if u, uerr := h.userStore.Get(user); uerr == nil {
			if set, all := poolAllowSet(u); !all {
				filtered := pools[:0]
				for _, p := range pools {
					if set[p.Name] {
						filtered = append(filtered, p)
					}
				}
				pools = filtered
			}
		}
	}
	jsonResp(w, http.StatusOK, pools)
}

// PoolBreakdown is the per-pool storage split shown as a segmented bar
// in the UI.
type PoolBreakdown struct {
	Pool      string `json:"pool"`
	Path      string `json:"path"`
	Purpose   string `json:"purpose"`
	Capacity  int64  `json:"capacity"`
	Available int64  `json:"available"`
	// DeviceID lets the frontend dedupe pools that share one physical
	// filesystem: libvirt reports the WHOLE filesystem's capacity for
	// every dir pool on it, so summing them double-counts the disk.
	DeviceID uint64 `json:"device_id,omitempty"`
	// Bytes by category. Volumes are split by extension because that's
	// what actually distinguishes them on a dir pool: .iso is optical
	// media, everything else is a VM/container disk image.
	DiskBytes   int64 `json:"disk_bytes"`
	ISOBytes    int64 `json:"iso_bytes"`
	BackupBytes int64 `json:"backup_bytes"`
	OtherBytes  int64 `json:"other_bytes"`
	// FreeBytes is Available, surfaced here so the bar can render the
	// unused remainder without a second lookup.
	FreeBytes int64 `json:"free_bytes"`
}

// GetStorageBreakdown reports what is actually consuming each pool,
// so the Storage page can show a segmented bar (VM disks / ISOs /
// backups / free) instead of a single opaque "62% used".
//
// Sizes use each volume's ALLOCATION, not its declared capacity: a
// sparse 100 GiB qcow2 holding 4 GiB must count as 4 GiB, otherwise
// thin-provisioned pools would always look impossibly overcommitted.
func (h *Handler) GetStorageBreakdown(w http.ResponseWriter, r *http.Request) {
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Same AllowedPools scoping as ListPools — a restricted user must
	// not learn about pools they can't use.
	if user, role, _ := audit.FromRequest(r); role != models.RoleAdmin && user != "" {
		if u, uerr := h.userStore.Get(user); uerr == nil {
			if set, all := poolAllowSet(u); !all {
				filtered := pools[:0]
				for _, p := range pools {
					if set[p.Name] {
						filtered = append(filtered, p)
					}
				}
				pools = filtered
			}
		}
	}

	out := make([]PoolBreakdown, 0, len(pools))
	for _, p := range pools {
		b := PoolBreakdown{
			Pool: p.Name, Path: p.Path, Purpose: p.Purpose,
			Capacity: p.Capacity, Available: p.Available,
			DeviceID: p.DeviceID, FreeBytes: p.Available,
		}
		vols, verr := h.compute.ListStorageVolumes(p.Name)
		if verr != nil {
			// A pool that can't be listed (inactive, unreachable netfs)
			// still belongs in the response with its capacity so the UI
			// can show it as unknown rather than dropping it entirely.
			out = append(out, b)
			continue
		}
		for _, v := range vols {
			// Internal qcow2 snapshots are a VIEW of an existing file,
			// not a separate one on disk — their bytes are already
			// counted in the parent volume, so adding them would
			// double-count and can push the bar past 100%.
			if v.IsSnapshot {
				continue
			}
			size := v.Allocated
			if size <= 0 {
				size = v.Capacity
			}
			// The pool's declared purpose outranks the filename: in a
			// pool that exists to hold install media, a file without
			// the .iso extension (netboot images, .img) is still
			// install media, and it used to land in "other" — an
			// unlabelled slice of the dashboard bar. Same for a
			// backup pool, whose contents are archives by definition.
			switch {
			case compute.HasPurpose(p.Purpose, compute.PoolPurposeISO),
				strings.HasSuffix(strings.ToLower(v.Name), ".iso"):
				b.ISOBytes += size
			case compute.HasPurpose(p.Purpose, compute.PoolPurposeBackup),
				isBackupVolumeName(v.Name):
				b.BackupBytes += size
			case v.Format != "":
				b.DiskBytes += size
			default:
				b.OtherBytes += size
			}
		}
		out = append(out, b)
	}
	jsonResp(w, http.StatusOK, out)
}

// isBackupVolumeName matches the archive artifacts the backup runner
// writes, so they're attributed to "backups" rather than counted as
// live VM disks.
func isBackupVolumeName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".tar") || strings.HasSuffix(n, ".tar.gz") ||
		strings.HasSuffix(n, ".tar.zst") || strings.HasSuffix(n, ".zst") ||
		strings.HasSuffix(n, ".ova")
}

func (h *Handler) CreatePool(w http.ResponseWriter, r *http.Request) {
	var req models.CreatePoolRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	isContainerPool := req.Purpose == compute.PoolPurposeContainer || req.Purpose == "lxc" || strings.HasPrefix(req.Type, "incus-") || req.Type == "container"
	if req.Type == "iscsi" {
		if req.Path == "" {
			req.Path = "/dev/disk/by-path"
		}
		if req.SourceHost == "" {
			jsonErr(w, http.StatusBadRequest, "source_host is required for iscsi pool")
			return
		}
		targetIQN := req.SourceDevice
		if targetIQN == "" {
			targetIQN = req.SourceIQN
		}
		if targetIQN == "" {
			jsonErr(w, http.StatusBadRequest, "source_device (target IQN) is required for iscsi pool")
			return
		}
		req.SourceDevice = targetIQN
		if (req.SourceUsername != "") != (req.SourcePassword != "") {
			jsonErr(w, http.StatusBadRequest,
				"iscsi chap auth requires both source_username and source_password")
			return
		}
	}

	// A directory pool with no path lands on the system disk, next to
	// the built-in pools, in a folder of its own named after the pool.
	//
	// Demanding a path for this case was friction with no payoff: the
	// operator who has not mounted a second disk has exactly one
	// sensible answer, and making them type it by hand is the step
	// where a typo silently creates a pool rooted somewhere it should
	// not be. The derived path still goes through validatePoolPath
	// below, so nothing is trusted just because we built it.
	if req.Path == "" && !isContainerPool && (req.Type == "dir" || req.Type == "") {
		safe, err := defaultPoolDirName(req.Name)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Path = filepath.Join(h.cfg.PoolsDir(), safe)
	}
	if req.Path == "" && !isContainerPool {
		jsonErr(w, http.StatusBadRequest, "path is required")
		return
	}
	// CIFS auth: both username and password must be present together.
	// Rejecting here (before any libvirt call) keeps partial config
	// from silently degrading to anonymous mount.
	if strings.EqualFold(req.SourceFormat, "cifs") {
		if (req.SourceUsername != "") != (req.SourcePassword != "") {
			jsonErr(w, http.StatusBadRequest,
				"cifs auth requires both source_username and source_password")
			return
		}
	}
	// Purpose is always a SINGLE nature: every pool is independent
	// and rooted at its own folder (a disk with several purposes gets
	// one pool per purpose, e.g. mydisk-vdi + mydisk-isos +
	// mydisk-containers). Unified multi-purpose pools are rejected
	// on purpose.
	if req.Purpose != "" {
		one, ok := parsePoolPurpose(req.Purpose)
		if !ok {
			jsonErr(w, http.StatusBadRequest,
				"purpose must be a single value: 'disk', 'iso', 'container', 'backup' or 'template'")
			return
		}
		req.Purpose = one // normalized ("lxc" -> "container")
	}
	if req.Path != "" && req.Type != "iscsi" {
		if err := validatePoolPath(req.Path); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid path: "+err.Error())
			return
		}
	} else if req.Path != "" && req.Type == "iscsi" {
		if !filepath.IsAbs(req.Path) || !poolPathAllowRE.MatchString(req.Path) {
			jsonErr(w, http.StatusBadRequest, "invalid iscsi path")
			return
		}
	}
	pool, err := h.compute.CreateStoragePool(r.Context(), req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "storage.pool.create", pool.Name, map[string]any{
		"type":    req.Type,
		"purpose": req.Purpose,
		"format":  req.SourceFormat,
		"auth":    req.SourceUsername != "",
	}))
	jsonResp(w, http.StatusCreated, pool)
}

// poolRetagBlockers reports the files in a pool that the new purpose
// would misfile, so a retag can be refused before it hides anything.
//
// Retagging is a metadata write with no data movement, which is
// exactly why it is dangerous on a populated pool: flipping a disk
// pool full of qcow2 images to "iso" does not touch a single byte, it
// just makes every one of those images invisible to the volume browser
// and unreachable to the VMs that reference them.
//
// The rule is by file extension because that is what the rest of the
// storage layer keys off. An empty pool never blocks, which is the
// case that matters for an operator fixing a purpose they picked
// wrong at creation time.
func poolRetagBlockers(vols []models.StorageVolume, newPurpose string) []string {
	var bad []string
	for _, v := range vols {
		if v.IsSnapshot {
			continue
		}
		isISO := strings.EqualFold(filepath.Ext(v.Name), ".iso")
		switch newPurpose {
		case compute.PoolPurposeISO:
			if !isISO {
				bad = append(bad, v.Name)
			}
		case compute.PoolPurposeDisk, compute.PoolPurposeTemplate:
			if isISO {
				bad = append(bad, v.Name)
			}
		}
	}
	return bad
}

// UpdatePool handles PUT /api/storage/pools/{name}.
//
// Supported operations:
//   - Retag the pool's purpose (disk/iso/container/backup/template).
//   - Rotate the libvirt CIFS secret (with or without new credentials).
//   - Trigger a cifs-needs-reauth re-define (for libvirtd reinstall
//     recovery).
//
// Unsupported operations (path/source/format changes) return 400
// because libvirt cannot live-update them on a running pool.
func (h *Handler) UpdatePool(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		jsonErr(w, http.StatusBadRequest, "pool name required")
		return
	}
	var req models.UpdatePoolRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// CIFS/CHAP auth fields must come as a pair. Without this guard, a
	// caller could pass a new password but no username, which would
	// produce a misconfigured auth block (or no auth at all).
	hasUser := req.SourceUsername != nil
	hasPass := req.SourcePassword != nil
	if hasUser != hasPass {
		jsonErr(w, http.StatusBadRequest,
			"auth requires both source_username and source_password")
		return
	}
	if req.Purpose != nil {
		one, ok := parsePoolPurpose(*req.Purpose)
		if !ok {
			jsonErr(w, http.StatusBadRequest,
				"purpose must be a single value: 'disk', 'iso', 'container', 'backup' or 'template'")
			return
		}
		current, exists := h.lookupPoolPurpose(name)
		if !exists {
			jsonErr(w, http.StatusNotFound, fmt.Sprintf("storage pool %q does not exist", name))
			return
		}
		// Crossing the libvirt/Incus line is not a retag, it is a
		// different pool on a different engine. Refusing here keeps
		// pool-purposes.json from claiming a libvirt pool is an Incus
		// one (and vice versa), which nothing downstream could honour.
		if (current == compute.PoolPurposeContainer) != (one == compute.PoolPurposeContainer) {
			jsonErr(w, http.StatusBadRequest,
				"a container pool lives in Incus and the others in libvirt; retagging across the two is not possible — create the pool you need instead")
			return
		}
		if one == current {
			// No-op retag. Dropping the field matters: left in place
			// it would fall through to the CIFS reauth path below,
			// which rejects every pool that is not netfs/cifs — so
			// re-sending a pool's own purpose would fail.
			req.Purpose = nil
		} else {
			if vols, verr := h.compute.ListStorageVolumes(name); verr == nil {
				if bad := poolRetagBlockers(vols, one); len(bad) > 0 {
					shown := bad
					if len(shown) > 5 {
						shown = shown[:5]
					}
					jsonErr(w, http.StatusConflict, fmt.Sprintf(
						"pool %q holds %d file(s) that do not belong in a %q pool (%s) — move them first",
						name, len(bad), one, strings.Join(shown, ", ")))
					return
				}
			}
			req.Purpose = &one
		}
		// A purpose-only no-op still has to answer with the pool,
		// not with a reauth error the caller never asked about.
		if req.Purpose == nil && !req.CifsNeedsReauth && !req.ChapNeedsReauth && !hasUser {
			pools, _ := h.compute.ListStoragePools()
			for _, p := range pools {
				if p.Name == name {
					jsonResp(w, http.StatusOK, p)
					return
				}
			}
			jsonErr(w, http.StatusNotFound, fmt.Sprintf("storage pool %q does not exist", name))
			return
		}
	}
	pool, err := h.compute.UpdateStoragePool(r.Context(), name, req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	details := map[string]any{"reauth": req.CifsNeedsReauth || req.ChapNeedsReauth}
	if req.Purpose != nil {
		details["purpose"] = *req.Purpose
	}
	h.audit.Log(auditFor(r, "storage.pool.update", name, details))
	jsonResp(w, http.StatusOK, pool)
}

// volumeInUse refuses destructive storage operations when the given
// attachments list is non-empty, writing a 409 with a human-readable
// list of the blocking VMs. Returns true when the request was refused.
func volumeInUse(w http.ResponseWriter, what string, atts []models.VolumeAttachment) bool {
	if len(atts) == 0 {
		return false
	}
	names := make([]string, 0, len(atts))
	for _, a := range atts {
		names = append(names, fmt.Sprintf("%s (%s)", a.VMName, a.State))
	}
	jsonResp(w, http.StatusConflict, map[string]any{
		"error":       fmt.Sprintf("%s is in use by: %s — detach it first", what, strings.Join(names, ", ")),
		"attachments": atts,
	})
	return true
}

// isBuiltinPool reports whether name is one of the pools WebKVM creates
// and maintains on the system disk. "ISOS" is the pre-v2.5 name of the
// ISO library: an install whose migration has not run yet (or could not
// run) must not have it deleted out from under the migration.
func isBuiltinPool(name string) bool {
	switch name {
	case config.DiskPoolName, config.ISOPoolName, config.IncusPoolName, "ISOS":
		return true
	}
	return false
}

func (h *Handler) DeletePool(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		jsonErr(w, http.StatusBadRequest, "pool name required")
		return
	}
	// Guard: WebKVM's own pools on the system disk are not deletable.
	// The server recreates them on the next start, so deleting one does
	// not even achieve what the operator asked for — it just detaches
	// libvirt from a directory that still holds their disks and ISOs.
	// Storage.svelte hides the button for these, but the button was the
	// ONLY thing standing in the way: the API accepted the call.
	if isBuiltinPool(name) {
		jsonErr(w, http.StatusConflict, fmt.Sprintf(
			"pool %q is a built-in WebKVM pool and cannot be deleted", name))
		return
	}
	// Guard: refuse to delete a pool whose volumes are attached to VMs.
	// An inactive pool ("not active", not just "not found") can't have
	// any live attachments either — a netfs pool whose remote mount
	// failed or dropped is a real state, and an admin must still be
	// able to delete it to clean up, or they'd be stuck forever.
	vols, err := h.compute.ListStorageVolumes(name)
	if err != nil && !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "not active") {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var allAtts []models.VolumeAttachment
	for _, v := range vols {
		atts, aerr := h.compute.FindVolumeAttachments(name, v.Name)
		if aerr != nil {
			continue
		}
		allAtts = append(allAtts, atts...)
	}
	if len(allAtts) > 0 {
		names := make([]string, 0, len(allAtts))
		for _, a := range allAtts {
			names = append(names, fmt.Sprintf("%s (%s)", a.VMName, a.State))
		}
		jsonErr(w, http.StatusConflict, fmt.Sprintf(
			"cannot delete pool %q: %d disk(s) still attached to VMs (%s) — detach them first",
			name, len(allAtts), strings.Join(names, ", ")))
		return
	}
	if err := h.compute.DeletePool(name); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "pool deleted"})
}

func (h *Handler) ListVolumes(w http.ResponseWriter, r *http.Request) {
	poolName := r.URL.Query().Get("pool")
	if poolName == "" {
		poolName = config.DiskPoolName
	}
	// Same AllowedPools scoping as ListPools: a restricted user must not
	// enumerate volumes (and thus filenames/sizes) of pools they cannot use.
	if !h.poolVisibleTo(r, poolName) {
		jsonResp(w, http.StatusOK, []models.StorageVolume{})
		return
	}
	vols, err := h.compute.ListStorageVolumes(poolName)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, vols)
}

// ProbeStorageDisk inspects a pool disk image by absolute path. Body:
// {"path": "...", "deep": bool}. Read-only. Used by the Add Disk dialog
// to warn when an "existing disk" already contains data before the
// operator attaches it.
func (h *Handler) ProbeStorageDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Deep bool   `json:"deep"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		jsonErr(w, http.StatusBadRequest, "path is required")
		return
	}
	if err := h.validateDiskSourcePath(req.Path); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	pool, vol, ok := h.resolveVolumeFromSource(req.Path)
	if !ok {
		jsonErr(w, http.StatusForbidden, "source path is not inside a known storage pool")
		return
	}
	if !h.poolVisibleTo(r, pool) {
		jsonErr(w, http.StatusForbidden, "access denied to storage pool")
		return
	}
	if atts, err := h.compute.FindVolumeAttachments(pool, vol); err == nil && len(atts) > 0 {
		for _, att := range atts {
			if err := h.requireVMAccess(r, att.VMID); err != nil {
				jsonErr(w, http.StatusForbidden, "access denied: volume is in use by another instance")
				return
			}
		}
	}
	if req.Deep {
		inspector, ok := diskprobe.InspectorAvailable()
		if !ok {
			inspector = ""
		}
		job := submitJob(jobOwner(r), "disk-probe", func() (any, error) {
			cctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			return diskprobe.Deep(cctx, req.Path, inspector), nil
		})
		jsonResp(w, http.StatusAccepted, job)
		return
	}
	res, err := diskprobe.Basic(r.Context(), req.Path)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, res)
}

func (h *Handler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	var req models.CreateVolumeRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Capacity <= 0 {
		jsonErr(w, http.StatusBadRequest, "name and capacity are required")
		return
	}
	if req.Pool == "" {
		req.Pool = config.DiskPoolName
	}
	if !h.requireDiskPool(w, req.Pool) {
		return
	}
	// A raw volume can be attached to a VM as an "existing disk" later,
	// so it must go through the same pool-ACL and disk-quota checks as
	// any other disk allocation, or a restricted user could create an
	// unmetered volume on a disallowed pool and attach it afterward.
	owner, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin {
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := assertPoolAllowed(u, req.Pool); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if err := h.checkDiskQuota(owner, map[string]int64{req.Pool: req.Capacity}); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
	}
	vol, err := h.compute.CreateStorageVolume(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusCreated, vol)
}

func (h *Handler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	pool := chi.URLParam(r, "pool")
	name := chi.URLParam(r, "name")
	if !h.poolVisibleTo(r, pool) {
		jsonErr(w, http.StatusForbidden, "access denied to storage pool")
		return
	}
	// Guard: refuse to delete a volume that is attached to any VM,
	// running or not. Detach the disk first.
	if atts, err := h.compute.FindVolumeAttachments(pool, name); err == nil && len(atts) > 0 {
		volumeInUse(w, fmt.Sprintf("volume %s/%s", pool, name), atts)
		return
	}
	// Being unattached is not the same as being unused: a linked clone
	// records its backing file inside its own qcow2 header, so the
	// attachment check above cannot see that dependency. The disk the
	// VM has attached is the overlay, never the image underneath it.
	if h.assertNoBackingDependents(w, fmt.Sprintf("volume %s/%s", pool, name), pool, name) {
		return
	}

	if err := h.compute.DeleteStorageVolume(pool, name); err != nil {
		if errors.Is(err, compute.ErrVolumeNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) ResizeVolume(w http.ResponseWriter, r *http.Request) {
	pool := chi.URLParam(r, "pool")
	name := chi.URLParam(r, "name")
	var req struct {
		Capacity int64 `json:"capacity"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Capacity <= 0 {
		jsonErr(w, http.StatusBadRequest, "capacity must be positive")
		return
	}
	owner, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin {
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := assertPoolAllowed(u, pool); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if cur, err := h.compute.GetStorageVolume(pool, name); err == nil {
			delta := req.Capacity - bytesToGB(cur.Capacity)
			if delta > 0 {
				if err := h.checkDiskQuota(owner, map[string]int64{pool: delta}); err != nil {
					jsonErr(w, http.StatusConflict, err.Error())
					return
				}
			}
		}
	}
	if err := h.compute.ResizeStorageVolume(pool, name, req.Capacity); err != nil {
		if errors.Is(err, compute.ErrVolumeNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "resized"})
}

func (h *Handler) ListISOs(w http.ResponseWriter, r *http.Request) {
	poolName := r.URL.Query().Get("pool")
	if poolName != "" {
		if !h.poolVisibleTo(r, poolName) {
			jsonResp(w, http.StatusOK, []models.ISOScanResult{})
			return
		}
		isos, err := h.compute.GetISOs(poolName)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonResp(w, http.StatusOK, isos)
		return
	}
	// No pool specified → aggregate ISOs from every pool. Purpose is
	// just an informational label, not a restriction on where an ISO
	// can live, so a pool tagged "disk" is scanned too — otherwise an
	// ISO uploaded there would be invisible in the default "all pools"
	// view even though GetISOs(pool) finds it fine when asked directly.
	allPools, err := h.compute.ListStoragePools()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var isos []models.ISOScanResult
	for _, p := range allPools {
		if !h.poolVisibleTo(r, p.Name) {
			continue
		}
		poolISOs, err := h.compute.GetISOs(p.Name)
		if err != nil {
			continue
		}
		isos = append(isos, poolISOs...)
	}
	if isos == nil {
		isos = []models.ISOScanResult{}
	}
	jsonResp(w, http.StatusOK, isos)
}

func (h *Handler) DeleteISO(w http.ResponseWriter, r *http.Request) {
	pool := chi.URLParam(r, "pool")
	name := chi.URLParam(r, "name")
	if pool == "" {
		pool = config.ISOPoolName
	}
	if !h.poolVisibleTo(r, pool) {
		jsonErr(w, http.StatusForbidden, "access denied to storage pool")
		return
	}
	// Guard: refuse to delete an ISO mounted in any VM's CD-ROM.
	if atts, err := h.compute.FindVolumeAttachments(pool, name); err == nil && len(atts) > 0 {
		volumeInUse(w, fmt.Sprintf("ISO %s/%s", pool, name), atts)
		return
	}
	if err := h.compute.DeleteISO(name, pool); err != nil {
		if errors.Is(err, compute.ErrVolumeNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "iso deleted"})
}

func (h *Handler) RenameISO(w http.ResponseWriter, r *http.Request) {
	pool := chi.URLParam(r, "pool")
	name := chi.URLParam(r, "name")
	if pool == "" {
		pool = config.ISOPoolName
	}
	if !h.poolVisibleTo(r, pool) {
		jsonErr(w, http.StatusForbidden, "access denied to storage pool")
		return
	}
	var req struct {
		NewName string `json:"new_name"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.NewName == "" {
		jsonErr(w, http.StatusBadRequest, "new_name is required")
		return
	}
	safeNew, err := safeISOFilename(req.NewName)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.compute.RenameISO(name, safeNew, pool); err != nil {
		if errors.Is(err, compute.ErrVolumeNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "renamed", "name": safeNew})
}

// poolPurposeFor resolves the declared purpose ("iso"/"disk") of a storage
// pool. It returns "" when the pool is unknown or the backend cannot report
// a purpose (e.g. before it is defined).
func (h *Handler) poolPurposeFor(name string) string {
	purpose, _ := h.lookupPoolPurpose(name)
	return purpose
}

// lookupPoolPurpose returns the pool's declared purpose and whether the
// pool exists at all. The two cases must stay apart: an existing pool
// with no declared purpose defaults to "disk", but a pool that does not
// exist must be refused outright. Collapsing both into "" would have let
// requireDiskPool accept any name the caller invented.
func (h *Handler) lookupPoolPurpose(name string) (string, bool) {
	if h.compute == nil {
		return "", false
	}
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		return "", false
	}
	for _, p := range pools {
		if p.Name == name {
			return p.Purpose, true
		}
	}
	return "", false
}

// requireISOPoolForCaller refuses the request when the target pool is
// not designated for ISO images OR the caller's AllowedPools excludes
// it. The pool-purpose check alone used to leave a per-pool leak: a
// restricted operator could upload/list/download an ISO into a pool they
// had no entitlement for. Admins and unrestricted users always pass the
// ACL half.
func (h *Handler) requireISOPoolForCaller(w http.ResponseWriter, r *http.Request, name string) bool {
	if !h.requireISOPool(w, name) {
		return false
	}
	if !h.poolVisibleTo(r, name) {
		jsonErr(w, http.StatusForbidden, fmt.Sprintf("pool %q is not available to this user", name))
		return false
	}
	return true
}

// requireISOPool refuses the request when the target pool is not
// designated for ISO images. Writing an ISO into a "disk" pool used to be
// silently allowed, which clutters the VM disk pool and makes the ISO
// invisible in purpose-scoped views; now it is a 400. Unknown pools are
// rejected too (GetPoolPath would fail later anyway).
func (h *Handler) requireISOPool(w http.ResponseWriter, name string) bool {
	purpose, ok := h.lookupPoolPurpose(name)
	if !ok || !compute.HasPurpose(purpose, compute.PoolPurposeISO) {
		jsonErr(w, http.StatusBadRequest,
			fmt.Sprintf("pool %q is not designated for ISO images", name))
		return false
	}
	return true
}

// requireDiskPool refuses the request when the target pool is not
// designated for disk images. Symmetric to requireISOPool: without this,
// a disk image (uploaded, restored, or deployed) could be written into
// the ISO pool, cluttering it and making the disk invisible in
// purpose-scoped views. Unknown pools are rejected too.
func (h *Handler) requireDiskPool(w http.ResponseWriter, name string) bool {
	purpose, ok := h.lookupPoolPurpose(name)
	if !ok || !compute.HasPurpose(purpose, compute.PoolPurposeDisk) {
		jsonErr(w, http.StatusBadRequest,
			fmt.Sprintf("pool %q is not designated for disk images", name))
		return false
	}
	return true
}

func (h *Handler) UploadISO(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<30)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		jsonErr(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "missing file field: "+err.Error())
		return
	}
	defer file.Close()

	name, err := safeISOFilename(header.Filename)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		jsonErr(w, http.StatusBadRequest, "invalid filename")
		return
	}
	if filepath.Ext(name) == "" {
		name += ".iso"
	}

	poolName := r.FormValue("pool")
	if poolName == "" {
		poolName = config.ISOPoolName
	}
	if strings.Contains(poolName, "..") || strings.Contains(poolName, "/") || strings.Contains(poolName, "\\") {
		jsonErr(w, http.StatusBadRequest, "invalid pool name: traversal not allowed")
		return
	}
	if !h.requireISOPoolForCaller(w, r, poolName) {
		return
	}
	poolPath, err := h.compute.GetPoolPath(poolName)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to resolve pool: "+err.Error())
		return
	}
	if strings.Contains(poolPath, "..") {
		jsonErr(w, http.StatusBadRequest, "invalid pool path: traversal not allowed")
		return
	}
	destPath := filepath.Join(poolPath, name)
	if strings.Contains(destPath, "..") {
		jsonErr(w, http.StatusBadRequest, "invalid destination path: traversal not allowed")
		return
	}
	if rel, rerr := filepath.Rel(poolPath, destPath); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		jsonErr(w, http.StatusBadRequest, "invalid destination path")
		return
	}

	dst, err := os.Create(destPath)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to create file: "+err.Error())
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		os.Remove(destPath)
		jsonErr(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
		return
	}

	if err := h.compute.RefreshPool(poolName); err != nil {
		jsonErr(w, http.StatusInternalServerError, "uploaded but failed to refresh pool: "+err.Error())
		return
	}

	jsonResp(w, http.StatusCreated, models.ISOScanResult{
		Path: destPath,
		Name: name,
		Size: written,
		Pool: poolName,
	})
}

// diskUploadExts is the allowlist of disk-image extensions accepted
// by UploadDisk (case-insensitive). Libvirt's dir-pool backend
// auto-detects the real format by inspecting the file itself once
// refreshed, so the extension is just a sanity/consistency check —
// same convention the rest of the app already uses for disk files.
var diskUploadExts = map[string]bool{
	".qcow2": true,
	".img":   true,
	".raw":   true,
	".qed":   true,
	".vmdk":  true,
	".ova":   true,
	".vdi":   true,
	".vhdx":  true,
}

// convertToQcow2 shells out to qemu-img to convert src into a native
// qcow2 image at dst. Returns false (never an error) when qemu-img isn't
// installed or the conversion fails — callers treat both cases the same
// way: keep the original file as-is instead of failing the whole upload.
func convertToQcow2(src, dst string) bool {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		return false
	}
	out, err := exec.Command("qemu-img", "convert", "-O", "qcow2", src, dst).CombinedOutput()
	if err != nil {
		slog.Warn("qemu-img convert failed", "src", src, "dst", dst, "err", err, "output", string(out))
		return false
	}
	return true
}

// extractAndConvertOVA extracts the primary virtual disk from an uploaded .ova tarball
// and converts it to native .qcow2 format inside the destination storage pool.
func extractAndConvertOVA(ovaPath, poolPath, originalName string) (string, string, int64, error) {
	if strings.Contains(ovaPath, "..") || strings.Contains(poolPath, "..") {
		return "", "", 0, fmt.Errorf("invalid path: traversal not allowed")
	}
	if strings.Contains(originalName, "/") || strings.Contains(originalName, "\\") || strings.Contains(originalName, "..") {
		return "", "", 0, fmt.Errorf("invalid original name")
	}

	f, err := os.Open(ovaPath)
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()

	tr := tar.NewReader(f)
	base := strings.TrimSuffix(originalName, filepath.Ext(originalName))
	qcow2Name := base + ".qcow2"
	qcow2Path := filepath.Join(poolPath, qcow2Name)
	if rel, rerr := filepath.Rel(poolPath, qcow2Path); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", "", 0, fmt.Errorf("invalid qcow2 destination path")
	}

	workDir, err := os.MkdirTemp(poolPath, ".webkvm-ova-extract-*")
	if err != nil {
		return "", "", 0, err
	}
	defer os.RemoveAll(workDir)

	var extractedDiskPath string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", 0, err
		}
		// Explicit zip-slip guard: reject paths with directory traversal
		if strings.Contains(hdr.Name, "..") {
			continue
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			// Never materialize symlinks, hardlinks or special files
			// from the archive: only regular files are extracted.
			continue
		}
		if strings.HasSuffix(hdr.Name, ".vmdk") || strings.HasSuffix(hdr.Name, ".qcow2") || strings.HasSuffix(hdr.Name, ".raw") || strings.HasSuffix(hdr.Name, ".img") {
			base := filepath.Base(hdr.Name)
			if base == "" || base == "." || base == "/" {
				continue
			}
			tmpFile := filepath.Join(workDir, base)
			// Defense in depth: prove containment inside workDir even
			// though Base already stripped every directory component
			// (zip-slip). Guards this loop against future refactors.
			if rel, rerr := filepath.Rel(workDir, tmpFile); rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			outF, err := os.Create(tmpFile)
			if err != nil {
				return "", "", 0, err
			}
			if _, err := io.Copy(outF, tr); err != nil {
				outF.Close()
				return "", "", 0, err
			}
			outF.Close()
			extractedDiskPath = tmpFile
			break
		}
	}

	if extractedDiskPath == "" {
		return "", "", 0, fmt.Errorf("no disk image found inside OVA archive")
	}

	if strings.HasSuffix(extractedDiskPath, ".qcow2") {
		if err := os.Rename(extractedDiskPath, qcow2Path); err != nil {
			return "", "", 0, err
		}
	} else if _, err := exec.LookPath("qemu-img"); err == nil {
		cmd := exec.Command("qemu-img", "convert", "-O", "qcow2", extractedDiskPath, qcow2Path)
		if out, cerr := cmd.CombinedOutput(); cerr != nil {
			return "", "", 0, fmt.Errorf("qemu-img convert: %w (%s)", cerr, string(out))
		}
	} else {
		rawName := base + filepath.Ext(extractedDiskPath)
		rawPath := filepath.Join(poolPath, rawName)
		if rel, rerr := filepath.Rel(poolPath, rawPath); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			return "", "", 0, fmt.Errorf("invalid raw destination path")
		}
		if err := os.Rename(extractedDiskPath, rawPath); err != nil {
			return "", "", 0, err
		}
		fi, _ := os.Stat(rawPath)
		return rawPath, rawName, fi.Size(), nil
	}

	fi, err := os.Stat(qcow2Path)
	if err != nil {
		return "", "", 0, err
	}
	return qcow2Path, qcow2Name, fi.Size(), nil
}

// UploadDisk streams an uploaded disk image (.qcow2, .raw, .img, .vmdk, .ova, .vdi, .vhdx)
// directly into a storage pool's directory, auto-converting VMware/VirtualBox formats
// to native QCOW2 where appropriate.
func (h *Handler) UploadDisk(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 500<<30) // 500GB cap, disk images run larger than ISOs

	mr, err := r.MultipartReader()
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
		return
	}

	owner, role, _ := audit.FromRequest(r)
	var poolName, name, destPath string
	var written int64

	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			jsonErr(w, http.StatusBadRequest, "failed to read multipart body: "+perr.Error())
			return
		}

		if part.FormName() == "pool" {
			b, _ := io.ReadAll(io.LimitReader(part, 256))
			poolName = strings.TrimSpace(string(b))
			if strings.Contains(poolName, "..") || strings.Contains(poolName, "/") || strings.Contains(poolName, "\\") {
				jsonErr(w, http.StatusBadRequest, "invalid pool name: traversal not allowed")
				return
			}
			continue
		}
		if part.FormName() != "file" {
			continue
		}

		if poolName == "" {
			poolName = config.DiskPoolName
		}
		if strings.Contains(poolName, "..") || strings.Contains(poolName, "/") || strings.Contains(poolName, "\\") {
			jsonErr(w, http.StatusBadRequest, "invalid pool name: traversal not allowed")
			return
		}
		// Checked before the body is streamed to disk so a rejected pool
		// costs no I/O at all.
		if !h.requireDiskPool(w, poolName) {
			return
		}
		name, err = safeISOFilename(part.FileName())
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
			jsonErr(w, http.StatusBadRequest, "invalid filename")
			return
		}
		if !diskUploadExts[strings.ToLower(filepath.Ext(name))] {
			jsonErr(w, http.StatusBadRequest, "unsupported disk image extension (use .qcow2, .img, .raw, .qed, .vmdk, .ova, .vdi, or .vhdx)")
			return
		}

		// Same ACL + disk-quota gate as CreateVolume: an uploaded disk
		// image can be attached to a VM as an "existing disk" afterward,
		// so it must not let a restricted user bypass pool ACL or quota
		// by uploading instead of creating a blank volume. r.ContentLength
		// is a good-enough upper bound (multipart overhead is negligible
		// next to a real disk image); skipped if the client didn't send it.
		if role != models.RoleAdmin {
			u, uerr := h.userStore.Get(owner)
			if uerr != nil {
				jsonErr(w, http.StatusUnauthorized, "user not found")
				return
			}
			if err := assertPoolAllowed(u, poolName); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
			if r.ContentLength > 0 {
				if err := h.checkDiskQuota(owner, map[string]int64{poolName: bytesToGB(r.ContentLength)}); err != nil {
					jsonErr(w, http.StatusConflict, err.Error())
					return
				}
			}
		}

		poolPath, perr := h.compute.GetPoolPath(poolName)
		if perr != nil {
			jsonErr(w, http.StatusInternalServerError, "failed to resolve pool: "+perr.Error())
			return
		}
		if strings.Contains(poolPath, "..") {
			jsonErr(w, http.StatusBadRequest, "invalid pool path: traversal not allowed")
			return
		}
		destPath = filepath.Join(poolPath, name)
		if strings.Contains(destPath, "..") {
			jsonErr(w, http.StatusBadRequest, "invalid destination path: traversal not allowed")
			return
		}
		if rel, rerr := filepath.Rel(poolPath, destPath); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			jsonErr(w, http.StatusBadRequest, "invalid destination path")
			return
		}

		dst, derr := os.Create(destPath)
		if derr != nil {
			jsonErr(w, http.StatusInternalServerError, "failed to create file: "+derr.Error())
			return
		}
		written, err = io.Copy(dst, part)
		dst.Close()
		if err != nil {
			os.Remove(destPath)
			jsonErr(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
			return
		}
		// A-02: ContentLength is client-controlled and can be absent
		// (chunked) or lower than what was actually written, so re-charge
		// quota against the real on-disk size and delete the file (freeing
		// the space) if the user is now over their cap.
		if role != models.RoleAdmin {
			fi, statErr := os.Stat(destPath)
			if statErr == nil {
				if err := h.checkDiskQuota(owner, map[string]int64{poolName: bytesToGB(fi.Size())}); err != nil {
					os.Remove(destPath)
					jsonErr(w, http.StatusConflict, "upload exceeded disk quota ("+err.Error()+"); file deleted")
					return
				}
			}
		}
	}

	if name == "" {
		jsonErr(w, http.StatusBadRequest, "missing file field")
		return
	}

	poolPath, perr := h.compute.GetPoolPath(poolName)
	if perr == nil {
		switch ext := strings.ToLower(filepath.Ext(name)); ext {
		case ".vmdk", ".vdi", ".vhdx":
			baseName := strings.TrimSuffix(name, ext)
			qcow2Name := baseName + ".qcow2"
			qcow2Path := filepath.Join(poolPath, qcow2Name)
			if strings.Contains(qcow2Path, "..") {
				jsonErr(w, http.StatusBadRequest, "invalid qcow2 destination path: traversal not allowed")
				return
			}
			if rel, rerr := filepath.Rel(poolPath, qcow2Path); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
				jsonErr(w, http.StatusBadRequest, "invalid qcow2 destination path")
				return
			}
			if convertToQcow2(destPath, qcow2Path) {
				os.Remove(destPath)
				destPath = qcow2Path
				name = qcow2Name
				if fi, serr := os.Stat(destPath); serr == nil {
					written = fi.Size()
				}
			}
		case ".ova":
			if convertedPath, cName, cSize, err := extractAndConvertOVA(destPath, poolPath, name); err == nil {
				os.Remove(destPath)
				destPath = convertedPath
				name = cName
				written = cSize
			} else {
				slog.Warn("ova extraction/conversion failed", "err", err)
			}
		}
	}

	if err := h.compute.RefreshPool(poolName); err != nil {
		jsonErr(w, http.StatusInternalServerError, "uploaded but failed to refresh pool: "+err.Error())
		return
	}

	jsonResp(w, http.StatusCreated, map[string]any{
		"name": name,
		"path": destPath,
		"size": written,
		"pool": poolName,
	})
}

func (h *Handler) UploadISOByCURL(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<30)

	name := r.URL.Query().Get("name")
	if name == "" {
		name = "uploaded.iso"
	}
	safe, err := safeISOFilename(name)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name = safe
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		jsonErr(w, http.StatusBadRequest, "invalid filename")
		return
	}
	if filepath.Ext(name) == "" {
		name += ".iso"
	}

	poolName := r.URL.Query().Get("pool")
	if poolName == "" {
		poolName = config.ISOPoolName
	}
	if strings.Contains(poolName, "..") || strings.Contains(poolName, "/") || strings.Contains(poolName, "\\") {
		jsonErr(w, http.StatusBadRequest, "invalid pool name: traversal not allowed")
		return
	}
	if !h.requireISOPoolForCaller(w, r, poolName) {
		return
	}
	poolPath, err := h.compute.GetPoolPath(poolName)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to resolve pool: "+err.Error())
		return
	}
	if strings.Contains(poolPath, "..") {
		jsonErr(w, http.StatusBadRequest, "invalid pool path: traversal not allowed")
		return
	}
	destPath := filepath.Join(poolPath, name)
	if strings.Contains(destPath, "..") {
		jsonErr(w, http.StatusBadRequest, "invalid destination path: traversal not allowed")
		return
	}
	if rel, rerr := filepath.Rel(poolPath, destPath); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		jsonErr(w, http.StatusBadRequest, "invalid destination path")
		return
	}

	dst, err := os.Create(destPath)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to create file: "+err.Error())
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, r.Body)
	if err != nil {
		os.Remove(destPath)
		jsonErr(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
		return
	}

	if err := h.compute.RefreshPool(poolName); err != nil {
		slog.Warn("refresh pool failed after ISO upload", "pool", poolName, "err", err)
	}

	jsonResp(w, http.StatusCreated, models.ISOScanResult{
		Path: destPath,
		Name: name,
		Size: written,
		Pool: poolName,
	})
}

func (h *Handler) DownloadISO(w http.ResponseWriter, r *http.Request) {
	var req models.DownloadISORequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.URL == "" {
		jsonErr(w, http.StatusBadRequest, "url is required")
		return
	}

	if err := safeDownloadURL(req.URL); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	parsedURL, _ := url.ParseRequestURI(req.URL)

	name := req.Name
	if name == "" {
		name = path.Base(parsedURL.Path)
		if name == "" || name == "." || name == "/" {
			name = "downloaded.iso"
		}
	}
	safe, err := safeISOFilename(name)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name = safe
	lowerName := strings.ToLower(name)
	if !strings.HasSuffix(lowerName, ".iso") && !strings.HasSuffix(lowerName, ".img") {
		name += ".iso"
	}

	poolName := req.Pool
	if poolName == "" {
		poolName = config.ISOPoolName
	}
	if !h.requireISOPoolForCaller(w, r, poolName) {
		return
	}

	jobID := fmt.Sprintf("dl_%d", time.Now().UnixNano())
	job := &models.DownloadJob{
		ID:       jobID,
		Name:     name,
		URL:      req.URL,
		Owner:    jobOwner(r),
		Progress: 0,
		Status:   "queued",
		Pool:     poolName,
	}
	storeJob(job)
	// Opportunistic GC on the submit path: finished jobs past the TTL
	// are dropped immediately (in addition to the periodic sweeper), so
	// a burst of activity can't grow the map unboundedly between ticks.
	pruneExpiredJobs(time.Now(), jobTTL)

	user, role, ip := audit.FromRequest(r)
	h.audit.Log(audit.Entry{
		User: user, Role: role, IP: ip, Action: "iso.download",
		Resource: name, Detail: map[string]interface{}{"url": req.URL, "pool": poolName},
	})

	go h.doDownloadISO(jobID, name, poolName)

	jsonResp(w, http.StatusAccepted, map[string]string{"job_id": jobID, "status": "started"})
}

func (h *Handler) doDownloadISO(jobID string, name, poolName string) {
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		updateJob(jobID, 0, "error", "invalid filename")
		return
	}
	poolPath, err := h.compute.GetPoolPath(poolName)
	if err != nil {
		updateJob(jobID, 0, "error", "failed to resolve pool: "+err.Error())
		return
	}
	destPath := filepath.Join(poolPath, name)
	if rel, rerr := filepath.Rel(poolPath, destPath); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		updateJob(jobID, 0, "error", "invalid destination path")
		return
	}

	j, ok := getJob(jobID)
	if !ok || j.Status != "queued" {
		return
	}

	const maxDownloadBytes int64 = 10 << 30
	if _, err := secureDownloadWithProgress(jobID, j.URL, destPath, 30*time.Minute, maxDownloadBytes); err != nil {
		updateJob(jobID, 0, "error", err.Error())
		return
	}

	if err := h.compute.RefreshPool(poolName); err != nil {
		slog.Warn("refresh pool failed after ISO download", "pool", poolName, "err", err)
	}

	updateJob(jobID, 100, "completed", "")
}

func (h *Handler) GetDownloadJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok := getJob(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "job not found")
		return
	}
	// A job record carries the destination path, the source URL, the pool
	// and the byte counts, and the IDs are UnixNano-derived rather than
	// secret. Serving it on a bare ID lookup leaked one user's activity
	// to any other authenticated user, so ownership is enforced here.
	//
	// 404 rather than 403: a 403 would confirm that the guessed ID names a
	// real job, which is half of what the caller was fishing for.
	//
	// Jobs created before Owner existed have an empty one and stay
	// readable — a deploy must not strand a user mid-download.
	if job.Owner != "" && !h.isJobOwnerOrAdmin(r, job.Owner) {
		jsonErr(w, http.StatusNotFound, "job not found")
		return
	}
	jsonResp(w, http.StatusOK, job)
}

// ListJobs returns all active and recent async jobs owned by or visible to the user.
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	jobsMu.RLock()
	defer jobsMu.RUnlock()

	out := make([]models.DownloadJob, 0, len(isoJobs))
	for _, j := range isoJobs {
		if j.Owner != "" && !h.isJobOwnerOrAdmin(r, j.Owner) {
			continue
		}
		out = append(out, *j)
	}
	jsonResp(w, http.StatusOK, map[string]any{"jobs": out})
}

// isJobOwnerOrAdmin reports whether the request comes from the job's
// owner or from an admin (who needs to diagnose other people's failed
// transfers).
func (h *Handler) isJobOwnerOrAdmin(r *http.Request, owner string) bool {
	if r.Header.Get("X-Role") == models.RoleAdmin {
		return true
	}
	return r.Header.Get("X-User") == owner
}
