package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/user"
)

// createContainerBackend is a minimal compute.Backend for CreateVM: the
// embedded nil interface panics on anything not overridden, so the test
// fails loudly if the handler starts reaching for something new.
type createContainerBackend struct {
	compute.Backend
	pools   []models.StoragePool
	vms     []models.VM
	created []models.CreateVMRequest
}

func (b *createContainerBackend) ListStoragePools() ([]models.StoragePool, error) {
	return b.pools, nil
}

func (b *createContainerBackend) DiskPoolName() string { return "webkvm-disks" }

func (b *createContainerBackend) ListDomains() ([]models.VM, error) { return b.vms, nil }

func (b *createContainerBackend) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	b.created = append(b.created, req)
	return models.VM{ID: "ct-" + req.Name, Name: req.Name, Hypervisor: "incus", Type: "container"}, nil
}

func (b *createContainerBackend) UpdateVMMeta(id string, upd models.VMMetaUpdate) (models.VMMeta, error) {
	return models.VMMeta{}, nil
}

func newCreateContainerHarness(t *testing.T, q models.Quota, vms ...models.VM) (*Handler, *createContainerBackend) {
	t.Helper()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")
	us, err := user.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := us.Create(models.CreateUserRequest{
		Username:     "carol",
		Password:     "Str0ng-Pass#2026",
		Role:         models.RoleOperator,
		AllowedPools: []string{"webkvm-incus"},
		Quota:        q,
	}); err != nil {
		t.Fatal(err)
	}
	be := &createContainerBackend{
		pools: []models.StoragePool{
			{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk, State: "active"},
			{Name: "webkvm-incus", Purpose: compute.PoolPurposeContainer, State: "created"},
		},
		vms: vms,
	}
	return &Handler{userStore: us, compute: be}, be
}

func postCreateVM(t *testing.T, h *Handler, body models.CreateVMRequest, userName, role string) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/vms", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", userName)
	req.Header.Set("X-Role", role)
	rec := httptest.NewRecorder()
	h.CreateVM(rec, req)
	return rec
}

// A user restricted to the Incus pool creating a container without
// naming a pool used to be ACL-checked against the libvirt disk pool
// (403). The effective container pool must be resolved, authorised and
// handed to the backend.
func TestCreateVM_ContainerNoPool_ResolvesContainerPool(t *testing.T) {
	h, be := newCreateContainerHarness(t, models.Quota{})
	rec := postCreateVM(t, h, models.CreateVMRequest{
		Name: "ct1", Type: "container", Image: "images:debian/12", DiskGB: 8,
	}, "carol", models.RoleOperator)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	if len(be.created) != 1 {
		t.Fatalf("CreateDomain calls = %d, want 1", len(be.created))
	}
	if got := be.created[0].StoragePool; got != "webkvm-incus" {
		t.Fatalf("StoragePool = %q, want webkvm-incus", got)
	}
}

// The per-pool quota must be booked on the container pool the disk
// actually lands in.
func TestCreateVM_ContainerNoPool_EnforcesContainerPoolQuota(t *testing.T) {
	existing := models.VM{
		ID: "ct-old", Name: "ct-old", OwnerID: "carol", Type: "container",
		Disks: []models.DiskInfo{{Device: "disk", Target: "root", Pool: "webkvm-incus", SizeGB: 8}},
	}
	h, be := newCreateContainerHarness(t, models.Quota{PoolQuotas: map[string]int{"webkvm-incus": 10}}, existing)

	rec := postCreateVM(t, h, models.CreateVMRequest{
		Name: "ct2", Type: "container", Image: "images:debian/12", DiskGB: 5,
	}, "carol", models.RoleOperator)
	if rec.Code != http.StatusConflict {
		t.Fatalf("over-quota status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if len(be.created) != 0 {
		t.Fatalf("CreateDomain must not run when over quota")
	}

	rec = postCreateVM(t, h, models.CreateVMRequest{
		Name: "ct3", Type: "container", Image: "images:debian/12", DiskGB: 2,
	}, "carol", models.RoleOperator)
	if rec.Code != http.StatusCreated {
		t.Fatalf("within-quota status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}

// No container pool at all: a restricted user must fail closed rather
// than let Incus pick a pool the ACL never saw.
func TestCreateVM_ContainerNoPool_NoContainerPoolFailsClosed(t *testing.T) {
	h, be := newCreateContainerHarness(t, models.Quota{})
	be.pools = be.pools[:1] // only the libvirt disk pool
	rec := postCreateVM(t, h, models.CreateVMRequest{
		Name: "ct4", Type: "container", Image: "images:debian/12",
	}, "carol", models.RoleOperator)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// Admins keep the historical behaviour: no pool is forced on them.
func TestCreateVM_ContainerNoPool_AdminUnchanged(t *testing.T) {
	h, be := newCreateContainerHarness(t, models.Quota{})
	rec := postCreateVM(t, h, models.CreateVMRequest{
		Name: "ct5", Type: "container", Image: "images:debian/12",
	}, "admin", models.RoleAdmin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	if got := be.created[0].StoragePool; got != "" {
		t.Fatalf("admin StoragePool = %q, want empty (Incus resolves it)", got)
	}
}
