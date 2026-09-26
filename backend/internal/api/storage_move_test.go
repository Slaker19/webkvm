package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// moveBackend answers just enough of compute.Backend to drive the two
// move handlers: the pool list (for purpose checks), the instance
// lookup (for the running/kind checks), and the moves themselves, which
// only record what they were asked to do.
type moveBackend struct {
	compute.Backend
	pools []models.StoragePool
	vms   map[string]models.VM

	// Recorded calls, so a test can assert the handler reached the
	// backend with the arguments it accepted — or never reached it.
	movedDomain  []string
	movedVolume  []string
	domainErr    error
	volumeErr    error
	moveCalledCh chan struct{}

	// templates holds the VM ids flagged as templates, which decides
	// whether a template pool will accept them.
	templates map[string]bool
}

func (m *moveBackend) GetVMMeta(id string) (models.VMMeta, error) {
	return models.VMMeta{Template: m.templates[id]}, nil
}

func (m *moveBackend) ListStoragePools() ([]models.StoragePool, error) { return m.pools, nil }

func (m *moveBackend) GetDomain(id string) (models.VM, error) {
	vm, ok := m.vms[id]
	if !ok {
		return models.VM{}, fmt.Errorf("domain %q not found", id)
	}
	return vm, nil
}

func (m *moveBackend) MoveDomainStorage(id, destPool string, _ func(float64, string)) error {
	m.movedDomain = append(m.movedDomain, id+"->"+destPool)
	m.signal()
	return m.domainErr
}

func (m *moveBackend) MoveVolume(srcPool, volName, destPool string, opts compute.MoveVolumeOpts) error {
	m.movedVolume = append(m.movedVolume,
		fmt.Sprintf("%s/%s->%s kind=%s copy=%v name=%s",
			srcPool, volName, destPool, opts.Kind, opts.KeepSource, opts.NewName))
	m.signal()
	return m.volumeErr
}

func (m *moveBackend) signal() {
	if m.moveCalledCh != nil {
		select {
		case m.moveCalledCh <- struct{}{}:
		default:
		}
	}
}

// newMoveHandler builds a handler over the standard fixture: two disk
// pools, one ISO pool, two container pools, one stopped VM, one running
// VM and one stopped container.
func newMoveHandler() (*Handler, *moveBackend) {
	b := &moveBackend{
		pools: []models.StoragePool{
			{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk},
			{Name: "Lexar-Discos", Purpose: compute.PoolPurposeDisk},
			{Name: "ISOS", Purpose: compute.PoolPurposeISO},
			{Name: "webkvm-incus", Purpose: compute.PoolPurposeContainer},
			{Name: "Lexar-Contenedores", Purpose: compute.PoolPurposeContainer},
			{Name: "Plantillas", Purpose: compute.PoolPurposeTemplate},
		},
		vms: map[string]models.VM{
			"stopped-vm": {Name: "stopped-vm", Type: "vm", State: models.VMStateShutoff},
			"running-vm": {Name: "running-vm", Type: "vm", State: models.VMStateRunning},
			"paused-vm":  {Name: "paused-vm", Type: "vm", State: models.VMStatePaused},
			"ct":         {Name: "ct", Type: "container", State: models.VMStateShutoff},
			"golden":     {Name: "golden", Type: "vm", State: models.VMStateShutoff},
		},
		templates:    map[string]bool{"golden": true},
		moveCalledCh: make(chan struct{}, 8),
	}
	return &Handler{compute: b}, b
}

// moveVMRequest issues POST /api/vms/{id}/move-storage as an admin,
// which is the role that skips the per-user pool ACL (exercised
// separately where the user store matters).
func moveVMRequest(h *Handler, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/vms/"+id+"/move-storage", strings.NewReader(body))
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", string(models.RoleAdmin))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.MoveVMStorage(rec, req)
	return rec
}

func moveVolumeRequest(h *Handler, pool, name, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost,
		"/api/storage/volumes/"+pool+"/"+name+"/move", strings.NewReader(body))
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", string(models.RoleAdmin))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("pool", pool)
	rctx.URLParams.Add("name", name)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.MoveVolume(rec, req)
	return rec
}

