package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/models"
)

type updateDiskBackend struct {
	*probeMockBackend
	updated []string
}

func (b *updateDiskBackend) UpdateDiskSource(id, dev, src string) error {
	b.updated = append(b.updated, id+":"+dev+":"+src)
	return nil
}

func callUpdateDisk(h *Handler, vm, user, role, source string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"source": source})
	req := httptest.NewRequest(http.MethodPut, "/api/vms/"+vm+"/disks/vda", bytes.NewReader(body))
	req.Header.Set("X-User", user)
	req.Header.Set("X-Role", role)
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", vm)
	rctx.URLParams.Add("dev", "vda")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.UpdateDisk(rec, req)
	return rec
}

func TestUpdateDisk_GateA(t *testing.T) {
	h, diskA, diskB := setupProbeTest(t)
	be := &updateDiskBackend{probeMockBackend: h.compute.(*probeMockBackend)}
	h.compute = be
	allowed := []string{"poolA", "poolB"}
	_, _ = h.userStore.Update("bob", models.UpdateUserRequest{AllowedPools: &allowed})

	// Non-admin, pool outside their ACL.
	if rec := callUpdateDisk(h, "vm-alice", "alice", models.RoleOperator, diskB); rec.Code != http.StatusForbidden {
		t.Fatalf("pool outside ACL: got %d (%s)", rec.Code, rec.Body.String())
	}
	// Non-admin, non-regular file.
	if rec := callUpdateDisk(h, "vm-alice", "alice", models.RoleOperator, "/dev/null"); rec.Code != http.StatusForbidden {
		t.Fatalf("/dev/null: got %d", rec.Code)
	}
	// Admin: still refuses non-regular files.
	if rec := callUpdateDisk(h, "vm-alice", "admin", models.RoleAdmin, "/dev/null"); rec.Code != http.StatusForbidden {
		t.Fatalf("admin /dev/null: got %d", rec.Code)
	}
	// Image attached to another VM: same 409 as CreateDisk (any role).
	if rec := callUpdateDisk(h, "vm-bob", "bob", models.RoleOperator, diskA); rec.Code != http.StatusConflict {
		t.Fatalf("attached elsewhere: got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := callUpdateDisk(h, "vm-bob", "admin", models.RoleAdmin, diskA); rec.Code != http.StatusConflict {
		t.Fatalf("admin attached elsewhere: got %d", rec.Code)
	}
	if len(be.updated) != 0 {
		t.Fatalf("backend reached despite rejection: %v", be.updated)
	}
	// Positive: own disk re-attached.
	if rec := callUpdateDisk(h, "vm-alice", "alice", models.RoleOperator, diskA); rec.Code != http.StatusOK {
		t.Fatalf("own disk: got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateUploadFile_NeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "x.iso")
	f, err := createUploadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("original")
	f.Close()
	_, err = createUploadFile(dest)
	if err == nil || uploadCreateStatus(err) != http.StatusConflict {
		t.Fatalf("expected 409-mapped error, got %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "original" {
		t.Fatal("existing file was modified")
	}
	// A symlink planted in the pool must not be followed.
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("keep"), 0o600)
	os.Symlink(victim, filepath.Join(dir, "link.iso"))
	if _, err := createUploadFile(filepath.Join(dir, "link.iso")); err == nil {
		t.Fatal("followed symlink")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatal("symlink target modified")
	}
}
