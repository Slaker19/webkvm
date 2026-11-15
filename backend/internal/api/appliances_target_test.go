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
