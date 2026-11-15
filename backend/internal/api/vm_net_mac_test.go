package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

type macCaptureBackend struct {
	compute.Backend
	got string
}

func (b *macCaptureBackend) UpdateNetworkIface(_ string, oldMAC string, _ models.UpdateNetIfaceRequest) error {
	b.got = oldMAC
	return nil
}

func TestUpdateNetIfaceDecodesURLEncodedMAC(t *testing.T) {
	be := &macCaptureBackend{}
	h := &Handler{compute: be}
	req := httptest.NewRequest(http.MethodPatch, "/api/vms/vm1/networks/52%3A54%3A00%3Aaa%3Abb%3Acc",
		strings.NewReader(`{"link_state":"down"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", "vm1")
	ctx.URLParams.Add("mac", "52%3A54%3A00%3Aaa%3Abb%3Acc")
	req = req.WithContext(contextWithRoute(req, ctx))
	rec := httptest.NewRecorder()

	h.UpdateNetIface(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if be.got != "52:54:00:aa:bb:cc" {
		t.Fatalf("MAC = %q, want decoded 52:54:00:aa:bb:cc", be.got)
	}
}
