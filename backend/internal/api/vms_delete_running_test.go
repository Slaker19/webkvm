package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

type runningDeleteBackend struct {
	compute.Backend
	vm        models.VM
	deleteErr error
	deleted   bool
}

func (b *runningDeleteBackend) GetDomain(string) (models.VM, error) { return b.vm, nil }
func (b *runningDeleteBackend) DeleteDomain(string) error {
	b.deleted = true
	return b.deleteErr
}

func deleteReq(id, query string) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, "/api/vms/"+id+query, nil)
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", string(models.RoleAdmin))
	return withVMID(req, id)
}

func maintWindow() string {
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	return `<nvpair name="is-managed" value="false"/><nvpair name="webkvm-maint-until" value="` + until + `"/>`
}

func TestDeleteRunningHAWorkloadInMaintenanceIs409(t *testing.T) {
	cases := []struct{ name, id, query string }{
		{"incus", "c1", "?hv=incus"},
		{"kvm", "web", "?hv=kvm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useRunner(t, &wlRunner{hasCP: true, vm: true, started: true, inc: true, incStarted: true, maint: maintWindow()})
			h := haModuleHandler(t)
			be := &runningDeleteBackend{vm: models.VM{ID: tc.id, Name: tc.id, State: models.VMStateRunning}}
			h.compute = be
			rec := httptest.NewRecorder()
			h.DeleteVM(rec, deleteReq(tc.id, tc.query))
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"instance_running"`) {
				t.Fatalf("want 409 instance_running, got %d %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(strings.ToLower(rec.Body.String()), "stop") || be.deleted {
				t.Fatalf("message must say to stop it first and nothing may be deleted: %s deleted=%v", rec.Body.String(), be.deleted)
			}
		})
	}
}

func TestDeleteStoppedHAWorkloadInMaintenanceStillWorks(t *testing.T) {
	useRunner(t, &wlRunner{hasCP: true, inc: true, incStarted: true, maint: maintWindow()})
	h := haModuleHandler(t)
	be := &runningDeleteBackend{vm: models.VM{ID: "c1", Name: "c1", State: models.VMStateShutoff}}
	h.compute = be
	rec := httptest.NewRecorder()
	h.DeleteVM(rec, deleteReq("c1", "?hv=incus"))
	if rec.Code != http.StatusOK || !be.deleted {
		t.Fatalf("stopped instance must be deletable: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteIncusRunningErrorMapsTo409(t *testing.T) {
	h := &Handler{compute: &runningDeleteBackend{
		vm:        models.VM{ID: "c2", Name: "c2", State: models.VMStateRunning},
		deleteErr: errors.New("Failed deleting instance \"c2\": Instance is running"),
	}}
	rec := httptest.NewRecorder()
	h.DeleteVM(rec, deleteReq("c2", ""))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "instance_running") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
