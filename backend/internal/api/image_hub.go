package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/appliances"
	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// BaseCloudImageItem describes an official cloud base image in the local pool.
type BaseCloudImageItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Format      string `json:"format"`
	SizeBytes   int64  `json:"size_bytes"`
	IsCached    bool   `json:"is_cached"`
	LocalPath   string `json:"local_path,omitempty"`
	LocalBytes  int64  `json:"local_bytes,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	// Pool names the disk pool holding the cached copy. Empty when the
	// image is not cached, or when it still sits in the legacy
	// DataDir/base-images folder from before pools were selectable.
	Pool string `json:"pool,omitempty"`
}

// baseImagesDir is the legacy cache location: a folder under DataDir
// that no pool knows about, so anything landing there was invisible to
// the volume browser, to quota accounting and to the move feature.
//
// It is still read when listing (so images pulled by an older build are
// not orphaned) but nothing new is written into it — see
// baseImageTargets/resolveBaseImagePool below.
func (h *Handler) baseImagesDir() string {
	dir := filepath.Join(h.cfg.DataDir, "base-images")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// baseImageName is the on-disk file name for a cached base image. It is
// deliberately prefixed: a base image shares a pool with ordinary VM
// disks now, and "debian-13.qcow2" is a name an operator could
// plausibly have given one of their own volumes.
func baseImageName(id string) string {
	return "base-" + id + ".qcow2"
}

// isBaseImageFilename reports whether a volume name is a cached base
// cloud image, i.e. one of the files baseImageName produces. The move
// endpoint uses it to widen the allowed destinations to template
// pools without also letting an ordinary VM disk be filed there.
func isBaseImageFilename(name string) bool {
	return strings.HasPrefix(name, "base-") && strings.HasSuffix(name, ".qcow2")
}

// baseImagePoolPurpose reports whether a pool may hold a cached base
// cloud image.
//
// Both "disk" and "template" qualify, and that is the whole point of
// the template purpose: a base image is a golden image, not a disk in
// daily use, so an operator who dedicates a pool to them should be
// able to park the images there. Before this, "template" pools were
// listed in the storage grid but were not a valid destination for the
// one kind of file they exist for.
func baseImagePoolPurpose(purpose string) bool {
	return compute.HasPurpose(purpose, compute.PoolPurposeDisk) ||
		compute.HasPurpose(purpose, compute.PoolPurposeTemplate)
}

// baseImagePools lists the pools a base image may live in, most
// preferred first. The default disk pool leads so an operator who never
// picks one keeps getting the behaviour they had.
func (h *Handler) baseImagePools() []models.StoragePool {
	if h.compute == nil {
		return nil
	}
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		return nil
	}
	def := h.defaultPool()
	var out []models.StoragePool
	for _, p := range pools {
		if !baseImagePoolPurpose(p.Purpose) {
			continue
		}
		// An inactive pool has no mounted directory behind it. libvirt
		// still reports a path, so writing there would silently fill
		// the root filesystem under an unmounted mount point instead
		// of the disk the operator believes they chose.
		//
		// An empty state means the backend does not report one, not
		// that the pool is down, so it is left alone.
		if p.State != "" && !poolStateUsable(p.State) {
			continue
		}
		if p.Name == def {
			out = append([]models.StoragePool{p}, out...)
			continue
		}
		out = append(out, p)
	}
	return out
}

// baseImageLocation is one copy of a cached base image. There is
// normally exactly one, but nothing on disk enforces that — see
// findAllCachedBaseImages.
type baseImageLocation struct {
	Path string
	Pool string // empty for the legacy DataDir/base-images folder
}

// findAllCachedBaseImages returns EVERY copy of a base image, most
// preferred pool first, plus the legacy folder.
//
// Several copies is a real state, not a theoretical one: the download
// endpoint writes into the pool it was asked for without checking the
// others, so pulling an image a second time into a different pool
// leaves two identical files on two different disks. Only the first
// was ever visible, so the second was invisible wasted space that no
// screen listed and no delete could reach.
//
// Scanning rather than trusting a recorded location is deliberate: the
// file can be moved between pools by the storage move feature, and a
// stale pointer would report a perfectly good image as missing.
func (h *Handler) findAllCachedBaseImages(id string) []baseImageLocation {
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return nil
	}
	name := baseImageName(id)
	var out []baseImageLocation
	for _, p := range h.baseImagePools() {
		dir, err := h.compute.GetPoolPath(p.Name)
		if err != nil {
			continue
		}
		cand := filepath.Join(dir, name)
		if rel, rerr := filepath.Rel(dir, cand); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			continue
		}
		if st, err := os.Stat(cand); err == nil && !st.IsDir() && st.Size() > 1024*1024 {
			out = append(out, baseImageLocation{Path: cand, Pool: p.Name})
		}
	}
	// Legacy folder, written by builds before the pool was selectable.
	legacy := filepath.Join(h.baseImagesDir(), id+".qcow2")
	if rel, rerr := filepath.Rel(h.baseImagesDir(), legacy); rerr == nil && !strings.HasPrefix(rel, "..") && rel != ".." {
		if st, err := os.Stat(legacy); err == nil && !st.IsDir() && st.Size() > 1024*1024 {
			out = append(out, baseImageLocation{Path: legacy})
		}
	}
	return out
}

// findCachedBaseImage locates a cached base image across every eligible
// pool plus the legacy folder, returning its path, the pool holding it
// (empty for the legacy folder) and whether it was found at all.
//
// When several copies exist this reports the most preferred one, which
// is the copy every other screen shows.
func (h *Handler) findCachedBaseImage(id string) (path, pool string, found bool) {
	locs := h.findAllCachedBaseImages(id)
	if len(locs) == 0 {
		return "", "", false
	}
	return locs[0].Path, locs[0].Pool, true
}

// resolveBaseImagePool validates the caller's pool choice and returns
// the pool name and its filesystem path. An empty request falls back to
// the first eligible pool, which keeps the endpoint usable by clients
// that predate the pool selector.
func (h *Handler) resolveBaseImagePool(requested string) (name, dir string, err error) {
	requested = strings.TrimSpace(requested)
	pools := h.baseImagePools()
	if len(pools) == 0 {
		return "", "", fmt.Errorf("no storage pool is designated for disk images")
	}
	if requested == "" {
		requested = pools[0].Name
	}
	for _, p := range pools {
		if p.Name != requested {
			continue
		}
		dir, derr := h.compute.GetPoolPath(p.Name)
		if derr != nil {
			return "", "", fmt.Errorf("pool %q: %w", p.Name, derr)
		}
		return p.Name, dir, nil
	}
	return "", "", fmt.Errorf("pool %q is not designated for disk images", requested)
}

// ListContainerImages handles GET /api/images/containers.
func (h *Handler) ListContainerImages(w http.ResponseWriter, r *http.Request) {
	if h.compute == nil {
		jsonResp(w, http.StatusOK, []models.IncusImageItem{})
		return
	}
	images, err := h.compute.ListIncusImages()
	if err != nil {
		jsonResp(w, http.StatusOK, []models.IncusImageItem{})
		return
	}
	jsonResp(w, http.StatusOK, images)
}

// PullContainerImage handles POST /api/images/containers/pull.
func (h *Handler) PullContainerImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ref string `json:"ref"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		jsonErr(w, http.StatusBadRequest, "image reference is required (e.g. images:ubuntu/26.04)")
		return
	}

	// Trigger image caching via Incus domain create test or pull
	jobID := fmt.Sprintf("img_pull_%d", time.Now().UnixNano())
	job := &models.DownloadJob{
		ID:       jobID,
		Name:     "Pull " + ref,
		URL:      ref,
		Owner:    jobOwner(r),
		Progress: 10,
		Status:   "downloading",
	}
	storeJob(job)

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				updateJob(jobID, 99, "error", fmt.Sprintf("panic in image pull: %v", rec))
			}
		}()

		updateJob(jobID, 30, "downloading", "Downloading image metadata and layers from image server...")

		// Copy the image straight into the local Incus image store. This
		// talks only to the remote image server + the local image API —
		// no instance is created, so it needs no network/bridge at all
		// (unlike the old approach of spinning up a throwaway container).
		if err := h.compute.PullIncusImage(ref); err != nil {
			updateJob(jobID, 99, "error", "Failed to cache image: "+err.Error())
			return
		}
		updateJob(jobID, 100, "completed", "Image successfully downloaded and cached in local pool.")
	}()

	if h.audit != nil {
		h.audit.Log(auditFor(r, "image.container.pull", ref, nil))
	}
	jsonResp(w, http.StatusAccepted, map[string]string{
		"job_id": jobID,
		"ref":    ref,
		"status": "queued",
	})
}

