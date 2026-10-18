package api

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
	"webkvm/internal/safego"
	"webkvm/internal/vzdump"
)

// ExportVM streams a backup of the VM to the client. The format
// depends on query parameters:
//
//	?format=ova&target=vmware  -> .ova with VMDK (VirtualBox/VMware compatible)
//	?format=ova&target=libvirt -> .ova with qcow2 (Proxmox/libvirt/GNOME Boxes)
//	?format=backup (default)   -> WebKVM backup (zstd compressed by default)
//	?compress=1                -> (backup only) re-pack disks with qemu-img -c
//	?legacy=gzip               -> (backup only) use gzip instead of zstd
func (h *Handler) ExportVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	if vm.State == models.VMStateRunning && vm.Hypervisor != "incus" {
		jsonErr(w, http.StatusConflict, "VM must be shut off before exporting")
		return
	}

	// Pre-flight: verify we can read every disk file before we
	// commit to a streaming response.
	if vm.Hypervisor != "incus" {
		if err := h.compute.ValidateDomainDisks(id); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	format := strings.ToLower(r.URL.Query().Get("format"))
	target := strings.ToLower(r.URL.Query().Get("target"))
	if format == "" {
		format = "backup"
	}

	switch format {
	case "ova":
		h.exportOVA(w, r, id, target)
	case "backup":
		h.exportBackup(w, r, id)
	case "proxmox":
		// Proxmox vzdump LXC format — only valid for Incus containers.
		if vm.Hypervisor != "incus" {
			jsonErr(w, http.StatusBadRequest, "format=proxmox is only available for Incus containers")
			return
		}
		h.exportProxmox(w, r, id, vm.Name)
	default:
		jsonErr(w, http.StatusBadRequest, "format must be 'ova', 'backup', or 'proxmox'")
	}
}

func (h *Handler) exportBackup(w http.ResponseWriter, r *http.Request, id string) {
	repack := r.URL.Query().Get("compress") == "1"
	compress := strings.ToLower(r.URL.Query().Get("legacy"))
	if compress == "" {
		compress = "zstd"
	}
	zstdLevel := 19
	if v := r.URL.Query().Get("zstdlevel"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 22 {
			zstdLevel = n
		}
	}

	ext := "tar.zst"
	contentType := "application/zstd"
	if compress == "gzip" {
		ext = "tar.gz"
		contentType = "application/gzip"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, id, ext))
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	opts := compute.ExportBackupOptions{
		Compress:    compress,
		ZstdLevel:   zstdLevel,
		RepackDisks: repack,
	}
	h.streamLibvirtWrite(w, r, func(pw io.Writer) error {
		_, err := h.compute.ExportDomain(r.Context(), id, opts, pw)
		return err
	})
}

func (h *Handler) exportProxmox(w http.ResponseWriter, r *http.Request, id, name string) {
	zstdLevel := 19
	if v := r.URL.Query().Get("zstdlevel"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 22 {
			zstdLevel = n
		}
	}

	filename := vzdump.VzdumpFilename(name, time.Now())

	pr, pw2 := io.Pipe()
	var exportErr error
	go func() {
		opts := compute.ExportBackupOptions{
			Compress:  "",
			ZstdLevel: 0,
		}
		_, exportErr = h.compute.ExportDomain(r.Context(), id, opts, pw2)
		pw2.CloseWithError(exportErr)
	}()

	tmpOut, tmpErr := os.CreateTemp("", "webkvm-export-*.tar.zst")
	if tmpErr != nil {
		jsonErr(w, http.StatusInternalServerError, "create tmpfile: "+tmpErr.Error())
		return
	}
	tmpPath := tmpOut.Name()
	defer os.Remove(tmpPath)
	defer tmpOut.Close()

	if err := vzdump.ExportToProxmox(pr, tmpOut, zstdLevel); err != nil {
		slog.Error("exportProxmox: convert failed", "id", id, "err", err)
		jsonErr(w, http.StatusInternalServerError, "export failed: "+err.Error())
		return
	}

	if err := tmpOut.Sync(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "sync tmpfile: "+err.Error())
		return
	}
	tmpOut.Close()

	tmpInfo, err := os.Stat(tmpPath)
	if err != nil {
		slog.Error("exportProxmox: stat tmpfile failed", "id", id, "err", err)
		jsonErr(w, http.StatusInternalServerError, "stat tmpfile: "+err.Error())
		return
	}
	srcF, err := os.Open(tmpPath)
	if err != nil {
		slog.Error("exportProxmox: open tmpfile failed", "id", id, "err", err)
		jsonErr(w, http.StatusInternalServerError, "open tmpfile: "+err.Error())
		return
	}
	defer srcF.Close()

	w.Header().Set("Content-Type", "application/zstd")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", strconv.FormatInt(tmpInfo.Size(), 10))
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, srcF); err != nil {
		slog.Warn("exportProxmox: stream to client failed", "id", id, "err", err)
	}
}

