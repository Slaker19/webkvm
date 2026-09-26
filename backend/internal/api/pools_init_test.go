package api

import (
	"context"
	"fmt"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// TestParsePoolPurpose: the five valid purposes are accepted (case
// and spacing free, "lxc" aliased to "container") and everything else
// is rejected — including the old comma-separated syntax, because a
// pool now has exactly one purpose.
func TestParsePoolPurpose(t *testing.T) {
	valid := map[string]string{
		"disk":      compute.PoolPurposeDisk,
		"iso":       compute.PoolPurposeISO,
		"container": compute.PoolPurposeContainer,
		"backup":    compute.PoolPurposeBackup,
		"template":  compute.PoolPurposeTemplate,
		"lxc":       compute.PoolPurposeContainer,
		"  ISO  ":   compute.PoolPurposeISO,
		"LXC":       compute.PoolPurposeContainer,
	}
	for in, want := range valid {
		got, ok := parsePoolPurpose(in)
		if !ok || got != want {
			t.Errorf("parsePoolPurpose(%q) = %q,%v; want %q,true", in, got, ok, want)
		}
	}

	// Multi-purpose CSV is the unified-pool syntax this design
	// removed: it must not sneak through as a valid purpose.
	for _, in := range []string{"", "   ", "disk,iso", "iso,disk,container", "disk,", ",", "bogus", "vm"} {
		if got, ok := parsePoolPurpose(in); ok {
			t.Errorf("parsePoolPurpose(%q) = %q, want rejected", in, got)
		}
	}
}

// legacyFallbackPurpose only serves clients that omit the subfolders
// field; anything it can't parse is a disk pool, and the pool lands
// in the backend that owns that purpose.
func TestLegacyFallbackPurpose(t *testing.T) {
	cases := map[string]struct{ purpose, backend string }{
		"":          {compute.PoolPurposeDisk, "libvirt"},
		"bogus":     {compute.PoolPurposeDisk, "libvirt"},
		"disk,iso":  {compute.PoolPurposeDisk, "libvirt"},
		"lxc":       {compute.PoolPurposeContainer, "incus"},
		"container": {compute.PoolPurposeContainer, "incus"},
		"ISO":       {compute.PoolPurposeISO, "libvirt"},
		"backup":    {compute.PoolPurposeBackup, "libvirt"},
	}
	for in, want := range cases {
		got := legacyFallbackPurpose(in)
		if got != want.purpose {
			t.Errorf("legacyFallbackPurpose(%q) = %q, want %q", in, got, want.purpose)
		}
		if b := backendForPurpose(got); b != want.backend {
			t.Errorf("backendForPurpose(%q) = %q, want %q", got, b, want.backend)
		}
	}
}

// allowedSubfolder is the single gate for both folder creation and
// pool derivation: a folder that can be created must have a nature,
// and vice versa.
func TestAllowedSubfolder(t *testing.T) {
	for _, in := range []string{"isos", "/isos", " isos/ ", "discos", "disks", "images",
		"contenedores", "containers", "backups", "plantillas", "templates"} {
		sub, ok := allowedSubfolder(in)
		if !ok {
			t.Errorf("allowedSubfolder(%q) rejected a standard folder", in)
			continue
		}
		if _, hasNature := subfolderNature[sub]; !hasNature {
			t.Errorf("allowedSubfolder(%q) -> %q has no pool nature", in, sub)
		}
	}
	// Path traversal and unknown names must not become directories.
	for _, in := range []string{"", "  ", "..", "../etc", "etc/passwd", "random", "ISOS"} {
		if _, ok := allowedSubfolder(in); ok {
			t.Errorf("allowedSubfolder(%q) must be rejected", in)
		}
	}
}

// TestSubfolderPoolSpec: every nature folder maps to its own
// independent pool — name with the purpose suffix, path rooted at
// the subfolder, container routed to Incus. Unknown folders and
// empty inputs produce no pool.
func TestSubfolderPoolSpec(t *testing.T) {
	cases := []struct {
		sub         string
		vol         string
		mount       string
		wantName    string
		wantPath    string
		wantPurpose string
		wantBackend string
	}{
		{"discos", "mydisk", "/mnt/mydisk", "mydisk-vdi", "/mnt/mydisk/discos", "disk", "libvirt"},
		{"isos", "mydisk", "/mnt/mydisk", "mydisk-isos", "/mnt/mydisk/isos", "iso", "libvirt"},
		{"contenedores", "mydisk", "/mnt/mydisk", "mydisk-containers", "/mnt/mydisk/contenedores", "container", "incus"},
		{"backups", "mydisk", "/mnt/mydisk", "mydisk-backups", "/mnt/mydisk/backups", "backup", "libvirt"},
		{"plantillas", "mydisk", "/mnt/mydisk", "mydisk-plantillas", "/mnt/mydisk/plantillas", "template", "libvirt"},
		// Legacy aliases resolve to the same nature pools.
		{"disks", "mydisk", "/mnt/mydisk", "mydisk-vdi", "/mnt/mydisk/disks", "disk", "libvirt"},
		{"containers", "mydisk", "/mnt/mydisk", "mydisk-containers", "/mnt/mydisk/containers", "container", "incus"},
		{"templates", "mydisk", "/mnt/mydisk", "mydisk-plantillas", "/mnt/mydisk/templates", "template", "libvirt"},
	}
	for _, c := range cases {
		req, backend, ok := subfolderPoolSpec(c.sub, c.vol, c.mount)
		if !ok {
			t.Errorf("subfolderPoolSpec(%q) not ok, want pool %q", c.sub, c.wantName)
			continue
		}
		if req.Name != c.wantName || req.Path != c.wantPath || req.Purpose != c.wantPurpose || backend != c.wantBackend {
			t.Errorf("subfolderPoolSpec(%q) = %+v backend %q, want name %q path %q purpose %q backend %q",
				c.sub, req, backend, c.wantName, c.wantPath, c.wantPurpose, c.wantBackend)
		}
		if req.Type != "dir" {
			t.Errorf("subfolderPoolSpec(%q) type = %q, want dir", c.sub, req.Type)
		}
	}
}

func TestSubfolderPoolSpecRejects(t *testing.T) {
	for _, sub := range []string{"", "  ", "random", "../etc", "discos/extra", "DISCOS"} {
		if _, _, ok := subfolderPoolSpec(sub, "mydisk", "/mnt/mydisk"); ok {
			t.Errorf("subfolderPoolSpec(%q) ok, want rejection", sub)
		}
	}
	if _, _, ok := subfolderPoolSpec("discos", "", "/mnt/mydisk"); ok {
		t.Error("empty volume name must not produce a pool")
	}
	if _, _, ok := subfolderPoolSpec("discos", "mydisk", ""); ok {
		t.Error("empty mount point must not produce a pool")
	}
}

// recordingPoolBackend captures every CreateStoragePool call so the
// per-folder registration can be asserted without libvirt/Incus.
// failOn makes a specific pool name fail, to exercise the error and
// "already exists" branches.
type recordingPoolBackend struct {
	compute.Backend
	created []models.CreatePoolRequest
	failOn  map[string]error
	// existingPaths answers GetPoolPath for pools that already
	// exist, so the path-mismatch detection can be exercised.
	existingPaths map[string]string
}

func (r *recordingPoolBackend) CreateStoragePool(_ context.Context, req models.CreatePoolRequest) (models.StoragePool, error) {
	r.created = append(r.created, req)
	if err, ok := r.failOn[req.Name]; ok {
		return models.StoragePool{}, err
	}
	return models.StoragePool{Name: req.Name, Purpose: req.Purpose, Path: req.Path}, nil
}

func (r *recordingPoolBackend) GetPoolPath(name string) (string, error) {
	if p, ok := r.existingPaths[name]; ok {
		return p, nil
	}
	return "", fmt.Errorf("pool %q not found", name)
}

func newRecordingHandler(failOn map[string]error) (*Handler, *recordingPoolBackend) {
	b := &recordingPoolBackend{failOn: failOn}
	return &Handler{compute: b}, b
}

// TestCreatePoolsForSubfolders: every marked folder with a nature
// becomes its OWN pool — three folders means three independent pools,
// never one unified pool.
func TestCreatePoolsForSubfolders(t *testing.T) {
	h, backend := newRecordingHandler(nil)
	got := h.createPoolsForSubfolders(context.Background(), "mydisk", "/mnt/mydisk",
		[]string{"discos", "contenedores", "isos"}, "")

	if len(got) != 3 || len(backend.created) != 3 {
		t.Fatalf("got %d results / %d creates, want 3 independent pools", len(got), len(backend.created))
	}
	want := map[string]struct{ purpose, backend, path string }{
		"mydisk-vdi":        {"disk", "libvirt", "/mnt/mydisk/discos"},
		"mydisk-containers": {"container", "incus", "/mnt/mydisk/contenedores"},
		"mydisk-isos":       {"iso", "libvirt", "/mnt/mydisk/isos"},
	}
	for _, r := range got {
		w, ok := want[r.Name]
		if !ok {
			t.Errorf("unexpected pool %q", r.Name)
			continue
		}
		if r.Purpose != w.purpose || r.Backend != w.backend || r.Status != "created" {
			t.Errorf("pool %q = purpose %q backend %q status %q, want %q/%q/created",
				r.Name, r.Purpose, r.Backend, r.Status, w.purpose, w.backend)
		}
	}
	for _, req := range backend.created {
		if req.Path != want[req.Name].path {
			t.Errorf("pool %q path = %q, want %q", req.Name, req.Path, want[req.Name].path)
		}
		// A unified pool would be rooted at the mount point itself.
		if req.Path == "/mnt/mydisk" {
			t.Errorf("pool %q is rooted at the mount point — unified pools are forbidden", req.Name)
		}
	}
}

// An EMPTY subfolder list means the operator unchecked everything:
// nothing is created. Collapsing it into the legacy fallback would
// silently recreate the unified multi-purpose pool at the mount root.
func TestCreatePoolsForSubfoldersEmptyCreatesNothing(t *testing.T) {
	for _, subs := range [][]string{{}, {"random"}, {"", "  "}} {
		h, backend := newRecordingHandler(nil)
		got := h.createPoolsForSubfolders(context.Background(), "mydisk", "/mnt/mydisk", subs, "disk")
		if len(got) != 0 || len(backend.created) != 0 {
			t.Errorf("subfolders %v created %d pools (%+v), want none", subs, len(backend.created), got)
		}
	}
}

// A client that omits the field entirely (nil) is pre-per-purpose and
// still gets its single pool at the mount root.
func TestCreatePoolsForSubfoldersLegacyFallback(t *testing.T) {
	cases := []struct{ purpose, wantPurpose, wantBackend string }{
		{"", "disk", "libvirt"},
		{"disk", "disk", "libvirt"},
		{"lxc", "container", "incus"},
		{"container", "container", "incus"},
		{"ISO", "iso", "libvirt"},
	}
	for _, c := range cases {
		h, backend := newRecordingHandler(nil)
		got := h.createPoolsForSubfolders(context.Background(), "mydisk", "/mnt/mydisk", nil, c.purpose)
		if len(got) != 1 || len(backend.created) != 1 {
			t.Fatalf("purpose %q: got %d pools, want 1 legacy pool", c.purpose, len(got))
		}
		if got[0].Name != "mydisk" || got[0].Purpose != c.wantPurpose || got[0].Backend != c.wantBackend {
			t.Errorf("purpose %q: got %+v, want mydisk/%s/%s", c.purpose, got[0], c.wantPurpose, c.wantBackend)
		}
		if backend.created[0].Path != "/mnt/mydisk" {
			t.Errorf("purpose %q: legacy pool path = %q, want the mount root", c.purpose, backend.created[0].Path)
		}
	}
}

// Duplicate folders that map to the same nature (discos + disks) must
// register the pool once, not twice.
func TestCreatePoolsForSubfoldersDedupes(t *testing.T) {
	h, backend := newRecordingHandler(nil)
	got := h.createPoolsForSubfolders(context.Background(), "mydisk", "/mnt/mydisk",
		[]string{"discos", "disks", "images", "contenedores", "containers"}, "")
	if len(got) != 2 || len(backend.created) != 2 {
		t.Fatalf("got %d pools, want 2 (one disk, one container): %+v", len(got), got)
	}
}

// Registration is best-effort: the disk is already formatted and
// mounted, so one failing pool must not abort the others, and a name
// collision reports "exists" rather than an error so a re-run of init
// converges instead of failing.
func TestCreatePoolsForSubfoldersBestEffort(t *testing.T) {
	h, _ := newRecordingHandler(map[string]error{
		"mydisk-vdi":        fmt.Errorf("pool 'mydisk-vdi' already exists with uuid 1234"),
		"mydisk-containers": fmt.Errorf("The storage pool already exists: The record already exists"),
		"mydisk-isos":       fmt.Errorf("permission denied"),
	})
	got := h.createPoolsForSubfolders(context.Background(), "mydisk", "/mnt/mydisk",
		[]string{"discos", "contenedores", "isos", "backups"}, "")
	if len(got) != 4 {
		t.Fatalf("got %d pools, want 4", len(got))
	}
	status := map[string]string{}
	for _, r := range got {
		status[r.Name] = r.Status
	}
	want := map[string]string{
		"mydisk-vdi":        "exists",
		"mydisk-containers": "exists",
		"mydisk-isos":       "error",
		"mydisk-backups":    "created",
	}
	for name, w := range want {
		if status[name] != w {
			t.Errorf("pool %q status = %q, want %q", name, status[name], w)
		}
	}
	for _, r := range got {
		if r.Status == "error" && r.Error == "" {
			t.Errorf("pool %q has status error but no message", r.Name)
		}
	}
}

// A pre-existing pool pointing at a DIFFERENT directory is silent
// data misrouting: disks would land on the other disk entirely. The
// row must flag the mismatch and report the real path, not show a
// plain "exists".
func TestCreateOnePoolFlagsPathMismatch(t *testing.T) {
	h, backend := newRecordingHandler(map[string]error{
		"mydisk-vdi":  fmt.Errorf("pool 'mydisk-vdi' already exists with uuid 1234"),
		"mydisk-isos": fmt.Errorf("pool 'mydisk-isos' already exists with uuid 5678"),
	})
	backend.existingPaths = map[string]string{
		// Same folder, just a trailing slash: NOT a mismatch.
		"mydisk-isos": "/mnt/mydisk/isos/",
		// A completely different disk: mismatch.
		"mydisk-vdi": "/mnt/otro-disco/discos",
	}
	got := h.createPoolsForSubfolders(context.Background(), "mydisk", "/mnt/mydisk",
		[]string{"discos", "isos"}, "")

	byName := map[string]PoolResult{}
	for _, r := range got {
		byName[r.Name] = r
	}
	vdi := byName["mydisk-vdi"]
	if vdi.Status != "exists" || !vdi.Mismatch {
		t.Errorf("mismatched pool = %+v, want status exists + Mismatch", vdi)
	}
	if vdi.Path != "/mnt/otro-disco/discos" || vdi.Error == "" {
		t.Errorf("mismatched pool must report the real path and a message: %+v", vdi)
	}
	isos := byName["mydisk-isos"]
	if isos.Status != "exists" || isos.Mismatch || isos.Error != "" {
		t.Errorf("same path (modulo trailing slash) must not be a mismatch: %+v", isos)
	}
}
