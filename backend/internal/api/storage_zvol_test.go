package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

type mockZVolBackend struct {
	compute.Backend
	attached     []models.AttachDiskRequest
	zvols        []models.HostZVol
	zvolAtts     map[string][]models.VolumeAttachment
	domainDisks  map[string][]models.DiskInfo
	domainOwners map[string]string
}

func (m *mockZVolBackend) AttachDisk(id string, req models.AttachDiskRequest) error {
	m.attached = append(m.attached, req)
	return nil
}

func (m *mockZVolBackend) FindZVolAttachments(zvolName string) ([]models.VolumeAttachment, error) {
	if m.zvolAtts == nil {
		return nil, nil
	}
	return m.zvolAtts[zvolName], nil
}

func (m *mockZVolBackend) GetDomain(id string) (models.VM, error) {
	return models.VM{
		ID:    id,
		Name:  "test-vm",
		Disks: m.domainDisks[id],
	}, nil
}

func (m *mockZVolBackend) GetVMMeta(id string) (models.VMMeta, error) {
	owner := "admin"
	if m.domainOwners != nil && m.domainOwners[id] != "" {
		owner = m.domainOwners[id]
	}
	return models.VMMeta{OwnerID: owner}, nil
}

func (m *mockZVolBackend) ListDomains() ([]models.VM, error) {
	return []models.VM{
		{ID: "vm-1", Name: "vm-1", Disks: m.domainDisks["vm-1"]},
		{ID: "vm-2", Name: "vm-2", Disks: m.domainDisks["vm-2"]},
	}, nil
}

