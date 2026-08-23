package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/config"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// ---- Job Tracking (thread-safe) ----
var (
	isoJobs = make(map[string]*models.DownloadJob)
	jobsMu  sync.RWMutex
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

func updateJob(id string, progress float64, status string, errMsg string) {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	if j, ok := isoJobs[id]; ok {
		j.Progress = progress
		j.Status = status
		j.Error = errMsg
	}
}

func cleanOldJobs() {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	for id, j := range isoJobs {
		if j.Status == "completed" || j.Status == "error" {
			delete(isoJobs, id)
		}
	}
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

func (h *Handler) ListPools(w http.ResponseWriter, r *http.Request) {
	pools, err := h.lv.ListStoragePools()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, pools)
}

func (h *Handler) CreatePool(w http.ResponseWriter, r *http.Request) {
	var req models.CreatePoolRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Path == "" {
		jsonErr(w, http.StatusBadRequest, "name and path are required")
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
	if req.Purpose != "" && req.Purpose != libvirt.PoolPurposeDisk && req.Purpose != libvirt.PoolPurposeISO {
		jsonErr(w, http.StatusBadRequest, "purpose must be 'disk' or 'iso'")
		return
	}
	if err := validatePoolPath(req.Path); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid path: "+err.Error())
		return
	}
	pool, err := h.lv.CreateStoragePool(r.Context(), req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "storage.pool.create", pool.Name, map[string]any{
		"type":   req.Type,
		"format": req.SourceFormat,
		"auth":   req.SourceUsername != "",
	}))
	jsonResp(w, http.StatusCreated, pool)
}

// UpdatePool handles PUT /api/storage/pools/{name}.
//
// Supported operations:
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
	// CIFS auth fields must come as a pair. Without this guard, a
	// caller could pass a new password but no username, which would
	// produce a misconfigured auth block (or no auth at all).
	hasUser := req.SourceUsername != nil
	hasPass := req.SourcePassword != nil
	if hasUser != hasPass {
		jsonErr(w, http.StatusBadRequest,
			"cifs auth requires both source_username and source_password")
		return
	}
	pool, err := h.lv.UpdateStoragePool(r.Context(), name, req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "storage.pool.update", name, map[string]any{
		"reauth": req.CifsNeedsReauth,
	}))
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

func (h *Handler) DeletePool(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		jsonErr(w, http.StatusBadRequest, "pool name required")
		return
	}
	// Guard: refuse to delete a pool whose volumes are attached to VMs.
	vols, err := h.lv.ListStorageVolumes(name)
	if err != nil && !strings.Contains(err.Error(), "not found") {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var allAtts []models.VolumeAttachment
	for _, v := range vols {
		atts, aerr := h.lv.FindVolumeAttachments(name, v.Name)
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
	if err := h.lv.DeletePool(name); err != nil {
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
	vols, err := h.lv.ListStorageVolumes(poolName)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, vols)
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
	vol, err := h.lv.CreateStorageVolume(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusCreated, vol)
}

func (h *Handler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	pool := chi.URLParam(r, "pool")
	name := chi.URLParam(r, "name")
	// Guard: refuse to delete a volume that is attached to any VM,
	// running or not. Detach the disk first.
	if atts, err := h.lv.FindVolumeAttachments(pool, name); err == nil && len(atts) > 0 {
		volumeInUse(w, fmt.Sprintf("volume %s/%s", pool, name), atts)
		return
	}
	if err := h.lv.DeleteStorageVolume(pool, name); err != nil {
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
	if err := h.lv.ResizeStorageVolume(pool, name, req.Capacity); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "resized"})
}

func (h *Handler) ListISOs(w http.ResponseWriter, r *http.Request) {
	poolName := r.URL.Query().Get("pool")
	if poolName == "" {
		poolName = config.ISOPoolName
	}
	isos, err := h.lv.GetISOs(poolName)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, isos)
}

