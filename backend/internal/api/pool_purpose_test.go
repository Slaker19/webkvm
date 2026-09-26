package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// fakePoolBackend answers ListStoragePools with a fixed set of pools so
// poolPurposeFor/requireISOPool/requireDiskPool can be tested without a
// real libvirt/Incus connection. Only the methods the handlers under
// test actually reach are overridden; the embedded nil interface panics
// on anything else, which keeps the tests honest about what they touch.
type fakePoolBackend struct {
	compute.Backend
	pools    []models.StoragePool
	poolPath string
}

func (f *fakePoolBackend) ListStoragePools() ([]models.StoragePool, error) {
	return f.pools, nil
}

func (f *fakePoolBackend) GetPoolPath(name string) (string, error) {
	if f.poolPath != "" {
		return f.poolPath, nil
	}
	return "", fmt.Errorf("pool %q not found", name)
}

func (f *fakePoolBackend) RefreshPool(name string) error { return nil }

func newPoolTestHandler(pools ...models.StoragePool) *Handler {
	return &Handler{compute: &fakePoolBackend{pools: pools}}
}

func newPoolTestHandlerWithPath(path string, pools ...models.StoragePool) *Handler {
	return &Handler{compute: &fakePoolBackend{pools: pools, poolPath: path}}
}

func TestPoolPurposeFor(t *testing.T) {
	h := newPoolTestHandler(
		models.StoragePool{Name: "ISOS", Purpose: compute.PoolPurposeISO},
		models.StoragePool{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk},
	)

	cases := []struct {
		name string
		want string
	}{
		{"ISOS", compute.PoolPurposeISO},
		{"webkvm-disks", compute.PoolPurposeDisk},
		{"does-not-exist", ""},
	}
	for _, c := range cases {
		if got := h.poolPurposeFor(c.name); got != c.want {
			t.Errorf("poolPurposeFor(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPoolPurposeFor_NilCompute(t *testing.T) {
	h := &Handler{}
	if got := h.poolPurposeFor("anything"); got != "" {
		t.Errorf("poolPurposeFor with nil compute = %q, want empty", got)
	}
}

func TestRequireISOPool(t *testing.T) {
	h := newPoolTestHandler(
		models.StoragePool{Name: "ISOS", Purpose: compute.PoolPurposeISO},
		models.StoragePool{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk},
	)

	cases := []struct {
		name   string
		pool   string
		wantOK bool
	}{
		{"iso pool accepted", "ISOS", true},
		{"disk pool rejected", "webkvm-disks", false},
		{"unknown pool rejected", "nope", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			ok := h.requireISOPool(rr, c.pool)
			if ok != c.wantOK {
				t.Fatalf("requireISOPool(%q) = %v, want %v", c.pool, ok, c.wantOK)
			}
			if !ok && rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rr.Code)
			}
		})
	}
}

func TestRequireDiskPool(t *testing.T) {
	h := newPoolTestHandler(
		models.StoragePool{Name: "ISOS", Purpose: compute.PoolPurposeISO},
		models.StoragePool{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk},
	)

	cases := []struct {
		name   string
		pool   string
		wantOK bool
	}{
		{"disk pool accepted", "webkvm-disks", true},
		{"iso pool rejected", "ISOS", false},
		{"unknown pool rejected", "nope", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			ok := h.requireDiskPool(rr, c.pool)
			if ok != c.wantOK {
				t.Fatalf("requireDiskPool(%q) = %v, want %v", c.pool, ok, c.wantOK)
			}
			if !ok && rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rr.Code)
			}
		})
	}
}

// TestRequirePoolGuards_LegacyCSVPurpose: installations predating the
// per-purpose model persisted unified pools as "iso,disk" or
// "disk,iso,container". Comparing the purpose with == made those pools
// fail BOTH guards, so a pool the UI listed as valid answered 400 to
// every ISO upload and every disk write. The guards must test
// membership, exactly like frontend/src/lib/purpose.js does.
func TestRequirePoolGuards_LegacyCSVPurpose(t *testing.T) {
	h := newPoolTestHandler(
		models.StoragePool{Name: "unified", Purpose: "iso,disk"},
		models.StoragePool{Name: "mydisk", Purpose: "disk,iso,container"},
		// Spacing is not normalized on the way in.
		models.StoragePool{Name: "spaced", Purpose: "disk, iso"},
		// A pool defined before purposes existed is a disk pool.
		models.StoragePool{Name: "undeclared", Purpose: ""},
		models.StoragePool{Name: "legacy-lxc", Purpose: "lxc"},
	)

	cases := []struct {
		pool     string
		wantISO  bool
		wantDisk bool
	}{
		{"unified", true, true},
		{"mydisk", true, true},
		{"spaced", true, true},
		{"undeclared", false, true},
		{"legacy-lxc", false, false},
	}
	for _, c := range cases {
		t.Run(c.pool, func(t *testing.T) {
			if got := h.requireISOPool(httptest.NewRecorder(), c.pool); got != c.wantISO {
				t.Errorf("requireISOPool(%q) = %v, want %v", c.pool, got, c.wantISO)
			}
			if got := h.requireDiskPool(httptest.NewRecorder(), c.pool); got != c.wantDisk {
				t.Errorf("requireDiskPool(%q) = %v, want %v", c.pool, got, c.wantDisk)
			}
		})
	}
}

