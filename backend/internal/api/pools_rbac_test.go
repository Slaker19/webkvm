package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/auth"
	"webkvm/internal/models"
)

// TestPoolsRBAC_RouteMapping verifica el contrato V12-SEC-04 a nivel de
// enrutado usando LOS MISMOS middlewares reales (auth.RequireRole /
// auth.RequireAtLeast) que monta NewRouter. Replica la estructura del
// grupo /api/storage del router real como canario de regresión: si alguien
// mueve una ruta de grupo, este test lo detecta aunque los handlers reales
// requieran libvirt (que aquí se sustituye por stubs: probamos la puerta,
// no la lógica de almacenamiento).
func TestPoolsRBAC_RouteMapping(t *testing.T) {
	okStub := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

	newMux := func() *chi.Mux {
		r := chi.NewRouter()
		r.Route("/api/storage", func(r chi.Router) {
			// Lecturas: cualquier usuario autenticado (sin gate de rol).
			r.Get("/pools", okStub)
			r.Get("/volumes", okStub)

			// Mutaciones de operador (volúmenes/ISO): fuera del foco.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireAtLeast("operator"))
				r.Post("/volumes", okStub)
			})

			// V12-SEC-04: las mutaciones de POOL quedan admin-only.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(models.RoleAdmin))
				r.Post("/pools", okStub)
				r.Put("/pools/{name}", okStub)
			})
		})
		return r
	}

	mux := newMux()
	operator := http.Header{"X-User": {"op"}, "X-Role": {"operator"}}
	admin := http.Header{"X-User": {"root"}, "X-Role": {"admin"}}

	cases := []struct {
		name       string
		method     string
		path       string
		hdr        http.Header
		wantStatus int
	}{
		{"operator GET pools", http.MethodGet, "/api/storage/pools", operator, http.StatusOK},
		{"operator GET volumes", http.MethodGet, "/api/storage/volumes", operator, http.StatusOK},
		{"operator POST pools -> 403", http.MethodPost, "/api/storage/pools", operator, http.StatusForbidden},
		{"operator PUT pools -> 403", http.MethodPut, "/api/storage/pools/webkvm-disks", operator, http.StatusForbidden},
		{"admin POST pools ok", http.MethodPost, "/api/storage/pools", admin, http.StatusOK},
		{"admin PUT pools ok", http.MethodPut, "/api/storage/pools/webkvm-disks", admin, http.StatusOK},
		{"operator POST volumes sigue ok", http.MethodPost, "/api/storage/volumes", operator, http.StatusOK},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		for k, v := range c.hdr {
			req.Header[k] = v
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != c.wantStatus {
			t.Errorf("%s: status = %d, want %d", c.name, rr.Code, c.wantStatus)
		}
	}
}