func (h *Handler) DeleteISO(w http.ResponseWriter, r *http.Request) {
	pool := chi.URLParam(r, "pool")
	name := chi.URLParam(r, "name")
	if pool == "" {
		pool = config.ISOPoolName
	}
	// Guard: refuse to delete an ISO mounted in any VM's CD-ROM.
	if atts, err := h.lv.FindVolumeAttachments(pool, name); err == nil && len(atts) > 0 {
		volumeInUse(w, fmt.Sprintf("ISO %s/%s", pool, name), atts)
		return
	}
	if err := h.lv.DeleteISO(name, pool); err != nil {
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
	if err := h.lv.RenameISO(name, safeNew, pool); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "renamed", "name": safeNew})
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
	if filepath.Ext(name) == "" {
		name += ".iso"
	}

	poolName := r.FormValue("pool")
	if poolName == "" {
		poolName = config.ISOPoolName
	}
	poolPath, err := h.lv.GetPoolPath(poolName)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to resolve pool: "+err.Error())
		return
	}
	destPath := filepath.Join(poolPath, name)

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

	if err := h.lv.RefreshPool(poolName); err != nil {
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
	if filepath.Ext(name) == "" {
		name += ".iso"
	}

	poolName := r.URL.Query().Get("pool")
	if poolName == "" {
		poolName = config.ISOPoolName
	}
	poolPath, err := h.lv.GetPoolPath(poolName)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to resolve pool: "+err.Error())
		return
	}
	destPath := filepath.Join(poolPath, name)

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

	if err := h.lv.RefreshPool(poolName); err != nil {
		fmt.Println("Warning: refresh pool failed:", err)
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
	if !strings.HasSuffix(strings.ToLower(name), ".iso") {
		name += ".iso"
	}

	poolName := req.Pool
	if poolName == "" {
		poolName = config.ISOPoolName
	}

	jobID := fmt.Sprintf("dl_%d", time.Now().UnixNano())
	job := &models.DownloadJob{
		ID:       jobID,
		Name:     name,
		URL:      req.URL,
		Progress: 0,
		Status:   "queued",
	}
	storeJob(job)
	cleanOldJobs()

	user, role, ip := audit.FromRequest(r)
	h.audit.Log(audit.Entry{
		User: user, Role: role, IP: ip, Action: "iso.download",
		Resource: name, Detail: map[string]interface{}{"url": req.URL, "pool": poolName},
	})

	go h.doDownloadISO(jobID, name, poolName)

	jsonResp(w, http.StatusAccepted, map[string]string{"job_id": jobID, "status": "started"})
}

func (h *Handler) doDownloadISO(jobID string, name, poolName string) {
	poolPath, err := h.lv.GetPoolPath(poolName)
	if err != nil {
		updateJob(jobID, 0, "error", "failed to resolve pool: "+err.Error())
		return
	}
	destPath := filepath.Join(poolPath, name)

	j, ok := getJob(jobID)
	if !ok || j.Status != "queued" {
		return
	}

	// Custom transport with DNS-rebind-safe dialer.
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
				// Dial the resolved IP directly. Dialing the hostname again
				// would perform a second DNS lookup and re-open a DNS-rebinding
				// window after safeDownloadURL already approved this host.
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("connection to %s is not allowed", host)
		},
	}
	client := &http.Client{
		Timeout:   30 * time.Minute,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-http scheme blocked")
			}
			// Re-check the redirect target.
			if err := safeDownloadURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}

	resp, err := client.Get(j.URL)
	if err != nil {
		updateJob(jobID, 0, "error", "download failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		updateJob(jobID, 0, "error", fmt.Sprintf("HTTP %d", resp.StatusCode))
		return
	}

	// Read one byte beyond the advertised limit so an oversized response is
	// detected instead of being silently truncated and marked completed.
	const maxDownloadBytes int64 = 10 << 30
	limitReader := io.LimitReader(resp.Body, maxDownloadBytes+1)

	dst, err := os.Create(destPath)
	if err != nil {
		updateJob(jobID, 0, "error", "failed to create file: "+err.Error())
		return
	}
	defer dst.Close()

	updateJob(jobID, 0, "downloading", "")

	// Report progress during the copy so the UI bar moves instead of
	// staying at 0% until the whole file lands. A percentage is only
	// available when the server sends Content-Length; updates are
	// throttled to ~4/s to avoid hammering the job mutex.
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	var lastReport time.Time
	progWriter := &progressReportingWriter{
		w:     dst,
		total: total,
		report: func(n int64) {
			if n < total && time.Since(lastReport) < 250*time.Millisecond {
				return
			}
			lastReport = time.Now()
			pct := float64(0)
			if total > 0 {
				pct = float64(n) / float64(total) * 100
				if pct > 100 {
					pct = 100
				}
			}
			updateJob(jobID, pct, "downloading", "")
		},
	}

	written, err := io.Copy(progWriter, limitReader)
	if err != nil {
		os.Remove(destPath)
		updateJob(jobID, 0, "error", "download interrupted: "+err.Error())
		return
	}
	if written > maxDownloadBytes {
		_ = dst.Close()
		_ = os.Remove(destPath)
		updateJob(jobID, 0, "error", "download exceeds the 10 GiB limit")
		return
	}

	if err := h.lv.RefreshPool(poolName); err != nil {
		fmt.Println("Warning: refresh pool failed:", err)
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
	jsonResp(w, http.StatusOK, job)
}
