package api

// pools_init.go — deriving storage pools from an initialized disk.
//
// One disk, several purposes, one INDEPENDENT pool per purpose. A
// disk named "mydisk" with /discos, /isos and /contenedores marked
// becomes three separate pools — mydisk-vdi and mydisk-isos in
// libvirt, mydisk-containers in Incus — each rooted at its own
// subfolder. There is no unified multi-purpose pool, and a pool never
// carries more than one purpose.
//
// The folder -> nature -> suffix maps below are mirrored in the
// frontend (frontend/src/lib/purpose.js) so the init dialog can show
// the exact pool names before the request; purpose.test.js pins the
// values so divergence fails a test.

import (
	"context"
	"path/filepath"
	"strings"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// parsePoolPurpose normalizes a single pool purpose and reports
// whether it is one of the five valid ones. "lxc" is accepted as an
// alias of "container" (CLI/API compatibility) and case/spacing are
// ignored.
//
// A pool has exactly ONE purpose: a disk serving several purposes
// gets one independent pool per purpose (see
// createPoolsForSubfolders). So a comma-separated list like
// "disk,iso" — the old unified-pool syntax — matches nothing and is
// rejected here, which is the whole point of this function.
func parsePoolPurpose(raw string) (string, bool) {
	p := strings.TrimSpace(strings.ToLower(raw))
	if p == "lxc" {
		p = compute.PoolPurposeContainer
	}
	switch p {
	case compute.PoolPurposeDisk, compute.PoolPurposeISO, compute.PoolPurposeContainer,
		compute.PoolPurposeBackup, compute.PoolPurposeTemplate:
		return p, true
	}
	return "", false
}

// subfolderNature maps an init-disk subfolder to the storage-pool
// nature it carries, legacy aliases included. Unknown folders carry
// no pool.
//
// This map is also the AUTHORITY on which folders the init handler
// is allowed to create on disk (see allowedSubfolder): keeping a
// second hand-written allowlist there meant a new folder could be
// created without ever getting a pool, or the other way round.
var subfolderNature = map[string]string{
	"isos":         compute.PoolPurposeISO,
	"discos":       compute.PoolPurposeDisk,
	"disks":        compute.PoolPurposeDisk,
	"images":       compute.PoolPurposeDisk,
	"contenedores": compute.PoolPurposeContainer,
	"containers":   compute.PoolPurposeContainer,
	"backups":      compute.PoolPurposeBackup,
	"plantillas":   compute.PoolPurposeTemplate,
	"templates":    compute.PoolPurposeTemplate,
}

// allowedSubfolder reports whether the init-disk handler may create
// this folder inside the mount point. It normalizes the caller's
// value the same way subfolderPoolSpec does, so "/isos" and "isos"
// behave identically, and returns the clean name to use.
//
// Restricting creation to this fixed set is what stops the endpoint
// from becoming an arbitrary path-creation primitive.
func allowedSubfolder(sub string) (string, bool) {
	// Spaces first, then slashes, then spaces again: " /isos/ "
	// must normalize to "isos". Trimming slashes before spaces
	// (the original order) left the trailing slash in place and
	// silently dropped the folder.
	sub = strings.TrimSpace(strings.Trim(strings.TrimSpace(sub), "/"))
	_, ok := subfolderNature[sub]
	return sub, ok
}

// folderPoolSuffix maps a pool nature to the suffix appended to the
// disk's volume name, so every purpose is its own independent pool:
// mydisk + discos/contenedores/isos becomes mydisk-vdi,
// mydisk-containers and mydisk-isos.
var folderPoolSuffix = map[string]string{
	compute.PoolPurposeDisk:      "-vdi",
	compute.PoolPurposeISO:       "-isos",
	compute.PoolPurposeContainer: "-containers",
	compute.PoolPurposeBackup:    "-backups",
	compute.PoolPurposeTemplate:  "-plantillas",
}

// PoolResult is one row of the per-folder creation report returned
// by the init-disk endpoint: Status is "created", "exists" (name
// collision — safe to retry) or "error".
type PoolResult struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
	Backend string `json:"backend"`
	// Path is where the pool ended up. For a "exists" row with
	// Mismatch set, this is the PRE-EXISTING path, not the one that
	// was requested.
	Path   string `json:"path,omitempty"`
	Status string `json:"status"`
	// Mismatch flags an "exists" row whose pool points at a different
	// directory than the one this init asked for — the operator
	// reused a volume name, and disks would silently land elsewhere.
	Mismatch bool   `json:"mismatch,omitempty"`
	Error    string `json:"error,omitempty"`
}