func TestAttachDisk_ZVol(t *testing.T) {
	tmp := t.TempDir()
	auditLogger, err := audit.New(tmp + "/audit.log")
	if err != nil {
		t.Fatal(err)
	}
	mock := &mockZVolBackend{
		zvolAtts: map[string][]models.VolumeAttachment{
			"tank/in-use-vol": {
				{
					VMID:   "vm-other",
					VMName: "other-vm",
					Target: "vda",
				},
			},
		},
	}
	h := &Handler{
		compute: mock,
		cfg:     &config.Config{DataDir: tmp},
		audit:   auditLogger,
	}

	tests := []struct {
		name       string
		role       string
		body       models.AttachDiskRequest
		wantStatus int
	}{
		{
			name: "forbidden for non-admin user",
			role: models.RoleOperator,
			body: models.AttachDiskRequest{
				Device: "disk",
				Bus:    "virtio",
				ZVol:   "tank/vm-disk-1",
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "bad request for invalid zvol name with shell chars",
			role: models.RoleAdmin,
			body: models.AttachDiskRequest{
				Device: "disk",
				Bus:    "virtio",
				ZVol:   "tank/vol;rm -rf",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "conflict when zvol is already attached to another VM",
			role: models.RoleAdmin,
			body: models.AttachDiskRequest{
				Device: "disk",
				Bus:    "virtio",
				ZVol:   "tank/in-use-vol",
			},
			wantStatus: http.StatusConflict,
		},
		{
			name: "bad request when both source and zvol are provided",
			role: models.RoleAdmin,
			body: models.AttachDiskRequest{
				Device: "disk",
				Bus:    "virtio",
				Source: "/some/path.qcow2",
				ZVol:   "tank/vol-1",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "bad request when zvol attached as cdrom",
			role: models.RoleAdmin,
			body: models.AttachDiskRequest{
				Device: "cdrom",
				Bus:    "sata",
				ZVol:   "tank/vol-1",
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.body)
			r := httptest.NewRequest(http.MethodPost, "/api/vms/vm-1/disks", bytes.NewReader(b))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-User", "tester")
			r.Header.Set("X-Role", tc.role)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", "vm-1")
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			h.CreateDisk(w, r)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

func TestListHostZVols(t *testing.T) {
	tmp := t.TempDir()
	auditLogger, err := audit.New(tmp + "/audit.log")
	if err != nil {
		t.Fatal(err)
	}
	mock := &mockZVolBackend{
		zvolAtts: map[string][]models.VolumeAttachment{
			"tank/vm-disk-1": {
				{
					VMID:   "vm-1",
					VMName: "vm-1",
					Target: "vda",
				},
			},
		},
	}
	h := &Handler{
		compute: mock,
		cfg:     &config.Config{DataDir: tmp},
		audit:   auditLogger,
	}

	r := httptest.NewRequest(http.MethodGet, "/api/host/zvols", nil)
	r.Header.Set("X-User", "admin")
	r.Header.Set("X-Role", models.RoleAdmin)

	w := httptest.NewRecorder()
	h.ListHostZVols(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
}

func TestListHostZPools(t *testing.T) {
	tmp := t.TempDir()
	auditLogger, err := audit.New(tmp + "/audit.log")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		cfg:   &config.Config{DataDir: tmp},
		audit: auditLogger,
	}

	r := httptest.NewRequest(http.MethodGet, "/api/host/zpools", nil)
	r.Header.Set("X-User", "admin")
	r.Header.Set("X-Role", models.RoleAdmin)

	w := httptest.NewRecorder()
	h.ListHostZPools(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
}

func TestCreateHostZPool_Validation(t *testing.T) {
	tmp := t.TempDir()
	auditLogger, err := audit.New(tmp + "/audit.log")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		cfg:   &config.Config{DataDir: tmp},
		audit: auditLogger,
	}

	cases := []struct {
		name       string
		body       models.CreateZPoolRequest
		wantStatus int
	}{
		{
			name:       "empty name",
			body:       models.CreateZPoolRequest{Name: "", Topology: "stripe", Devices: []string{"/dev/sdb"}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid name with slashes",
			body:       models.CreateZPoolRequest{Name: "pool/sub", Topology: "stripe", Devices: []string{"/dev/sdb"}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "reserved keyword name",
			body:       models.CreateZPoolRequest{Name: "mirror", Topology: "stripe", Devices: []string{"/dev/sdb"}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty devices",
			body:       models.CreateZPoolRequest{Name: "mypool", Topology: "stripe", Devices: []string{}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid disk path injection",
			body:       models.CreateZPoolRequest{Name: "mypool", Topology: "stripe", Devices: []string{"/dev/sdb; rm -rf"}},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := json.Marshal(c.body)
			r := httptest.NewRequest(http.MethodPost, "/api/host/zpools", bytes.NewReader(b))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-User", "admin")
			r.Header.Set("X-Role", models.RoleAdmin)

			w := httptest.NewRecorder()
			h.CreateHostZPool(w, r)

			if w.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, c.wantStatus, w.Body.String())
			}
		})
	}
}

func TestCreateHostZVol_Validation(t *testing.T) {
	tmp := t.TempDir()
	auditLogger, err := audit.New(tmp + "/audit.log")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		cfg:   &config.Config{DataDir: tmp},
		audit: auditLogger,
	}

	cases := []struct {
		name       string
		body       models.CreateZVolRequest
		wantStatus int
	}{
		{
			name:       "invalid pool name",
			body:       models.CreateZVolRequest{Pool: "invalid/pool", Name: "vol1", SizeGB: 10},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid vol name with spaces",
			body:       models.CreateZVolRequest{Pool: "tank", Name: "vol 1", SizeGB: 10},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "zero size",
			body:       models.CreateZVolRequest{Pool: "tank", Name: "vol1", SizeGB: 0},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := json.Marshal(c.body)
			r := httptest.NewRequest(http.MethodPost, "/api/host/zvols", bytes.NewReader(b))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-User", "admin")
			r.Header.Set("X-Role", models.RoleAdmin)

			w := httptest.NewRecorder()
			h.CreateHostZVol(w, r)

			if w.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, c.wantStatus, w.Body.String())
			}
		})
	}
}

func TestCreateHostRAID_Validation(t *testing.T) {
	tmp := t.TempDir()
	auditLogger, err := audit.New(tmp + "/audit.log")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		cfg:   &config.Config{DataDir: tmp},
		audit: auditLogger,
	}

	cases := []struct {
		name       string
		body       models.CreateRAIDRequest
		wantStatus int
	}{
		{
			name:       "empty devices",
			body:       models.CreateRAIDRequest{Level: "1", Devices: []string{}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid disk path",
			body:       models.CreateRAIDRequest{Level: "1", Devices: []string{"/dev/sdb; rm -rf", "/dev/sdc"}},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := json.Marshal(c.body)
			r := httptest.NewRequest(http.MethodPost, "/api/host/raid", bytes.NewReader(b))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-User", "admin")
			r.Header.Set("X-Role", models.RoleAdmin)

			w := httptest.NewRecorder()
			h.CreateHostRAID(w, r)

			if w.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, c.wantStatus, w.Body.String())
			}
		})
	}
}
