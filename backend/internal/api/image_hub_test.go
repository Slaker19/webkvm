package api

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

// imagePoolBackend answers the pool queries the base-image helpers make.
// paths maps a pool name to its directory, so a test can lay out real
// files and have GetPoolPath point at them.
type imagePoolBackend struct {
	compute.Backend
	pools     []models.StoragePool
	paths     map[string]string
	defPool   string
	refreshed []string

	// deps is what BackingDependents reports, keyed by volume name, so
	// a test can stand a linked clone in front of a delete.
	deps    map[string][]string
	depsErr error
}

func (b *imagePoolBackend) BackingDependents(pool, vol string) ([]string, error) {
	return b.deps[vol], b.depsErr
}

func (b *imagePoolBackend) BackingDependentsOfPath(path string) ([]string, error) {
	return b.deps[filepath.Base(path)], b.depsErr
}

func (b *imagePoolBackend) ListStoragePools() ([]models.StoragePool, error) { return b.pools, nil }
func (b *imagePoolBackend) DiskPoolName() string                            { return b.defPool }

func (b *imagePoolBackend) GetPoolPath(name string) (string, error) {
	if p, ok := b.paths[name]; ok {
		return p, nil
	}
	return "", fmt.Errorf("pool %q not found", name)
}

func (b *imagePoolBackend) RefreshPool(name string) error {
	b.refreshed = append(b.refreshed, name)
	return nil
}