// backendForPurpose names the storage backend that owns a pool of
// this purpose. Containers live in Incus (Combined.CreateStoragePool
// routes purpose "container" to the secondary backend); every other
// purpose is a libvirt dir pool. The returned name is reported back
// to the UI, which shows it next to each pool.
func backendForPurpose(purpose string) string {
	if purpose == compute.PoolPurposeContainer {
		return "incus"
	}
	return "libvirt"
}

// subfolderPoolSpec builds the pool request for one init-disk
// subfolder: an independent pool rooted at <mount>/<subfolder>,
// named <volume><suffix>. Unknown folders return ok == false.
func subfolderPoolSpec(sub, volumeName, mountPoint string) (models.CreatePoolRequest, string, bool) {
	sub, ok := allowedSubfolder(sub)
	if !ok || volumeName == "" || mountPoint == "" {
		return models.CreatePoolRequest{}, "", false
	}
	nature := subfolderNature[sub]
	return models.CreatePoolRequest{
		Name:    volumeName + folderPoolSuffix[nature],
		Path:    filepath.Join(mountPoint, sub),
		Type:    "dir",
		Purpose: nature,
	}, backendForPurpose(nature), true
}

// createPoolsForSubfolders registers one independent storage pool
// per init-disk subfolder that carries a nature — NEVER a unified
// multi-purpose pool. Every row is best-effort: the disk is already
// formatted and mounted, so a failed registration must not fail the
// whole init; the per-pool Status tells the caller what happened.
//
// When the caller sends NO subfolders field at all (a pre-per-purpose
// client), it falls back to a single pool at the mount root with the
// given purpose, preserving the historical behavior for those
// clients only.
//
// An explicitly EMPTY list, or a list whose folders carry no nature,
// creates nothing: the operator unchecked everything on purpose, and
// silently registering a catch-all pool at the mount root would
// recreate exactly the unified multi-purpose pool this design
// removes. Current clients always send the field.
func (h *Handler) createPoolsForSubfolders(ctx context.Context, volumeName, mountPoint string, subfolders []string, fallbackPurpose string) []PoolResult {
	results := []PoolResult{}
	seen := map[string]bool{}
	for _, sub := range subfolders {
		req, backend, ok := subfolderPoolSpec(sub, volumeName, mountPoint)
		if !ok || seen[req.Name] {
			continue
		}
		seen[req.Name] = true
		results = append(results, h.createOnePool(ctx, req, backend))
	}
	if len(results) == 0 && subfolders == nil {
		purpose := legacyFallbackPurpose(fallbackPurpose)
		results = append(results, h.createOnePool(ctx, models.CreatePoolRequest{
			Name:    volumeName,
			Path:    mountPoint,
			Type:    "dir",
			Purpose: purpose,
		}, backendForPurpose(purpose)))
	}
	return results
}

// legacyFallbackPurpose normalizes the pool_purpose field of a
// pre-per-purpose client. Anything unrecognized (including empty)
// becomes a disk pool, which is what those clients defaulted to.
func legacyFallbackPurpose(raw string) string {
	if p, ok := parsePoolPurpose(raw); ok {
		return p
	}
	return compute.PoolPurposeDisk
}

// createOnePool runs a single registration and maps the outcome to
// a PoolResult. A name collision ("already exists") is Status
// "exists", not an error: re-running init on the same disk converges
// instead of failing.
//
// A pre-existing pool of the same name may well point somewhere else
// (the operator reused a volume name across disks). That is silent
// data misrouting, so the existing path is read back and reported in
// Error when it differs — Status stays "exists", but the caller has
// what it needs to warn instead of showing a plain green row.
func (h *Handler) createOnePool(ctx context.Context, req models.CreatePoolRequest, backend string) PoolResult {
	res := PoolResult{Name: req.Name, Purpose: req.Purpose, Backend: backend, Path: req.Path}
	if _, err := h.compute.CreateStoragePool(ctx, req); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			res.Status = "exists"
			if actual, perr := h.compute.GetPoolPath(req.Name); perr == nil &&
				actual != "" && filepath.Clean(actual) != filepath.Clean(req.Path) {
				res.Path = actual
				res.Mismatch = true
				res.Error = "pool already exists at " + actual + " (expected " + req.Path + ")"
			}
			return res
		}
		res.Status = "error"
		res.Error = err.Error()
		return res
	}
	res.Status = "created"
	return res
}