// TestRequirePoolGuards_UnknownPoolStaysRejected pins the distinction
// the "" default could have erased: an EXISTING pool with no declared
// purpose is a disk pool, but a pool that does not exist at all must
// still be refused — otherwise any invented name would pass the disk
// guard.
func TestRequirePoolGuards_UnknownPoolStaysRejected(t *testing.T) {
	h := newPoolTestHandler(
		models.StoragePool{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk},
	)
	rr := httptest.NewRecorder()
	if h.requireDiskPool(rr, "invented") {
		t.Error("requireDiskPool accepted a pool that does not exist")
	}
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	if h.requireISOPool(httptest.NewRecorder(), "invented") {
		t.Error("requireISOPool accepted a pool that does not exist")
	}
	// Nil compute must not open the guards either.
	empty := &Handler{}
	if empty.requireDiskPool(httptest.NewRecorder(), "webkvm-disks") {
		t.Error("requireDiskPool passed with no compute backend")
	}
}

// TestCreateVolume_RejectsISOPool covers Task 3: creating a blank volume
// inside the ISO pool must be refused before any backend call.
func TestCreateVolume_RejectsISOPool(t *testing.T) {
	h := newPoolTestHandler(
		models.StoragePool{Name: "ISOS", Purpose: compute.PoolPurposeISO},
	)
	body := `{"name":"test-vol","capacity":10,"pool":"ISOS"}`
	req := httptest.NewRequest(http.MethodPost, "/api/storage/volumes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", "admin")
	rr := httptest.NewRecorder()

	h.CreateVolume(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

// TestAssertPoolPurpose is the backend half of the "storage worlds never
// mix" invariant. The frontend filters its selectors, but a filtered
// dropdown is not a guarantee: the API is reachable directly, and the
// bug this guards against was reproduced live — a CreateVM naming the
// ISO pool wrote a qcow2 into the ISO library next to the install
// media, because both are plain libvirt directory pools and nothing
// lower down objects.
func TestAssertPoolPurpose(t *testing.T) {
	h := newPoolTestHandler(
		models.StoragePool{Name: "ISOS", Purpose: compute.PoolPurposeISO},
		models.StoragePool{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk},
		models.StoragePool{Name: "webkvm-incus", Purpose: compute.PoolPurposeContainer},
		models.StoragePool{Name: "Plantillas", Purpose: compute.PoolPurposeTemplate},
		models.StoragePool{Name: "legacy", Purpose: "iso,disk"},
	)

	cases := []struct {
		pool    string
		want    string
		wantErr bool
	}{
		{"webkvm-disks", compute.PoolPurposeDisk, false},
		{"ISOS", compute.PoolPurposeDisk, true},
		{"webkvm-incus", compute.PoolPurposeDisk, true},
		{"Plantillas", compute.PoolPurposeDisk, true},
		{"webkvm-incus", compute.PoolPurposeContainer, false},
		{"webkvm-disks", compute.PoolPurposeContainer, true},
		{"ISOS", compute.PoolPurposeISO, false},
		// A legacy multi-purpose row satisfies either nature.
		{"legacy", compute.PoolPurposeDisk, false},
		{"legacy", compute.PoolPurposeISO, false},
		// An unknown pool is refused rather than silently defaulted.
		{"nope", compute.PoolPurposeDisk, true},
		// An empty name means "use the caller's default", checked there.
		{"", compute.PoolPurposeDisk, false},
	}
	for _, tc := range cases {
		err := h.assertPoolPurpose(tc.pool, tc.want)
		if tc.wantErr && err == nil {
			t.Errorf("assertPoolPurpose(%q, %q): want error, got nil", tc.pool, tc.want)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("assertPoolPurpose(%q, %q): unexpected error %v", tc.pool, tc.want, err)
		}
	}
}

// The error text is what the operator reads in the dialog, so it must
// name the pool and both natures rather than say "invalid".
func TestAssertPoolPurposeMessage(t *testing.T) {
	h := newPoolTestHandler(models.StoragePool{Name: "ISOS", Purpose: compute.PoolPurposeISO})
	err := h.assertPoolPurpose("ISOS", compute.PoolPurposeDisk)
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"ISOS", "iso", "disk"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err.Error(), want)
		}
	}
}

// The import endpoint serves both worlds from one upload, so it accepts
// a disk OR a container pool — but still not the ISO library.
func TestAssertPoolPurposeAny(t *testing.T) {
	h := newPoolTestHandler(
		models.StoragePool{Name: "ISOS", Purpose: compute.PoolPurposeISO},
		models.StoragePool{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk},
		models.StoragePool{Name: "webkvm-incus", Purpose: compute.PoolPurposeContainer},
	)
	both := []string{compute.PoolPurposeDisk, compute.PoolPurposeContainer}
	if err := h.assertPoolPurposeAny("webkvm-disks", both...); err != nil {
		t.Errorf("disk pool refused: %v", err)
	}
	if err := h.assertPoolPurposeAny("webkvm-incus", both...); err != nil {
		t.Errorf("container pool refused: %v", err)
	}
	if err := h.assertPoolPurposeAny("ISOS", both...); err == nil {
		t.Error("the ISO library was accepted as an import target")
	}
	if err := h.assertPoolPurposeAny("nope", both...); err == nil {
		t.Error("an unknown pool was accepted")
	}
}