// newImageHandler builds a handler over two disk pools plus an ISO pool
// (which must never be offered), with the default disk pool second in
// the backend's listing so the ordering logic is actually exercised.
func newImageHandler(t *testing.T) (*Handler, *imagePoolBackend, map[string]string) {
	t.Helper()
	root := t.TempDir()
	dirs := map[string]string{
		"Lexar-Discos": filepath.Join(root, "lexar"),
		"webkvm-disks": filepath.Join(root, "default"),
		"ISOS":         filepath.Join(root, "isos"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b := &imagePoolBackend{
		pools: []models.StoragePool{
			{Name: "Lexar-Discos", Purpose: compute.PoolPurposeDisk},
			{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk},
			{Name: "ISOS", Purpose: compute.PoolPurposeISO},
		},
		paths:   dirs,
		defPool: "webkvm-disks",
	}
	h := &Handler{compute: b, cfg: &config.Config{DataDir: filepath.Join(root, "data")}}
	return h, b, dirs
}

// writeImage drops a file big enough to pass the 1 MiB "is this really
// an image" floor the listing applies.
func writeImage(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolveBaseImagePool: the destination must be a real disk pool.
// An ISO pool or an invented name has to be refused before a multi-GB
// download starts, not after.
func TestResolveBaseImagePool(t *testing.T) {
	h, _, dirs := newImageHandler(t)

	// No choice means the default disk pool, not merely the first one
	// the backend happened to list.
	name, dir, err := h.resolveBaseImagePool("")
	if err != nil {
		t.Fatalf("empty pool: %v", err)
	}
	if name != "webkvm-disks" || dir != dirs["webkvm-disks"] {
		t.Errorf("default = %q/%q, want webkvm-disks/%q", name, dir, dirs["webkvm-disks"])
	}

	// An explicit, valid choice wins over the default.
	name, dir, err = h.resolveBaseImagePool("  Lexar-Discos  ")
	if err != nil {
		t.Fatalf("explicit pool: %v", err)
	}
	if name != "Lexar-Discos" || dir != dirs["Lexar-Discos"] {
		t.Errorf("explicit = %q/%q, want Lexar-Discos", name, dir)
	}

	// A base image is a VM disk; filing it in the ISO pool would hide
	// it from the disk browser and clutter the ISO one.
	for _, bad := range []string{"ISOS", "no-such-pool"} {
		if _, _, err := h.resolveBaseImagePool(bad); err == nil {
			t.Errorf("resolveBaseImagePool(%q) succeeded, want refusal", bad)
		}
	}
}

// An inactive pool has no mounted filesystem behind it, yet libvirt
// still reports a path for it. Downloading there would quietly fill the
// root filesystem under an unmounted mount point.
func TestResolveBaseImagePoolSkipsInactive(t *testing.T) {
	h, b, _ := newImageHandler(t)
	b.pools = []models.StoragePool{
		{Name: "Seagate", Purpose: compute.PoolPurposeDisk, State: "inactive"},
		{Name: "Lexar-Discos", Purpose: compute.PoolPurposeDisk, State: "active"},
	}
	b.defPool = "Seagate"

	if _, _, err := h.resolveBaseImagePool("Seagate"); err == nil {
		t.Error("an inactive pool was accepted as a download destination")
	}
	// Even as the default, an inactive pool must not be the fallback.
	name, _, err := h.resolveBaseImagePool("")
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if name != "Lexar-Discos" {
		t.Errorf("fallback = %q, want the one pool that is actually up", name)
	}

	// A backend that reports no state at all is not making a claim
	// about the pool being down, so the pool stays eligible.
	b.pools = []models.StoragePool{{Name: "Lexar-Discos", Purpose: compute.PoolPurposeDisk}}
	b.defPool = "Lexar-Discos"
	if _, _, err := h.resolveBaseImagePool("Lexar-Discos"); err != nil {
		t.Errorf("a pool with no reported state must stay usable: %v", err)
	}
}

func TestResolveBaseImagePoolNoDiskPools(t *testing.T) {
	h, b, _ := newImageHandler(t)
	b.pools = []models.StoragePool{{Name: "ISOS", Purpose: compute.PoolPurposeISO}}
	if _, _, err := h.resolveBaseImagePool(""); err == nil {
		t.Error("a host with no disk pool must fail loudly, not pick the ISO pool")
	}
}

// TestFindCachedBaseImage: the lookup scans every pool rather than
// trusting a recorded location, because the move feature can relocate
// the file at any time.
func TestFindCachedBaseImage(t *testing.T) {
	h, _, dirs := newImageHandler(t)

	if _, _, found := h.findCachedBaseImage("debian-13"); found {
		t.Fatal("reported an image that was never downloaded")
	}

	// Cached in a non-default pool: it must still be found.
	want := filepath.Join(dirs["Lexar-Discos"], "base-debian-13.qcow2")
	writeImage(t, want)
	got, pool, found := h.findCachedBaseImage("debian-13")
	if !found || got != want || pool != "Lexar-Discos" {
		t.Errorf("find = %q/%q/%v, want %q/Lexar-Discos/true", got, pool, found, want)
	}

	// A stub too small to be a real image is a leftover, not a cache
	// hit: reporting it would offer a broken disk for deployment.
	small := filepath.Join(dirs["webkvm-disks"], "base-tiny.qcow2")
	if err := os.WriteFile(small, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, found := h.findCachedBaseImage("tiny"); found {
		t.Error("a truncated file was reported as a cached image")
	}
}

// Images pulled by an older build live in DataDir/base-images under
// their bare id. They must keep working, or an upgrade would silently
// orphan every image the operator had already downloaded.
func TestFindCachedBaseImageLegacyFolder(t *testing.T) {
	h, _, _ := newImageHandler(t)
	legacy := filepath.Join(h.baseImagesDir(), "ubuntu-24.qcow2")
	writeImage(t, legacy)

	got, pool, found := h.findCachedBaseImage("ubuntu-24")
	if !found || got != legacy {
		t.Fatalf("find = %q/%v, want %q/true", got, found, legacy)
	}
	// No pool owns the legacy folder, and saying otherwise would make
	// the delete path try to refresh a pool that never held the file.
	if pool != "" {
		t.Errorf("pool = %q, want empty for the legacy folder", pool)
	}
}

// A pool copy must win over a legacy one: after a re-download the
// legacy file is the stale one.
func TestFindCachedBaseImagePrefersPool(t *testing.T) {
	h, _, dirs := newImageHandler(t)
	writeImage(t, filepath.Join(h.baseImagesDir(), "debian-13.qcow2"))
	inPool := filepath.Join(dirs["webkvm-disks"], "base-debian-13.qcow2")
	writeImage(t, inPool)

	got, pool, _ := h.findCachedBaseImage("debian-13")
	if got != inPool || pool != "webkvm-disks" {
		t.Errorf("find = %q/%q, want the pool copy %q", got, pool, inPool)
	}
}

// TestBaseImageName: the prefix keeps a base image from colliding with
// a volume the operator named themselves, now that both share a pool.
func TestBaseImageName(t *testing.T) {
	if got := baseImageName("debian-13"); got != "base-debian-13.qcow2" {
		t.Errorf("baseImageName = %q, want base-debian-13.qcow2", got)
	}
}

func deleteBaseImageRequest(h *Handler, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/images/cloud-base/"+id, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.DeleteBaseCloudImage(rec, req)
	return rec
}

// TestDeleteBaseCloudImage removes the file wherever it happens to be,
// and refuses an id that tries to name a path of its own.
func TestDeleteBaseCloudImage(t *testing.T) {
	h, b, dirs := newImageHandler(t)
	target := filepath.Join(dirs["Lexar-Discos"], "base-debian-13.qcow2")
	writeImage(t, target)

	if rec := deleteBaseImageRequest(h, "debian-13"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("the cached image is still on disk")
	}
	// The pool listing still shows the volume until it is rescanned.
	if len(b.refreshed) == 0 || b.refreshed[0] != "Lexar-Discos" {
		t.Errorf("refreshed = %v, want the pool that held the image", b.refreshed)
	}

	// Deleting something already gone is the state the caller asked
	// for; a 404 would make a retry look like a failure.
	if rec := deleteBaseImageRequest(h, "debian-13"); rec.Code != http.StatusOK {
		t.Errorf("second delete status = %d, want 200", rec.Code)
	}
}

// The id is interpolated into a file name, so a traversal attempt must
// never reach the filesystem.
func TestDeleteBaseCloudImageRejectsTraversal(t *testing.T) {
	h, _, _ := newImageHandler(t)
	for _, id := range []string{"../escape", "a/b", `a\b`, "..", ""} {
		if rec := deleteBaseImageRequest(h, id); rec.Code != http.StatusBadRequest {
			t.Errorf("delete id %q = %d, want 400", id, rec.Code)
		}
	}
}

// A cached base image exists to be cloned copy-on-write, so deleting
// one while a linked clone is still backed by it destroys that clone:
// the overlay stops opening entirely. The attachment guards cannot see
// this — the disk attached to the VM is the overlay, not the image
// underneath — so the delete path has to ask explicitly.
func TestDeleteBaseCloudImageRefusesWithDependents(t *testing.T) {
	h, b, dirs := newImageHandler(t)
	target := filepath.Join(dirs["Lexar-Discos"], "base-debian-13.qcow2")
	writeImage(t, target)
	b.deps = map[string][]string{"base-debian-13.qcow2": {"web-01.qcow2", "web-02.qcow2"}}

	rec := deleteBaseImageRequest(h, "debian-13")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "web-01.qcow2") {
		t.Errorf("the refusal does not name the clones that block it: %s", rec.Body.String())
	}
	if _, err := os.Stat(target); err != nil {
		t.Error("the image was deleted despite the refusal")
	}

	// Once the clones are gone, the delete goes through.
	b.deps = nil
	if rec := deleteBaseImageRequest(h, "debian-13"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 once nothing depends on it", rec.Code)
	}
}

// A backend that cannot answer must not be read as "nothing depends on
// it": that would wave through exactly the deletion the guard exists to
// stop.
func TestDeleteBaseCloudImageFailsClosed(t *testing.T) {
	h, b, dirs := newImageHandler(t)
	target := filepath.Join(dirs["Lexar-Discos"], "base-debian-13.qcow2")
	writeImage(t, target)
	b.depsErr = errors.New("libvirt is unreachable")

	rec := deleteBaseImageRequest(h, "debian-13")
	if rec.Code == http.StatusOK {
		t.Fatalf("the delete succeeded while the guard could not run (%s)", rec.Body.String())
	}
	if _, err := os.Stat(target); err != nil {
		t.Error("the image was deleted while the guard could not run")
	}
}

func TestDecompressGzipImage(t *testing.T) {
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "test.img.gz")
	decompPath := filepath.Join(dir, "test.img")

	content := []byte("decompressed test payload for cloud image")
	f, err := os.Create(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write(content); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	f.Close()

	if err := gunzipFile(gzPath, decompPath); err != nil {
		t.Fatalf("gunzipFile failed: %v", err)
	}

	got, err := os.ReadFile(decompPath)
	if err != nil {
		t.Fatalf("read decompressed file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("got %q, want %q", got, content)
	}
}