// TestMoveVMStorageValidation covers every synchronous rejection. These
// must be answered before a job is created: a client that gets a 202
// has to be able to assume the move was at least plausible.
func TestMoveVMStorageValidation(t *testing.T) {
	cases := []struct {
		name string
		vm   string
		body string
		want int
	}{
		{"missing pool", "stopped-vm", `{}`, http.StatusBadRequest},
		{"blank pool", "stopped-vm", `{"pool":"   "}`, http.StatusBadRequest},
		{"bad json", "stopped-vm", `{`, http.StatusBadRequest},
		{"unknown vm", "ghost", `{"pool":"Lexar-Discos"}`, http.StatusNotFound},
		{"unknown pool", "stopped-vm", `{"pool":"nope"}`, http.StatusBadRequest},
		// The separation that matters: a VM must not land in a
		// container pool, nor a container in a VM disk pool.
		{"vm into container pool", "stopped-vm", `{"pool":"webkvm-incus"}`, http.StatusBadRequest},
		{"container into disk pool", "ct", `{"pool":"Lexar-Discos"}`, http.StatusBadRequest},
		{"vm into iso pool", "stopped-vm", `{"pool":"ISOS"}`, http.StatusBadRequest},
		// A live QEMU holds its image open; the copy would be torn.
		{"running vm", "running-vm", `{"pool":"Lexar-Discos"}`, http.StatusConflict},
		{"paused vm", "paused-vm", `{"pool":"Lexar-Discos"}`, http.StatusConflict},
		// A template pool is a shelf for golden images: only a VM
		// actually flagged as a template may be put on it.
		{"plain vm into template pool", "stopped-vm", `{"pool":"Plantillas"}`, http.StatusBadRequest},
		{"container into template pool", "ct", `{"pool":"Plantillas"}`, http.StatusBadRequest},
		// The happy paths, each into a pool of its own world.
		{"vm into disk pool", "stopped-vm", `{"pool":"Lexar-Discos"}`, http.StatusAccepted},
		{"container into container pool", "ct", `{"pool":"Lexar-Contenedores"}`, http.StatusAccepted},
		{"template into template pool", "golden", `{"pool":"Plantillas"}`, http.StatusAccepted},
		// A template is still an ordinary VM disk, so it may also be
		// moved back into a normal disk pool.
		{"template into disk pool", "golden", `{"pool":"Lexar-Discos"}`, http.StatusAccepted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, b := newMoveHandler()
			rec := moveVMRequest(h, c.vm, c.body)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}
			if c.want != http.StatusAccepted && len(b.movedDomain) > 0 {
				t.Errorf("rejected request still reached the backend: %v", b.movedDomain)
			}
			if c.want == http.StatusAccepted {
				<-b.moveCalledCh
			}
		})
	}
}

