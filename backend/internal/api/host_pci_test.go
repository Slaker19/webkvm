package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/models"
)

type mockPCIBoundBackend struct {
	compute.Backend
	boundAddress   string
	unboundAddress string
}

func (m *mockPCIBoundBackend) BindHostPCIDeviceVFIO(address string) error {
	m.boundAddress = address
	return nil
}

func (m *mockPCIBoundBackend) UnbindHostPCIDeviceVFIO(address string) error {
	m.unboundAddress = address
	return nil
}

func (m *mockPCIBoundBackend) ListHostPCIDevices() ([]models.PCIIOMMUGroup, error) {
	return []models.PCIIOMMUGroup{
		{
			Group: 1,
			Devices: []models.PCIDevice{
				{
					Name:        "Test GPU",
					Address:     "0000:01:00.0",
					VendorID:    "10de",
					ProductID:   "2204",
					Driver:      "vfio-pci",
					VFIOBound:   true,
					Assignable:  true,
					IOMMUGroup:  1,
				},
			},
			AllVFIOBound: true,
			Assignable:   true,
		},
	}, nil
}

func (m *mockPCIBoundBackend) GetHostPCIPreflight() (models.PCIPreflightInfo, error) {
	return models.PCIPreflightInfo{
		IOMMUEnabled:     true,
		IOMMUVendor:      "intel",
		IOMMUMode:        "passthrough",
		VFIOModuleLoaded: true,
		VFIOAvailable:    true,
	}, nil
}

func TestHostPCIHandlers(t *testing.T) {
	mock := &mockPCIBoundBackend{}
	aud, err := audit.New(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{compute: mock, audit: aud}

	// 1. List PCI devices
	req := httptest.NewRequest(http.MethodGet, "/api/host/pci-devices", nil)
	rec := httptest.NewRecorder()
	h.ListHostPCIDevices(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListHostPCIDevices expected 200, got %d", rec.Code)
	}

	// 2. Preflight
	req = httptest.NewRequest(http.MethodGet, "/api/host/pci-preflight", nil)
	rec = httptest.NewRecorder()
	h.GetHostPCIPreflight(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetHostPCIPreflight expected 200, got %d", rec.Code)
	}

	// 3. Bind VFIO
	rCtx := chi.NewRouteContext()
	rCtx.URLParams.Add("address", "0000:01:00.0")
	req = httptest.NewRequest(http.MethodPost, "/api/host/pci-devices/0000:01:00.0/bind-vfio", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rCtx))
	rec = httptest.NewRecorder()
	h.BindHostPCIDeviceVFIO(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("BindHostPCIDeviceVFIO expected 200, got %d", rec.Code)
	}
	if mock.boundAddress != "0000:01:00.0" {
		t.Fatalf("expected bound address 0000:01:00.0, got %s", mock.boundAddress)
	}

	// 4. Unbind VFIO
	rCtx2 := chi.NewRouteContext()
	rCtx2.URLParams.Add("address", "0000:01:00.0")
	req = httptest.NewRequest(http.MethodPost, "/api/host/pci-devices/0000:01:00.0/unbind-vfio", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rCtx2))
	rec = httptest.NewRecorder()
	h.UnbindHostPCIDeviceVFIO(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("UnbindHostPCIDeviceVFIO expected 200, got %d", rec.Code)
	}
	if mock.unboundAddress != "0000:01:00.0" {
		t.Fatalf("expected unbound address 0000:01:00.0, got %s", mock.unboundAddress)
	}
}
