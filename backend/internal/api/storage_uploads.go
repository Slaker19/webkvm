package api

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/config"
	"webkvm/internal/imgsafe"
	"webkvm/internal/models"
)

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

	dst, err := createUploadFile(destPath)
	if err != nil {
		jsonErr(w, uploadCreateStatus(err), "failed to create file: "+err.Error())
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

func convertToQcow2(src, dst string) bool {
	if !imgsafe.Available() {
		return false
	}
	declared := imgsafe.FormatFromExt(src)
	if declared == "" {
		return false
	}
	if err := imgsafe.Convert(context.Background(), src, dst, declared, 2*time.Hour); err != nil {
		slog.Warn("qemu-img convert failed", "src", src, "dst", dst, "err", err)
		return false
	}
	return true
}

func validateUploadedImage(path string) error {
	declared := imgsafe.FormatFromExt(path)
	if declared == "" {
		return fmt.Errorf("unsupported disk image extension")
	}
	if !imgsafe.Available() {
		if declared == "raw" {
			return nil
		}
		return fmt.Errorf("qemu-img is required to validate %s images", declared)
	}
	_, err := imgsafe.Inspect(context.Background(), path, declared)
	return err
}

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
		// 0x00 is the legacy regular-file type (tar.TypeRegA, deprecated in
		// Go 1.11); accept it alongside tar.TypeReg like tar(1) does.
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 {
			continue
		}
		cleanName := filepath.Clean(hdr.Name)
		if strings.Contains(cleanName, "..") || filepath.IsAbs(cleanName) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(cleanName))
		if ext == ".vmdk" || ext == ".raw" || ext == ".img" || ext == ".qcow2" {
			extractedDiskPath = filepath.Join(workDir, filepath.Base(cleanName))
			outFile, err := os.OpenFile(extractedDiskPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return "", "", 0, err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return "", "", 0, err
			}
			outFile.Close()
			break
		}
	}

	if extractedDiskPath == "" {
		return "", "", 0, fmt.Errorf("no valid disk image found in OVA archive")
	}

	if err := validateUploadedImage(extractedDiskPath); err != nil {
		return "", "", 0, fmt.Errorf("extracted disk rejected: %w", err)
	}

	if convertToQcow2(extractedDiskPath, qcow2Path) {
		fi, err := os.Stat(qcow2Path)
		if err != nil {
			return "", "", 0, err
		}
		return qcow2Path, qcow2Name, fi.Size(), nil
	}

	return "", "", 0, fmt.Errorf("failed to convert extracted disk to qcow2")
}

func (h *Handler) UploadDisk(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 500<<30)

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

		dst, derr := createUploadFile(destPath)
		if derr != nil {
			jsonErr(w, uploadCreateStatus(derr), "failed to create file: "+derr.Error())
			return
		}
		written, err = io.Copy(dst, part)
		dst.Close()
		if err != nil {
			os.Remove(destPath)
			jsonErr(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
			return
		}
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

	if strings.ToLower(filepath.Ext(name)) != ".ova" {
		if verr := validateUploadedImage(destPath); verr != nil {
			_ = os.Remove(destPath)
			jsonErr(w, http.StatusBadRequest, "uploaded image rejected: "+verr.Error())
			return
		}
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

	dst, err := createUploadFile(destPath)
	if err != nil {
		jsonErr(w, uploadCreateStatus(err), "failed to create file: "+err.Error())
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
