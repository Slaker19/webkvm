package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

type mockGuestCompute struct {
	compute.Backend
	vm         models.VM
	execResult compute.GuestExecResult
	execErr    error
	freezeErr  error
	freezeCnt  int
	freezeStat string
	syncErr    error
}

func (m *mockGuestCompute) GetDomain(id string) (models.VM, error) {
	return m.vm, nil
}

func (m *mockGuestCompute) GuestExec(id, path string, args []string, timeoutSec int) (compute.GuestExecResult, error) {
	return m.execResult, m.execErr
}

func (m *mockGuestCompute) FSFreeze(id string, freeze bool) (int, error) {
	return m.freezeCnt, m.freezeErr
}

func (m *mockGuestCompute) FSFreezeStatus(id string) (string, error) {
	return m.freezeStat, nil
}

func (m *mockGuestCompute) GuestSyncTime(id string) error {
	return m.syncErr
}

func TestVMGuestExec(t *testing.T) {
	mock := &mockGuestCompute{
		vm: models.VM{
			ID:         "vm-1",
			State:      models.VMStateRunning,
			Hypervisor: "kvm",
		},
		execResult: compute.GuestExecResult{
			ExitCode: 0,
			Stdout:   "Linux test 6.8.0 #1 SMP",
			Stderr:   "",
			Exited:   true,
		},
	}

	h := &Handler{
		compute: mock,
	}

	body, _ := json.Marshal(GuestExecRequest{Command: "uname -a"})
	req := httptest.NewRequest(http.MethodPost, "/api/vms/vm-1/guest/exec", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "vm-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.VMGuestExec(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res compute.GuestExecResult
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "Linux test 6.8.0 #1 SMP" || res.ExitCode != 0 || !res.Exited {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestVMGuestFSFreezeAndThaw(t *testing.T) {
	mock := &mockGuestCompute{
		vm: models.VM{
			ID:         "vm-1",
			State:      models.VMStateRunning,
			Hypervisor: "kvm",
		},
		freezeCnt:  2,
		freezeStat: "frozen",
	}

	h := &Handler{
		compute: mock,
	}

	// 1. Freeze
	body, _ := json.Marshal(GuestFSFreezeRequest{Freeze: true})
	req := httptest.NewRequest(http.MethodPost, "/api/vms/vm-1/guest/freeze", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "vm-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.VMGuestFSFreeze(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Status
	reqStat := httptest.NewRequest(http.MethodGet, "/api/vms/vm-1/guest/freeze-status", nil)
	reqStat = reqStat.WithContext(context.WithValue(reqStat.Context(), chi.RouteCtxKey, rctx))
	recStat := httptest.NewRecorder()
	h.VMGuestFSFreezeStatus(recStat, reqStat)
	if recStat.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recStat.Code, recStat.Body.String())
	}

	// 3. Sync time
	reqSync := httptest.NewRequest(http.MethodPost, "/api/vms/vm-1/guest/sync-time", nil)
	reqSync = reqSync.WithContext(context.WithValue(reqSync.Context(), chi.RouteCtxKey, rctx))
	recSync := httptest.NewRecorder()
	h.VMGuestSyncTime(recSync, reqSync)
	if recSync.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recSync.Code, recSync.Body.String())
	}
}
