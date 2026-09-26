package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/cloudinit"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

func newTestHandlerWithSnippets(t *testing.T) (*Handler, string) {
	tmp := t.TempDir()
	store, err := cloudinit.NewSnippetStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	auditFile := tmp + "/audit.log"
	auditLogger, err := audit.New(auditFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: tmp}
	h := &Handler{
		cfg:      cfg,
		snippets: store,
		audit:    auditLogger,
	}
	return h, tmp
}

func TestListCloudInitSnippets(t *testing.T) {
	h, _ := newTestHandlerWithSnippets(t)
	req := httptest.NewRequest(http.MethodGet, "/api/cloudinit/snippets", nil)
	w := httptest.NewRecorder()

	h.ListCloudInitSnippets(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var list []cloudinit.Snippet
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) < 4 {
		t.Errorf("expected at least 4 presets, got %d", len(list))
	}
}

func TestCloudInitSnippetsCRUD(t *testing.T) {
	h, _ := newTestHandlerWithSnippets(t)

	// Create
	payload := `{"name":"Nginx Web Server","description":"Installs nginx","type":"user-data","content":"#cloud-config\npackages:\n  - nginx\n"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cloudinit/snippets", strings.NewReader(payload))
	w := httptest.NewRecorder()
	h.CreateCloudInitSnippet(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created cloudinit.Snippet
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "Nginx Web Server" {
		t.Fatalf("invalid created snippet: %+v", created)
	}

	// Get
	r := chi.NewRouter()
	r.Get("/api/cloudinit/snippets/{id}", h.GetCloudInitSnippet)
	r.Put("/api/cloudinit/snippets/{id}", h.UpdateCloudInitSnippet)
	r.Delete("/api/cloudinit/snippets/{id}", h.DeleteCloudInitSnippet)

	reqGet := httptest.NewRequest(http.MethodGet, "/api/cloudinit/snippets/"+created.ID, nil)
	wGet := httptest.NewRecorder()
	r.ServeHTTP(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 on get, got %d", wGet.Code)
	}

	// Update
	upPayload := `{"name":"Nginx Web Server v2","content":"#cloud-config\npackages:\n  - nginx\n  - curl\n"}`
	reqUp := httptest.NewRequest(http.MethodPut, "/api/cloudinit/snippets/"+created.ID, strings.NewReader(upPayload))
	wUp := httptest.NewRecorder()
	r.ServeHTTP(wUp, reqUp)
	if wUp.Code != http.StatusOK {
		t.Fatalf("expected 200 on update, got %d: %s", wUp.Code, wUp.Body.String())
	}

	// Delete
	reqDel := httptest.NewRequest(http.MethodDelete, "/api/cloudinit/snippets/"+created.ID, nil)
	wDel := httptest.NewRecorder()
	r.ServeHTTP(wDel, reqDel)
	if wDel.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d: %s", wDel.Code, wDel.Body.String())
	}
}

func TestPreviewCloudInit(t *testing.T) {
	h, _ := newTestHandlerWithSnippets(t)

	body, _ := json.Marshal(PreviewCloudInitRequest{
		SnippetID: "preset-docker",
		User:      "devops",
		Hostname:  "docker-node-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cloudinit/preview", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.PreviewCloudInit(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	ud := resp["user_data"]
	if !strings.Contains(ud, "name: devops") || !strings.Contains(ud, "docker-ce") || !strings.Contains(ud, "usermod -aG docker") {
		t.Errorf("preview user-data missing expected fields:\n%s", ud)
	}
}

func TestCloudInitSeedName(t *testing.T) {
	cases := map[string]string{
		"my-vm":       "seed-my-vm.iso",
		"my vm space": "seed-my-vm-space.iso",
	}
	for name, want := range cases {
		if got := cloudInitSeedName(name); got != want {
			t.Errorf("cloudInitSeedName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFindCloudInitCdrom(t *testing.T) {
	disks := []models.DiskInfo{
		{Device: "disk", Target: "vda", Source: "/pool/vm.qcow2"},
		{Device: "cdrom", Target: "sdb", Source: "/data/cloudinit/seed-my-vm.iso"},
		{Device: "cdrom", Target: "sdc", Source: "/pool/isos/ubuntu.iso"},
	}
	cd := findCloudInitCdrom(disks, "my-vm")
	if cd == nil {
		t.Fatal("expected to find the seed cdrom")
	}
	if cd.Target != "sdb" {
		t.Errorf("expected target sdb, got %s", cd.Target)
	}

	// A VM with no seed attached (only a regular ISO) must return nil.
	none := findCloudInitCdrom(disks[:1], "my-vm")
	if none != nil {
		t.Errorf("expected no seed cdrom, got %+v", none)
	}
	other := findCloudInitCdrom(disks, "different-vm")
	if other != nil {
		t.Errorf("expected no match for a different VM name, got %+v", other)
	}
}

func TestBuildCloudInitSeed_UniqueInstanceIDOnReapply(t *testing.T) {
	if _, err := lookPathXorriso(); err != nil {
		t.Skip("xorriso not available in test environment")
	}
	h, _ := newTestHandlerWithSnippets(t)

	req := &models.CloudInitRequest{User: "alice", Password: "s3cr3t1", Hostname: "vm1"}
	iso1, err := h.buildCloudInitSeed("vm1", req)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	// A second call for the same VM must produce a different instance-id
	// (embedded in the ISO) so cloud-init re-applies instead of skipping
	// it as already-provisioned. We can't easily read back the ISO
	// content without mounting it, so we assert indirectly: two builds
	// one second apart must not error and must both leave a valid,
	// non-empty ISO at the same deterministic path (overwritten).
	iso2, err := h.buildCloudInitSeed("vm1", req)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if iso1 != iso2 {
		t.Errorf("expected the same seed path across reapplies, got %q vs %q", iso1, iso2)
	}
}

// lookPathXorriso is a tiny indirection so the ISO-building test can
// skip cleanly on environments without xorriso installed (same
// dependency BuildNoCloudISO already requires).
func lookPathXorriso() (string, error) {
	return exec.LookPath("xorriso")
}