// DeleteContainerImage handles DELETE /api/images/containers/{fingerprint}.
func (h *Handler) DeleteContainerImage(w http.ResponseWriter, r *http.Request) {
	fp := chi.URLParam(r, "fingerprint")
	if fp == "" {
		jsonErr(w, http.StatusBadRequest, "fingerprint is required")
		return
	}
	// Try native compute backend first
	var delErr error
	if h.compute != nil {
		delErr = h.compute.DeleteIncusImage(fp)
	}
	if delErr != nil {
		// Best-effort execution via Incus CLI or API as fallback
		cmd := exec.Command("incus", "image", "delete", fp)
		if out, err := cmd.CombinedOutput(); err != nil {
			cmd2 := exec.Command("lxc", "image", "delete", fp)
			if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
				jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete image: %v / %s / %s", delErr, string(out), string(out2)))
				return
			}
		}
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "image.container.delete", fp, nil))
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted", "fingerprint": fp})
}

// ListBaseCloudImages handles GET /api/images/cloud-base.
func (h *Handler) ListBaseCloudImages(w http.ResponseWriter, r *http.Request) {
	var result []BaseCloudImageItem

	for _, app := range appliances.Defaults {
		if app.Category != "cloud" && app.Category != "nas" && app.Category != "router" {
			continue
		}
		item := BaseCloudImageItem{
			ID:          app.ID,
			Name:        app.Name,
			Description: app.Description,
			URL:         app.URL,
			Format:      app.Format,
			SizeBytes:   app.SizeBytes,
		}
		if path, pool, ok := h.findCachedBaseImage(app.ID); ok {
			item.IsCached = true
			item.LocalPath = path
			item.Pool = pool
			if st, err := os.Stat(path); err == nil {
				item.LocalBytes = st.Size()
				item.UpdatedAt = st.ModTime().UTC().Format(time.RFC3339)
			}
		}
		result = append(result, item)
	}

	jsonResp(w, http.StatusOK, result)
}