func (h *Handler) exportOVA(w http.ResponseWriter, r *http.Request, id, target string) {
	var ovaTarget compute.OVATarget
	switch target {
	case "vmware", "":
		ovaTarget = compute.OVATargetVMware
	case "libvirt":
		ovaTarget = compute.OVATargetLibvirt
	default:
		jsonErr(w, http.StatusBadRequest, "target must be 'vmware' or 'libvirt'")
		return
	}

	zstdLevel := 19
	if v := r.URL.Query().Get("zstdlevel"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 22 {
			zstdLevel = n
		}
	}

	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s.ova"`, id, ovaTarget))
	w.Header().Set("X-Export-Target", string(ovaTarget))
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	opts := compute.OVAOptions{
		Target:    ovaTarget,
		Compress:  compute.OVACompressZstd,
		ZstdLevel: zstdLevel,
	}
	h.streamLibvirtWrite(w, r, func(pw io.Writer) error {
		return h.compute.ExportDomainOVA(r.Context(), id, opts, pw)
	})
}

func (h *Handler) streamLibvirtWrite(w http.ResponseWriter, r *http.Request, producer func(io.Writer) error) {
	flusher, _ := w.(http.Flusher)
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer safego.Recover("vm_stream")
		defer close(errCh)
		defer pw.Close()
		if err := producer(pw); err != nil {
			_ = pw.CloseWithError(err)
			errCh <- err
			return
		}
		errCh <- nil
	}()

	buf := make([]byte, 64*1024)
	for {
		n, rerr := pr.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				_ = pr.CloseWithError(werr)
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				slog.Warn("export_stream_truncated", "err", rerr)
			}
			break
		}
	}
	<-errCh
}

func (h *Handler) ImportVM(w http.ResponseWriter, r *http.Request) {
	h.importArchive(w, r, false)
}

func (h *Handler) ImportOVA(w http.ResponseWriter, r *http.Request) {
	h.importArchive(w, r, true)
}

