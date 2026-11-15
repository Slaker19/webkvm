package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

type getErrBackend struct {
	compute.Backend
	err error
}

func (g *getErrBackend) GetDomain(string) (models.VM, error) { return models.VM{}, g.err }

func getVMVia(t *testing.T, err error) *httptest.ResponseRecorder {
	t.Helper()
	h := &Handler{compute: &getErrBackend{err: err}, remote: &remoteTarget{node: nodes.Node{ID: "n1", Name: "lab"}}}
	rt := chi.NewRouter()
	rt.Get("/api/vms/{id}", h.GetVM)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/vms/nope", nil)
	req.Header.Set("X-Role", models.RoleAdmin)
	rt.ServeHTTP(rr, req)
	return rr
}

// A VM that does not exist on a REMOTE node is a 404, not a 500.
func TestGetVMRemoteDomainNotFoundIs404(t *testing.T) {
	rr := getVMVia(t, errors.New("domain not found: virError(Code=42, Domain=10, Message='Domain not found')"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := getVMVia(t, errors.New("connection reset by peer")); rr.Code != http.StatusInternalServerError {
		t.Fatalf("real failures stay 500: %d", rr.Code)
	}
}