// TestMoveVolumeValidation pins the ISO/disk separation and the input
// sanitation on the volume path.
func TestMoveVolumeValidation(t *testing.T) {
	cases := []struct {
		name       string
		pool, vol  string
		body       string
		want       int
		wantRecord string
	}{
		{"missing pool", "webkvm-disks", "a.qcow2", `{}`, http.StatusBadRequest, ""},
		{"bad json", "webkvm-disks", "a.qcow2", `{`, http.StatusBadRequest, ""},
		{"same pool", "webkvm-disks", "a.qcow2", `{"pool":"webkvm-disks"}`, http.StatusConflict, ""},
		{"bad kind", "webkvm-disks", "a.qcow2", `{"pool":"Lexar-Discos","kind":"weird"}`, http.StatusBadRequest, ""},
		{"unknown src pool", "nope", "a.qcow2", `{"pool":"Lexar-Discos"}`, http.StatusBadRequest, ""},
		{"unknown dest pool", "webkvm-disks", "a.qcow2", `{"pool":"nope"}`, http.StatusBadRequest, ""},
		// A disk must not be filed into an ISO pool, nor the reverse.
		{"disk into iso pool", "webkvm-disks", "a.qcow2", `{"pool":"ISOS"}`, http.StatusBadRequest, ""},
		{"iso from disk pool", "webkvm-disks", "a.iso", `{"pool":"ISOS","kind":"iso"}`, http.StatusBadRequest, ""},
		// Containers live in Incus, which has no ISO or disk volumes
		// of this shape; a container pool is never a valid endpoint.
		{"disk into container pool", "webkvm-disks", "a.qcow2", `{"pool":"webkvm-incus"}`, http.StatusBadRequest, ""},
		// A rename must not be able to write outside the pool.
		{"traversal new_name", "webkvm-disks", "a.qcow2",
			`{"pool":"Lexar-Discos","new_name":"../escape.qcow2"}`, http.StatusBadRequest, ""},
		{"separator new_name", "webkvm-disks", "a.qcow2",
			`{"pool":"Lexar-Discos","new_name":"sub/dir.qcow2"}`, http.StatusBadRequest, ""},
		// Happy paths, including that "kind" defaults to disk and that
		// copy=true is forwarded as KeepSource.
		{"disk move", "webkvm-disks", "a.qcow2", `{"pool":"Lexar-Discos"}`,
			http.StatusAccepted, "webkvm-disks/a.qcow2->Lexar-Discos kind=disk copy=false name="},
		{"disk copy renamed", "webkvm-disks", "a.qcow2",
			`{"pool":"Lexar-Discos","copy":true,"new_name":"b.qcow2"}`,
			http.StatusAccepted, "webkvm-disks/a.qcow2->Lexar-Discos kind=disk copy=true name=b.qcow2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, b := newMoveHandler()
			rec := moveVolumeRequest(h, c.pool, c.vol, c.body)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}
			if c.want != http.StatusAccepted {
				if len(b.movedVolume) > 0 {
					t.Errorf("rejected request still reached the backend: %v", b.movedVolume)
				}
				return
			}
			<-b.moveCalledCh
			if len(b.movedVolume) != 1 || b.movedVolume[0] != c.wantRecord {
				t.Errorf("backend saw %v, want [%q]", b.movedVolume, c.wantRecord)
			}
		})
	}
}

// TestMoveErrStatus keeps each failure mapped to a status the operator
// can act on. A 500 for "the VM is running" would read as a server bug
// rather than as something they can fix by stopping the VM.
func TestMoveErrStatus(t *testing.T) {
	cases := map[error]int{
		compute.ErrDomainMustBeStoppedToMove: http.StatusConflict,
		compute.ErrSamePool:                  http.StatusConflict,
		compute.ErrVolumeInUse:               http.StatusConflict,
		compute.ErrVolumeHasDependents:       http.StatusConflict,
		compute.ErrBackingCheckUnavailable:   http.StatusConflict,
		compute.ErrInsufficientSpace:         http.StatusConflict,
		compute.ErrCrossBackendMove:          http.StatusBadRequest,
		compute.ErrNotImplemented:            http.StatusNotImplemented,
		errors.New("disk on fire"):           http.StatusInternalServerError,
	}
	for err, want := range cases {
		if got := moveErrStatus(err); got != want {
			t.Errorf("moveErrStatus(%v) = %d, want %d", err, got, want)
		}
		// Wrapped errors must map the same way: the backends add
		// context ("copy foo: %w") to every one of these.
		wrapped := fmt.Errorf("while moving: %w", err)
		if got := moveErrStatus(wrapped); got != want {
			t.Errorf("moveErrStatus(wrapped %v) = %d, want %d", err, got, want)
		}
	}
}

// TestIsContainerVM: the kind decides which pool world an instance may
// move into, so it must not depend on how the backend spelled it.
func TestIsContainerVM(t *testing.T) {
	for _, typ := range []string{"container", "Container", "lxc", "LXC"} {
		if !isContainerVM(models.VM{Type: typ}) {
			t.Errorf("isContainerVM(%q) = false, want true", typ)
		}
	}
	for _, typ := range []string{"vm", "VM", "kvm", ""} {
		if isContainerVM(models.VM{Type: typ}) {
			t.Errorf("isContainerVM(%q) = true, want false", typ)
		}
	}
}
