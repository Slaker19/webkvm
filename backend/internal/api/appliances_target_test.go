package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/appliances"
	"webkvm/internal/compute"
	"webkvm/internal/models"
)

func TestDeployApplianceRejectsRemoteNodeInsteadOfSilentlyDeployingLocally(t *testing.T) {
	h := &Handler{
		compute:  &fakeApplianceBackend{},
		appStore: appliances.NewStore(t.TempDir()),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/appliances/deploy", bytes.NewBufferString(
		`{"name":"remote-app","type":"container","node":"remote-kvm"}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", appliances.Defaults[0].ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	rec := httptest.NewRecorder()

	h.DeployAppliance(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "appliance_remote_unsupported") {
		t.Fatalf("response lacks stable unsupported code: %s", rec.Body.String())
	}
}

type fakeApplianceBackend struct {
	compute.Backend
	poolPath string
}

// ListStoragePools answers with one active disk pool so the deploy
// pre-flight can run without a live libvirt.
func (f *fakeApplianceBackend) ListStoragePools() ([]models.StoragePool, error) {
	return []models.StoragePool{{Name: "webkvm-disks", State: "active"}}, nil
}

// GetDomain reports no existing instance, so the deploy target is free.
func (f *fakeApplianceBackend) GetDomain(id string) (models.VM, error) {
	return models.VM{}, errors.New("not found")
}

// DomainExists mirrors GetDomain for the name-collision pre-flight.
func (f *fakeApplianceBackend) DomainExists(name string) (bool, error) {
	return false, nil
}

// GetPoolPath resolves the fake pool to a scratch directory.
func (f *fakeApplianceBackend) GetPoolPath(name string) (string, error) {
	return f.poolPath, nil
}

// VolumeExists reports no existing volume, so the deploy target is free.
func (f *fakeApplianceBackend) VolumeExists(pool, name string) (bool, error) {
	return false, nil
}

// Issue found in the v0.1.14 catalog audit: a container deploy silently
// became a KVM VM when Incus was not installed, and a disk-image
// appliance (cloud/NAS/router entry) deployed as "container" fell back
// to a generic Ubuntu container under the appliance's name. Both must
// fail loudly instead.
func TestDeployApplianceContainerWithoutIncusFailsLoudly(t *testing.T) {
	h := &Handler{
		compute:  &fakeApplianceBackend{},
		appStore: appliances.NewStore(t.TempDir()),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/appliances/deploy", bytes.NewBufferString(
		`{"name":"ct-app","type":"container"}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", "pihole")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	rec := httptest.NewRecorder()

	h.DeployAppliance(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Incus") {
		t.Fatalf("response must name Incus: %s", rec.Body.String())
	}
}

func TestDeployApplianceDiskImageAsContainerIsRejected(t *testing.T) {
	h := &Handler{
		compute:  compute.NewCombined(&fakeApplianceBackend{}, &fakeApplianceBackend{}),
		appStore: appliances.NewStore(t.TempDir()),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/appliances/deploy", bytes.NewBufferString(
		`{"name":"nas-ct","type":"container"}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", "haos")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	rec := httptest.NewRecorder()

	h.DeployAppliance(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "KVM VM") {
		t.Fatalf("response must point at KVM: %s", rec.Body.String())
	}
}

// Council r6: the deploy job silently discarded req.CloudInit for
// appliances without cloud-init support — an operator who typed
// credentials for OPNsense booted a box with the vendor default login
// (admin/opnsense) believing they had set a password. The request must
// be refused instead.
func TestDeployApplianceCloudInitForUnsupportedEntryRejected(t *testing.T) {
	h := &Handler{
		compute:  compute.NewCombined(&fakeApplianceBackend{poolPath: t.TempDir()}, &fakeApplianceBackend{poolPath: t.TempDir()}),
		appStore: appliances.NewStore(t.TempDir()),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/appliances/deploy", bytes.NewBufferString(
		`{"name":"fw","type":"vm","cloud_init":{"user":"me","password":"Secret1!pass"}}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", "haos")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	rec := httptest.NewRecorder()

	h.DeployAppliance(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cloud-init") {
		t.Fatalf("response must explain the cloud-init refusal: %s", rec.Body.String())
	}
}
