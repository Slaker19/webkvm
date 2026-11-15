package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// volBackend mimics a node with one active pool and a stale inactive one
// (v11 live: webkvm-disks/webkvm-isos left on .10 pointing to /tmp).
type volBackend struct {
	compute.Backend
	listPoolsErr error
}

const rawVirErr = "virError(Code=55, Domain=18, Message='Requested operation is not valid: storage pool 'webkvm-disks' is not active')"

func (b *volBackend) ListStoragePools() ([]models.StoragePool, error) {
	if b.listPoolsErr != nil {
		return nil, b.listPoolsErr
	}
	return []models.StoragePool{
		{Name: "md-1-vdi", State: "active"},
		{Name: "webkvm-disks", State: "inactive"},
		{Name: "broken", State: "active"},
	}, nil
}

func (b *volBackend) ListStorageVolumes(pool string) ([]models.StorageVolume, error) {
	switch pool {
	case "md-1-vdi":
		return []models.StorageVolume{{Name: "a.qcow2", Pool: pool}}, nil
	case "webkvm-disks":
		return nil, errors.New("list volumes: " + rawVirErr)
	case "broken":
		return nil, errors.New("list volumes: virError(Code=38, Message='cannot open directory /x: secret detail')")
	}
	return nil, errors.New("lookup pool: virError(Code=49, Domain=18, Message='Storage pool not found: no storage pool with matching name')")
}

func listVolumes(t *testing.T, h *Handler, query string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/storage/volumes"+query, nil)
	req.Header.Set("X-Role", models.RoleAdmin)
	rec := httptest.NewRecorder()
	h.ListVolumes(rec, req)
	return rec, rec.Body.String()
}

func TestListVolumesWithoutPoolSkipsInactivePools(t *testing.T) {
	h := &Handler{compute: &volBackend{}}
	rec, body := listVolumes(t, h, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("one inactive pool must not fail the listing: %d %s", rec.Code, body)
	}
	var vols []models.StorageVolume
	if err := json.Unmarshal([]byte(body), &vols); err != nil || len(vols) != 1 || vols[0].Name != "a.qcow2" {
		t.Fatalf("vols = %s (%v)", body, err)
	}
	if got := rec.Header().Get("X-WebKVM-Skipped-Pools"); got != "broken,webkvm-disks" {
		t.Fatalf("skipped pools header = %q", got)
	}
	if strings.Contains(body, "virError") || strings.Contains(body, "secret detail") {
		t.Fatalf("raw libvirt error leaked: %s", body)
	}
}

func TestListVolumesNamedPoolErrorsAreTyped(t *testing.T) {
	h := &Handler{compute: &volBackend{}}
	for _, tc := range []struct {
		pool, code string
		status     int
	}{
		{"webkvm-disks", "pool_inactive", http.StatusConflict},
		{"ghost", "pool_not_found", http.StatusNotFound},
		{"broken", "volumes_unavailable", http.StatusBadGateway},
	} {
		rec, body := listVolumes(t, h, "?pool="+tc.pool)
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		if rec.Code != tc.status || m["code"] != tc.code || m["pool"] != tc.pool {
			t.Fatalf("%s: %d %s", tc.pool, rec.Code, body)
		}
		if strings.Contains(body, "virError") || strings.Contains(body, "secret detail") {
			t.Fatalf("%s: raw libvirt error leaked: %s", tc.pool, body)
		}
	}
	if rec, _ := listVolumes(t, h, "?pool=md-1-vdi"); rec.Code != http.StatusOK {
		t.Fatalf("active pool: %d", rec.Code)
	}
	h = &Handler{compute: &volBackend{listPoolsErr: errors.New("virError(Code=1, Message='internal')")}}
	rec, body := listVolumes(t, h, "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(body, "volumes_unavailable") || strings.Contains(body, "virError") {
		t.Fatalf("pool list failure: %d %s", rec.Code, body)
	}
}
