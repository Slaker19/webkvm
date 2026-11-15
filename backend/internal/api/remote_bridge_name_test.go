package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

type attachRecorder struct {
	compute.Backend
	got []string
}

func (a *attachRecorder) AttachNetworkIface(_ string, req models.AttachNetRequest) error {
	a.got = append(a.got, req.Network)
	return nil
}

// On a remote node the NIC bridge is a free-text name: a valid one reaches
// the backend, an invalid one is a typed 400 before anything is attempted.
func TestRemoteNICAttachValidatesBridgeName(t *testing.T) {
	be := &attachRecorder{}
	h := &Handler{compute: be, remote: &remoteTarget{node: nodes.Node{ID: "n1", Name: "lab"}}}
	rt := chi.NewRouter()
	rt.Post("/api/vms/{id}/networks", h.CreateNetIface)
	post := func(network string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/vms/vm1/networks", strings.NewReader(`{"network":"`+network+`"}`))
		req.Header.Set("X-Role", models.RoleAdmin)
		rr := httptest.NewRecorder()
		rt.ServeHTTP(rr, req)
		return rr
	}
	if rr := post("vmbr0"); rr.Code != http.StatusOK || len(be.got) != 1 {
		t.Fatalf("valid bridge: %d %s", rr.Code, rr.Body.String())
	}
	for _, bad := range []string{"vm br", "a;b", "abcdefghijklmnop", "a..b"} {
		rr := post(bad)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid_bridge") {
			t.Fatalf("%q: %d %s", bad, rr.Code, rr.Body.String())
		}
	}
	if len(be.got) != 1 {
		t.Fatalf("invalid names reached the backend: %v", be.got)
	}
}
