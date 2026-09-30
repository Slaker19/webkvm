package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/zvol"
)

type zvolTestBackend struct {
	compute.Backend
	vms      []models.VM
	attached []models.AttachDiskRequest
}

func (b *zvolTestBackend) ListDomains() ([]models.VM, error) { return b.vms, nil }
func (b *zvolTestBackend) AttachDisk(id string, req models.AttachDiskRequest) error {
	b.attached = append(b.attached, req)
	return nil
}

func stubZVols(t *testing.T, vols map[string]zvol.Volume) {
	t.Helper()
	origGet, origList := zvolGet, zvolList
	zvolGet = func(_ context.Context, name string) (zvol.Volume, error) {
		if err := zvol.ValidateName(name); err != nil {
			return zvol.Volume{}, err
		}
		v, ok := vols[name]
		if !ok {
			return zvol.Volume{}, zvol.ErrNotVolume
		}
		return v, nil
	}
	zvolList = func(context.Context) ([]zvol.Volume, error) {
		out := []zvol.Volume{}
		for _, v := range vols {
			out = append(out, v)
		}
		return out, nil
	}
	t.Cleanup(func() { zvolGet, zvolList = origGet, origList })
}

func attachZVolRequest(role, vmID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/vms/"+vmID+"/disks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "u1")
	req.Header.Set("X-Role", role)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", vmID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestCreateDisk_ZVol(t *testing.T) {
	stubZVols(t, map[string]zvol.Volume{
		"tank/empty": {Name: "tank/empty", Dev: "/dev/zvol/tank/empty"},
		"tank/full":  {Name: "tank/full", Dev: "/dev/zvol/tank/full", HasData: true, WrittenBytes: 5 << 30},
		"tank/used":  {Name: "tank/used", Dev: "/dev/zvol/tank/used"},
	})
	other := models.VM{ID: "vm-other", Name: "other", Disks: []models.DiskInfo{
		{Device: "disk", Type: "block", BlockDev: "/dev/zvol/tank/used"},
	}}

	cases := []struct {
		name, role, body string
		want             int
	}{
		{"admin attaches empty zvol", models.RoleAdmin, `{"zvol":"tank/empty"}`, http.StatusOK},
		{"non-admin refused", models.RoleOperator, `{"zvol":"tank/empty"}`, http.StatusForbidden},
		{"path traversal refused", models.RoleAdmin, `{"zvol":"tank/../../sda"}`, http.StatusBadRequest},
		{"not a volume", models.RoleAdmin, `{"zvol":"tank/missing"}`, http.StatusBadRequest},
		{"cdrom refused", models.RoleAdmin, `{"zvol":"tank/empty","device":"cdrom"}`, http.StatusBadRequest},
		{"with size refused", models.RoleAdmin, `{"zvol":"tank/empty","size_gb":10}`, http.StatusBadRequest},
		{"has data refused", models.RoleAdmin, `{"zvol":"tank/full"}`, http.StatusConflict},
		{"has data forced", models.RoleAdmin, `{"zvol":"tank/full","force":true}`, http.StatusOK},
		{"in use by other VM", models.RoleAdmin, `{"zvol":"tank/used","force":true}`, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be := &zvolTestBackend{vms: []models.VM{other}}
			h := &Handler{compute: be}
			rr := httptest.NewRecorder()
			h.CreateDisk(rr, attachZVolRequest(tc.role, "vm1", tc.body))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", rr.Code, tc.want, rr.Body.String())
			}
			if tc.want == http.StatusOK {
				if len(be.attached) != 1 || be.attached[0].ZVol == "" || be.attached[0].Device != "disk" {
					t.Fatalf("backend attach not called as expected: %+v", be.attached)
				}
			} else if len(be.attached) != 0 {
				t.Fatalf("backend attach must not run on refusal: %+v", be.attached)
			}
		})
	}
}

func TestListZVols_ReportsAttachment(t *testing.T) {
	stubZVols(t, map[string]zvol.Volume{
		"tank/used": {Name: "tank/used", Dev: "/dev/zvol/tank/used"},
	})
	be := &zvolTestBackend{vms: []models.VM{{ID: "id1", Name: "web", Disks: []models.DiskInfo{
		{Device: "disk", Type: "block", BlockDev: "/dev/zvol/tank/used"},
	}}}}
	h := &Handler{compute: be}
	rr := httptest.NewRecorder()
	h.ListZVols(rr, httptest.NewRequest(http.MethodGet, "/api/host/zvols", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var got []ZVolInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AttachedVMName != "web" || got[0].Name != "tank/used" {
		t.Fatalf("unexpected %+v", got)
	}
}

func TestCountsAgainstQuota_SkipsBlockDisks(t *testing.T) {
	if countsAgainstQuota(models.DiskInfo{Device: "disk", Type: "block", SizeGB: 100}) {
		t.Error("zvol block disk must not be billed to the owner's quota")
	}
	if !countsAgainstQuota(models.DiskInfo{Device: "disk", Type: "file", SizeGB: 10}) {
		t.Error("file disk must still count")
	}
}
