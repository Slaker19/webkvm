package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/user"
	"webkvm/internal/vmsched"
)

// fakeSchedBackend tracks start/shutdown calls and returns VM metadata.
type fakeSchedBackend struct {
	compute.Backend
	started  bool
	stopped  bool
	template bool
	owner    string
}

func (b *fakeSchedBackend) StartDomain(id string) error {
	b.started = true
	return nil
}

func (b *fakeSchedBackend) ShutdownDomain(id string) error {
	b.stopped = true
	return nil
}

func (b *fakeSchedBackend) GetDomain(id string) (models.VM, error) {
	return models.VM{
		ID:       id,
		Name:     id,
		Template: b.template,
		OwnerID:  b.owner,
	}, nil
}

func (b *fakeSchedBackend) GetVMMeta(id string) (models.VMMeta, error) {
	return models.VMMeta{OwnerID: b.owner}, nil
}

func boolPtr(v bool) *bool { return &v }

func setupSchedHandler(t *testing.T, be *fakeSchedBackend, perms *models.UserPermissions) (*Handler, *user.Store) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")

	us, err := user.NewStore(dir)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	if _, err := us.Create(models.CreateUserRequest{
		Username:    "op",
		Password:    "Str0ng-Pass#2026",
		Role:        models.RoleOperator,
		Permissions: perms,
	}); err != nil {
		t.Fatalf("create operator: %v", err)
	}

	schedStore := vmsched.NewStore(dir)
	scheduler := vmsched.NewScheduler(schedStore, func(vmID, action string) error {
		return nil
	}, func(vmID string, maxKeep int) error {
		return nil
	}, nil)

	h := &Handler{
		userStore:    us,
		compute:      be,
		vmSchedStore: schedStore,
		vmScheduler:  scheduler,
	}
	return h, us
}

func schedRequest(method, path, user, role string, body any, params map[string]string) *http.Request {
	var bodyReader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("X-User", user)
	req.Header.Set("X-Role", role)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// A-1: Un operador con can_control_power=false NO debe poder encender ni
// apagar una VM a traves de PowerVMNow (/power/{action}).
func TestPowerVMNow_OperadorSinPermiso_Bloqueado(t *testing.T) {
	be := &fakeSchedBackend{owner: "op"}
	h, _ := setupSchedHandler(t, be, &models.UserPermissions{CanControlPower: boolPtr(false)})

	for _, action := range []string{"start", "stop"} {
		t.Run(action, func(t *testing.T) {
			be.started = false
			be.stopped = false

			req := schedRequest(http.MethodPost, "/api/vms/vm1/power/"+action, "op", models.RoleOperator, nil, map[string]string{
				"id":     "vm1",
				"action": action,
			})
			rr := httptest.NewRecorder()
			h.PowerVMNow(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Fatalf("action=%s: se esperaba 403 Forbidden para operador sin control_power; got %d (body=%s)",
					action, rr.Code, rr.Body.String())
			}
			if be.started || be.stopped {
				t.Fatalf("action=%s: la VM fue arrancada/parada a pesar de la falta de permisos", action)
			}
		})
	}
}

// A-1: PowerVMNow con "start" debe invocar checkStartQuota, bloqueando el arranque
// si la VM es una plantilla.
func TestPowerVMNow_BloqueaArranqueDePlantillas(t *testing.T) {
	be := &fakeSchedBackend{owner: "op", template: true}
	h, _ := setupSchedHandler(t, be, &models.UserPermissions{CanControlPower: boolPtr(true)})

	req := schedRequest(http.MethodPost, "/api/vms/tpl1/power/start", "op", models.RoleOperator, nil, map[string]string{
		"id":     "tpl1",
		"action": "start",
	})
	rr := httptest.NewRecorder()
	h.PowerVMNow(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("arrancar una plantilla por PowerVMNow deberia fallar con 409 Conflict (checkStartQuota); got %d: %s",
			rr.Code, rr.Body.String())
	}
	if be.started {
		t.Fatalf("la plantilla fue arrancada por PowerVMNow")
	}
}

// Control: un operador con control_power=true si debe poder usar PowerVMNow.
func TestPowerVMNow_OperadorConPermiso_Funciona(t *testing.T) {
	be := &fakeSchedBackend{owner: "op"}
	h, _ := setupSchedHandler(t, be, &models.UserPermissions{CanControlPower: boolPtr(true)})

	req := schedRequest(http.MethodPost, "/api/vms/vm1/power/start", "op", models.RoleOperator, nil, map[string]string{
		"id":     "vm1",
		"action": "start",
	})
	rr := httptest.NewRecorder()
	h.PowerVMNow(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("operador con control_power deberia recibir 200; got %d: %s", rr.Code, rr.Body.String())
	}
	if !be.started {
		t.Fatalf("la VM no fue arrancada")
	}
}

// A-2: SetVMSchedule sin control_power debe rechazar programar start/stop.
func TestSetVMSchedule_SinControlPower_BloqueaCronEncendido(t *testing.T) {
	be := &fakeSchedBackend{owner: "op"}
	h, _ := setupSchedHandler(t, be, &models.UserPermissions{
		CanControlPower: boolPtr(false),
		CanSnapshots:    boolPtr(true),
	})

	body := map[string]any{
		"start_cron": "0 8 * * *",
	}
	req := schedRequest(http.MethodPut, "/api/vms/vm1/schedule", "op", models.RoleOperator, body, map[string]string{
		"id": "vm1",
	})
	rr := httptest.NewRecorder()
	h.SetVMSchedule(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("programar start_cron sin can_control_power deberia dar 403; got %d: %s",
			rr.Code, rr.Body.String())
	}
}

// A-2: SetVMSchedule sin snapshots debe rechazar programar snapshots automaticos.
func TestSetVMSchedule_SinSnapshots_BloqueaCronSnapshots(t *testing.T) {
	be := &fakeSchedBackend{owner: "op"}
	h, _ := setupSchedHandler(t, be, &models.UserPermissions{
		CanControlPower: boolPtr(true),
		CanSnapshots:    boolPtr(false),
	})

	body := map[string]any{
		"snapshot_cron": "0 2 * * *",
		"snapshot_max":  5,
	}
	req := schedRequest(http.MethodPut, "/api/vms/vm1/schedule", "op", models.RoleOperator, body, map[string]string{
		"id": "vm1",
	})
	rr := httptest.NewRecorder()
	h.SetVMSchedule(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("programar snapshot_cron sin can_snapshots deberia dar 403; got %d: %s",
			rr.Code, rr.Body.String())
	}
}
