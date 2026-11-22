package api

import (
	"bytes"
	"context"
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

type fakeApplianceBackend struct{ compute.Backend }

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