// PullBaseCloudImage handles POST /api/images/cloud-base/pull.
func (h *Handler) PullBaseCloudImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
		// Pool is the disk pool to download into. Optional: an empty
		// value takes the default disk pool, which is what clients
		// that predate this field send.
		Pool string `json:"pool"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	id := strings.TrimSpace(req.ID)
	app, ok := h.appStore.Get(id)
	if !ok {
		// Try finding in Defaults
		found := false
		for _, d := range appliances.Defaults {
			if d.ID == id {
				app = d
				found = true
				break
			}
		}
		if !found {
			jsonErr(w, http.StatusNotFound, "unknown cloud image ID: "+id)
			return
		}
	}

	// The pool is resolved synchronously: a bad pool name is something
	// the operator can fix right now, and reporting it through a job
	// they have to go and poll would be needless friction.
	poolName, poolDir, err := h.resolveBaseImagePool(req.Pool)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// An image already on disk must not be downloaded a second time
	// into a different pool. The download wrote wherever it was told
	// without looking at the other pools, so a second pull left two
	// identical multi-hundred-MB files on two disks — and only the
	// first was ever listed, making the second invisible wasted space
	// that no screen showed and no delete reached.
	//
	// Relocating is the operation the operator actually wants here, and
	// it already exists: the move endpoint copies once and removes the
	// source. Saying so is more useful than silently duplicating.
	//
	// Every location is checked for the requested pool first: a copy in
	// the target pool means "already cached", even when some other pool
	// is listed before it and happens to hold a (stray) second copy.
	if cached := h.findAllCachedBaseImages(id); len(cached) > 0 {
		for _, loc := range cached {
			if loc.Pool == poolName {
				jsonResp(w, http.StatusOK, map[string]any{
					"id": id, "status": "cached", "pool": poolName,
				})
				return
			}
		}
		where := cached[0].Pool
		if where == "" {
			where = cached[0].Path
		}
		jsonErr(w, http.StatusConflict, fmt.Sprintf(
			"%s is already cached in pool %q — move it instead of downloading a second copy",
			app.Name, where))
		return
	}

	jobID := fmt.Sprintf("base_img_%d", time.Now().UnixNano())
	job := &models.DownloadJob{
		ID:       jobID,
		Name:     "Download " + app.Name,
		URL:      app.URL,
		Owner:    jobOwner(r),
		Pool:     poolName,
		Progress: 0,
		Status:   "queued",
	}
	storeJob(job)

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				updateJob(jobID, 99, "error", fmt.Sprintf("panic in base image download: %v", rec))
			}
		}()

		updateJob(jobID, 5, "downloading", "Starting download of "+app.Name+" from official mirror...")
		destPath := filepath.Join(poolDir, baseImageName(app.ID))
		tmpDest := destPath + ".tmp"
		_ = os.Remove(tmpDest)

		// Uses the shared DNS-rebind-safe helper: this path previously went
		// straight through http.DefaultClient, bypassing every SSRF guard the
		// other download call sites enforce.
		const maxBaseImageBytes int64 = 10 << 30
		if _, err := secureDownloadWithProgress(jobID, app.URL, tmpDest, 30*time.Minute, maxBaseImageBytes); err != nil {
			updateJob(jobID, 99, "error", "Download error: "+err.Error())
			return
		}

		updateJob(jobID, 95, "downloading", "Finalizing and converting image...")

		// Handle decompression if needed
		compression := app.Compression
		if compression == "" || compression == "none" {
			switch {
			case strings.HasSuffix(app.URL, ".xz"):
				compression = "xz"
			case strings.HasSuffix(app.URL, ".gz"):
				compression = "gz"
			case strings.HasSuffix(app.URL, ".bz2"):
				compression = "bz2"
			}
		}

		if compression == "xz" || compression == "gz" || compression == "bz2" {
			decompressed := tmpDest + ".decomp"
			var derr error
			switch compression {
			case "xz":
				derr = xzFile(tmpDest, decompressed)
			case "gz":
				derr = gunzipFile(tmpDest, decompressed)
			case "bz2":
				derr = bz2File(tmpDest, decompressed)
			}
			if derr != nil {
				_ = os.Remove(tmpDest)
				_ = os.Remove(decompressed)
				updateJob(jobID, 99, "error", fmt.Sprintf("decompress %s failed: %v", compression, derr))
				return
			}
			_ = os.Remove(tmpDest)
			tmpDest = decompressed
		}

		// Ensure proper qcow2 format
		qcow2Dest := tmpDest + ".qcow2"
		if convertToQcow2(tmpDest, qcow2Dest) {
			_ = os.Remove(tmpDest)
			tmpDest = qcow2Dest
		}

		if err := os.Rename(tmpDest, destPath); err != nil {
			updateJob(jobID, 99, "error", "Failed to finalize file: "+err.Error())
			return
		}

		// The image is a pool volume now, so the pool has to be told or
		// it stays absent from the volume list until something else
		// happens to refresh it.
		if err := h.compute.RefreshPool(poolName); err != nil {
			slog.Warn("base_image_pool_refresh_failed", "pool", poolName, "err", err)
		}

		updateJob(jobID, 100, "completed", app.Name+" is now cached in pool "+poolName+".")
	}()

	if h.audit != nil {
		h.audit.Log(auditFor(r, "image.cloud_base.pull", id, nil))
	}
	jsonResp(w, http.StatusAccepted, map[string]string{
		"job_id": jobID,
		"id":     id,
		"status": "queued",
	})
}

// DeleteBaseCloudImage handles DELETE /api/images/cloud-base/{id}.
func (h *Handler) DeleteBaseCloudImage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// The id comes from the URL, and the file name is built from it, so
	// it must not be able to name a path of its own.
	if id == "" || id != filepath.Base(id) || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		jsonErr(w, http.StatusBadRequest, "invalid image id")
		return
	}

	// Look the image up rather than assuming the legacy folder: it may
	// have been downloaded into any pool, or moved between pools
	// afterwards.
	//
	// Every copy is deleted, not just the first. Installs that predate
	// the duplicate guard in PullBaseCloudImage can hold the same image
	// in several pools, and deleting only the visible one would report
	// success while leaving the rest behind as space the UI no longer
	// lists.
	locs := h.findAllCachedBaseImages(id)
	if len(locs) == 0 {
		// Nothing to delete is the state the caller wanted; saying so
		// with a 404 would make a retry after a failure look broken.
		jsonResp(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
		return
	}
	// A cached base image is precisely the kind of file other disks get
	// layered on: the whole point of caching it is instant
	// copy-on-write clones. Removing it would leave every one of them
	// unopenable.
	//
	// Checked for every copy BEFORE deleting any of them: a dependent
	// on the second copy must not be discovered after the first has
	// already been unlinked.
	//
	// An image still in the legacy folder has no pool to look it up by,
	// so it is checked by path instead.
	for _, loc := range locs {
		if loc.Pool != "" {
			if h.assertNoBackingDependents(w, "base image "+id, loc.Pool, baseImageName(id)) {
				return
			}
		} else if h.assertNoBackingDependentsByPath(w, "base image "+id, loc.Path) {
			return
		}
	}
	pools := make([]string, 0, len(locs))
	for _, loc := range locs {
		if err := os.Remove(loc.Path); err != nil && !os.IsNotExist(err) {
			jsonErr(w, http.StatusInternalServerError, "failed to delete image: "+err.Error())
			return
		}
		if loc.Pool != "" {
			pools = append(pools, loc.Pool)
			if err := h.compute.RefreshPool(loc.Pool); err != nil {
				slog.Warn("base_image_pool_refresh_failed", "pool", loc.Pool, "err", err)
			}
		}
	}
	poolName := strings.Join(pools, ", ")
	if h.audit != nil {
		h.audit.Log(auditFor(r, "image.cloud_base.delete", id, map[string]any{
			"pools": pools, "copies": len(locs),
		}))
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted", "id": id, "pool": poolName})
}