func (h *Handler) importArchive(w http.ResponseWriter, r *http.Request, requireOVA bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 100<<30)       // 100GB upload cap
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB in-memory buffer before disk spill
		jsonErr(w, http.StatusBadRequest, "invalid multipart: "+err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "file field required")
		return
	}
	defer file.Close()

	newName := strings.TrimSpace(r.FormValue("name"))
	if newName != "" {
		if err := validateVMName(newName); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	pool := strings.TrimSpace(r.FormValue("pool"))
	if pool == "" {
		pool = config.DiskPoolName
	}
	if err := h.assertPoolPurposeAny(pool,
		compute.PoolPurposeDisk, compute.PoolPurposeContainer); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	network := strings.TrimSpace(r.FormValue("network"))
	if network != "" {
		if err := validateVMName(network); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid network: "+err.Error())
			return
		}
	}
	importOpts := compute.ImportOpts{
		Admin:   h.mediaScope(r).Admin,
		Network: network,
	}
	if v := r.FormValue("vcpus"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			importOpts.VCPUs = n
		}
	}
	if v := r.FormValue("ram_mb"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			importOpts.RAMMB = n
		}
	}
	if v := r.FormValue("autostart"); v != "" {
		b := v == "true" || v == "1"
		importOpts.Autostart = &b
	}

	if owner, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		vcpus := importOpts.VCPUs
		if vcpus == 0 {
			vcpus = 2
		}
		ram := importOpts.RAMMB
		if ram == 0 {
			ram = 2048
		}
		diskGB := bytesToGB(hdr.Size)
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := assertPoolAllowed(u, pool); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if err := h.checkQuota(owner, 1, int64(vcpus), int64(ram), diskGB); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		if err := h.checkDiskQuota(owner, map[string]int64{pool: diskGB}); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
	}

	h.audit.Log(auditFor(r, "vm.import", "pending", map[string]interface{}{
		"action":     "start",
		"filename":   hdr.Filename,
		"size_bytes": hdr.Size,
		"pool":       pool,
		"name":       newName,
		"ova":        requireOVA,
	}))

	if strings.Contains(pool, "..") {
		jsonErr(w, http.StatusBadRequest, "invalid pool: traversal not allowed")
		return
	}
	poolPath, err := h.compute.GetPoolPath(pool)
	if err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    "resolve pool path: " + err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, "resolve pool path: "+err.Error())
		return
	}
	if strings.Contains(poolPath, "..") {
		jsonErr(w, http.StatusBadRequest, "invalid pool path: traversal not allowed")
		return
	}

	tmp, err := os.CreateTemp(poolPath, "webkvm-import-*.tmp")
	if err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    "create temp file: " + err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, "create temp file in pool: "+err.Error())
		return
	}
	tmpPath := tmp.Name()
	defer tmp.Close()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, file); err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    "save upload: " + err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, "save upload: "+err.Error())
		return
	}
	if err := tmp.Sync(); err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    "sync upload: " + err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    "close upload: " + err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	isLXCImport := false
	if !requireOVA {
		if ok, _ := vzdump.IsVzdumpLXC(tmpPath); ok {
			isLXCImport = true
			ctPool := h.containerPoolName(pool)
			if owner, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
				if ctPool == "" {
					jsonErr(w, http.StatusServiceUnavailable, "cannot resolve a default container storage pool; specify one explicitly")
					return
				}
				u, uerr := h.userStore.Get(owner)
				if uerr != nil {
					jsonErr(w, http.StatusUnauthorized, "user not found")
					return
				}
				if err := assertPoolAllowed(u, ctPool); err != nil {
					jsonErr(w, http.StatusForbidden, err.Error())
					return
				}
				if err := h.checkDiskQuota(owner, map[string]int64{ctPool: bytesToGB(hdr.Size)}); err != nil {
					jsonErr(w, http.StatusConflict, err.Error())
					return
				}
			}
			if ctPool != "" {
				pool = ctPool
			}
		}
	}

	uuid, resolvedName, warnings, format, err := h.importLocalArchive(tmpPath, hdr.Filename, hdr.Size, newName, pool, requireOVA, importOpts)
	if err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    err.Error(),
		}))
		if errors.Is(err, compute.ErrForbidden) {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	owner, _, _ := audit.FromRequest(r)
	if _, merr := h.compute.UpdateVMMeta(uuid, importedMetaUpdate(owner)); merr != nil {
		slog.Warn("import_meta_reset_failed", "id", uuid, "err", merr)
	}
	if owner != "" {
		if isLXCImport {
			if rerr := h.checkDiskQuota(owner, nil); rerr != nil {
				_ = h.compute.DeleteDomain(uuid)
				h.audit.Log(auditFor(r, "vm.import_quota_rollback", uuid, map[string]interface{}{
					"name": resolvedName, "pool": pool, "error": rerr.Error(),
				}))
				jsonErr(w, http.StatusConflict, "Cuota excedida tras la importación del disco. Recursos limpiados por seguridad.")
				return
			}
		} else if imported, gerr := h.compute.GetDomain(uuid); gerr == nil && len(imported.Disks) > 0 {
			vol := filepath.Base(imported.Disks[0].Source)
			if rerr := h.recheckDiskQuota(owner, pool, vol); rerr != nil {
				_ = h.compute.DeleteDomain(uuid)
				if imported.Name != "" {
					var diskNames []string
					for _, d := range imported.Disks {
						if d.Source != "" {
							diskNames = append(diskNames, filepath.Base(d.Source))
						}
					}
					_, _, _ = h.compute.DeleteVMDiskFiles(imported.Name, diskNames...)
				}
				h.audit.Log(auditFor(r, "vm.import_quota_rollback", uuid, map[string]interface{}{
					"name": imported.Name, "volume": vol, "error": rerr.Error(),
				}))
				jsonErr(w, http.StatusConflict, "Cuota excedida tras la importación del disco. Recursos limpiados por seguridad.")
				return
			}
		}
	}

	h.audit.Log(auditFor(r, "vm.import", uuid, map[string]interface{}{
		"action":     "done",
		"filename":   hdr.Filename,
		"name":       resolvedName,
		"size_bytes": hdr.Size,
		"warnings":   len(warnings),
	}))
	resp := map[string]any{
		"status":         "imported",
		"id":             uuid,
		"name":           resolvedName,
		"requested_name": newName,
		"filename":       hdr.Filename,
		"format":         format,
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	jsonResp(w, http.StatusCreated, resp)
}

func (h *Handler) importLocalArchive(sourcePath, sourceFilename string, sourceSize int64, newName, pool string, requireOVA bool, opts compute.ImportOpts) (uuid, resolvedName string, warnings []string, format string, err error) {
	f, err := os.Open(sourcePath)
	if err != nil {
		return "", "", nil, "", fmt.Errorf("open source: %w", err)
	}
	defer f.Close()

	br := bufio.NewReader(f)
	head, _ := br.Peek(4)

	isGzip := len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b
	isZstd := len(head) >= 4 && head[0] == 0x28 && head[1] == 0xb5 && head[2] == 0x2f && head[3] == 0xfd

	isOVA := requireOVA
	if isOVA {
		format = ".ova"
	} else {
		switch {
		case isGzip:
			format = ".tar.gz"
		case isZstd:
			format = ".tar.zst"
		default:
			format = ".bin"
		}
	}

	isVzdumpLXC := false
	if !isOVA {
		if ok, _ := vzdump.IsVzdumpLXC(sourcePath); ok {
			isVzdumpLXC = true
			format = ".tar.zst"
		}
	}

	switch {
	case isOVA:
		uuid, resolvedName, err = h.compute.ImportOVA(sourcePath, newName, pool)

	case isVzdumpLXC:
		convDest, cerr := os.CreateTemp("", "webkvm-vzdump-conv-*.tar")
		if cerr != nil {
			return "", "", nil, format, fmt.Errorf("vzdump conv tmpfile: %w", cerr)
		}
		convPath := convDest.Name()
		convDest.Close()
		defer os.Remove(convPath)

		ctPool := h.containerPoolName(pool)
		if cerr := vzdump.ImportFromProxmox(sourcePath, convPath, newName, ctPool); cerr != nil {
			return "", "", nil, format, fmt.Errorf("vzdump convert: %w", cerr)
		}
		opts.GeneratedConfig = true
		uuid, resolvedName, warnings, err = h.compute.ImportDomain(convPath, newName, ctPool, opts)

	default:
		uuid, resolvedName, warnings, err = h.compute.ImportDomain(sourcePath, newName, pool, opts)
	}
	if err != nil {
		return "", "", nil, format, err
	}
	_ = sourceFilename
	_ = sourceSize
	return uuid, resolvedName, warnings, format, nil
}

func importedMetaUpdate(owner string) models.VMMetaUpdate {
	notShared := false
	upd := models.VMMetaUpdate{Shared: &notShared}
	if owner != "" {
		upd.OwnerID = &owner
	}
	return upd
}
