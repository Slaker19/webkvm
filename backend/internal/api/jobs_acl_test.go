package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/models"
)

// GetDownloadJob (storage.go) does a bare map lookup by ID and returns
// the record. No owner check, no role check, nothing — and the job
// carries the destination path, the source URL, the pool and the byte
// counts of whatever operation created it.
//
// The IDs are not secret either: they are fmt.Sprintf("vm_%d", UnixNano),
// so a caller who knows roughly when a job ran has a small space to walk.
//
// Confirmed live before writing this: audit-viewer read a job belonging
// to audit-operator (vm_1790349189994823692) with a 200 on both routes
// and got back the internal path, the format and the size.

func peticionJob(h *Handler, id, user, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+id, nil)
	req.Header.Set("X-User", user)
	req.Header.Set("X-Role", role)
	// chi puts URL params in the route context, not in the path.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	h.GetDownloadJob(rr, req)
	return rr
}

// Control: the owner must still be able to poll their own job. A fix
// that breaks this has just swapped one bug for a worse one.
func TestGetDownloadJob_ElDuenoPuedeLeerlo(t *testing.T) {
	resetJobs()
	storeJob(&models.DownloadJob{
		ID: "job-de-alice", Status: "running", Owner: "alice",
		Name: "descarga.iso", Pool: "webkvm-isos",
	})

	rr := peticionJob(&Handler{}, "job-de-alice", "alice", "operator")
	if rr.Code != http.StatusOK {
		t.Fatalf("el dueno debe poder leer su propio job; got %d: %s", rr.Code, rr.Body.String())
	}
}

// EL FALLO: bob pide el job de alice y lo recibe entero.
func TestGetDownloadJob_OtroUsuarioNoDebeLeerlo(t *testing.T) {
	resetJobs()
	storeJob(&models.DownloadJob{
		ID: "job-de-alice", Status: "running", Owner: "alice",
		Name: "privado.iso", Pool: "webkvm-isos",
		URL: "https://interno.example/privado.iso",
	})

	rr := peticionJob(&Handler{}, "job-de-alice", "bob", "viewer")
	if rr.Code == http.StatusOK {
		var j models.DownloadJob
		_ = json.Unmarshal(rr.Body.Bytes(), &j)
		t.Fatalf("bob leyo el job de alice: name=%q pool=%q url=%q", j.Name, j.Pool, j.URL)
	}
	if rr.Code != http.StatusNotFound {
		t.Fatalf("se espera 404 (no 403: un id ajeno no debe confirmarse como existente); got %d", rr.Code)
	}
}

// Un admin sí puede: es quien diagnostica por que una descarga ajena falla.
func TestGetDownloadJob_AdminPuedeLeerlo(t *testing.T) {
	resetJobs()
	storeJob(&models.DownloadJob{ID: "job-de-alice", Status: "running", Owner: "alice"})

	rr := peticionJob(&Handler{}, "job-de-alice", "root", models.RoleAdmin)
	if rr.Code != http.StatusOK {
		t.Fatalf("un admin debe poder leer cualquier job; got %d: %s", rr.Code, rr.Body.String())
	}
}

// Los jobs anteriores a este cambio no tienen Owner. No pueden quedar
// sin acceso para su dueno legitimo durante el despliegue, asi que un
// job sin dueno se sirve a cualquier usuario autenticado, igual que antes.
func TestGetDownloadJob_JobSinDuenoSigueAccesible(t *testing.T) {
	resetJobs()
	storeJob(&models.DownloadJob{ID: "job-viejo", Status: "running"})

	rr := peticionJob(&Handler{}, "job-viejo", "quien-sea", "viewer")
	if rr.Code != http.StatusOK {
		t.Fatalf("un job sin Owner (creado antes del cambio) debe seguir siendo legible; got %d", rr.Code)
	}
}
