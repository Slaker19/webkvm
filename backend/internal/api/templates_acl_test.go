package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// ListVMs filters through filterVMsByACL (vms.go:43). ListTemplates does
// not — it appends every VM carrying the template flag, whoever owns it
// (templates.go:58-70). The template list is therefore a full-fleet
// dump for any authenticated caller, and InstantiateTemplate clones from
// whatever {id} it is given.
//
// This is the DeletePool shape again: the restriction that exists lives
// in the UI, not on the endpoint. There is no requireVMOwnership on
// POST /api/templates/{id}/instantiate either, so the read is not merely
// a disclosure — it is the first half of cloning somebody else's disk.

// Embedding compute.Backend (nil) and overriding only what the two
// handlers under test touch is the pattern the other tests here use: any
// other call panics, which keeps the test honest about its own reach.
type aclTemplateBackend struct {
	compute.Backend
	vms   []models.VM
	metas map[string]models.VMMeta
}

func (b *aclTemplateBackend) ListDomains() ([]models.VM, error) { return b.vms, nil }

func (b *aclTemplateBackend) GetVMMeta(id string) (models.VMMeta, error) {
	if m, ok := b.metas[id]; ok {
		return m, nil
	}
	return models.VMMeta{}, fmt.Errorf("vm %q not found", id)
}

// UpdateVMMeta applies the partial update to the in-memory metas, just
// enough of the real backends' semantics for the Shared/Template flags.
func (b *aclTemplateBackend) UpdateVMMeta(id string, upd models.VMMetaUpdate) (models.VMMeta, error) {
	m, ok := b.metas[id]
	if !ok {
		return models.VMMeta{}, fmt.Errorf("vm %q not found", id)
	}
	if upd.Template != nil {
		m.Template = *upd.Template
	}
	if upd.Shared != nil {
		m.Shared = *upd.Shared
	}
	if upd.OwnerID != nil {
		m.OwnerID = *upd.OwnerID
	}
	b.metas[id] = m
	return m, nil
}

func requestAs(user, role string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/templates", nil)
	req.Header.Set("X-User", user)
	req.Header.Set("X-Role", role)
	return req
}

func aclHarness() *Handler {
	return &Handler{compute: &aclTemplateBackend{
		vms: []models.VM{
			{ID: "tpl-alice", Name: "plantilla-alice"},
			{ID: "vm-bob", Name: "vm-bob"},
		},
		metas: map[string]models.VMMeta{
			"tpl-alice": {OwnerID: "alice", Template: true},
			"vm-bob":    {OwnerID: "bob"},
		},
	}}
}

func templatesFrom(t *testing.T, h *Handler, req *http.Request) []models.VM {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ListTemplates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListTemplates devolvió %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Templates []models.VM `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("respuesta ilegible: %v", err)
	}
	return out.Templates
}

// ListVMs answers with a bare array, not an envelope.
func vmsFrom(t *testing.T, h *Handler, req *http.Request) []models.VM {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ListVMs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListVMs devolvió %d: %s", rec.Code, rec.Body.String())
	}
	var out []models.VM
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("respuesta ilegible: %v", err)
	}
	return out
}

// The control: ListVMs is already correct. If this ever starts failing,
// the harness is wrong rather than the code.
func TestListVMs_HidesOtherUsersVMs(t *testing.T) {
	h := aclHarness()
	got := vmsFrom(t, h, requestAs("bob", models.RoleOperator))
	for _, vm := range got {
		if vm.ID == "tpl-alice" {
			t.Fatal("ListVMs devolvió una VM de alice a bob: el control está mal")
		}
	}
}

// The defect: ListTemplates ignores the same ACL.
func TestListTemplates_LeaksOtherUsersTemplates(t *testing.T) {
	h := aclHarness()
	got := templatesFrom(t, h, requestAs("bob", models.RoleOperator))

	for _, tpl := range got {
		if tpl.ID == "tpl-alice" {
			t.Errorf("ListTemplates devolvió %q a bob, que no es su dueño", tpl.ID)
		}
	}
}

// An admin sees everything, so the endpoint is not simply broken: the
// gap is that a non-admin sees as much as an admin.
func TestListTemplates_AdminStillSeesEverything(t *testing.T) {
	h := aclHarness()
	got := templatesFrom(t, h, requestAs("admin", models.RoleAdmin))
	if len(got) != 1 || got[0].ID != "tpl-alice" {
		t.Fatalf("admin debería ver la plantilla, vio %+v", got)
	}
}

// ---- Shared templates ("share with all users") ----

// sharedHarness adds a shared template owned by alice next to the
// non-shared one from aclHarness.
func sharedHarness() (*Handler, *aclTemplateBackend) {
	b := &aclTemplateBackend{
		vms: []models.VM{
			{ID: "tpl-alice", Name: "plantilla-alice"},
			{ID: "tpl-shared", Name: "plantilla-compartida", Template: true, Shared: true},
			{ID: "vm-bob", Name: "vm-bob"},
		},
		metas: map[string]models.VMMeta{
			"tpl-alice":  {OwnerID: "alice", Template: true},
			"tpl-shared": {OwnerID: "alice", Template: true, Shared: true},
			"vm-bob":     {OwnerID: "bob"},
		},
	}
	return &Handler{compute: b}, b
}

func withID(req *http.Request, id string) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
}

func metaUpdate(h *Handler, user, role, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, "/api/vms/"+id+"/meta", strings.NewReader(body))
	req.Header.Set("X-User", user)
	req.Header.Set("X-Role", role)
	rec := httptest.NewRecorder()
	h.UpdateVMMeta(rec, withID(req, id))
	return rec
}

func TestListTemplates_NonAdminSeesSharedTemplate(t *testing.T) {
	h, _ := sharedHarness()
	got := templatesFrom(t, h, requestAs("bob", models.RoleOperator))
	var sawShared bool
	for _, tpl := range got {
		switch tpl.ID {
		case "tpl-shared":
			sawShared = true
		case "tpl-alice":
			t.Errorf("bob ve la plantilla no compartida de alice")
		}
	}
	if !sawShared {
		t.Fatalf("bob debería ver la plantilla compartida, vio %+v", got)
	}
}

func TestListVMs_StillHidesSharedTemplate(t *testing.T) {
	h, _ := sharedHarness()
	for _, vm := range vmsFrom(t, h, requestAs("bob", models.RoleOperator)) {
		if vm.ID == "tpl-shared" {
			t.Fatal("ListVMs no debe mostrar a bob la plantilla compartida de alice")
		}
	}
}

func TestUpdateVMMeta_SharedRequiresAdmin(t *testing.T) {
	h, b := sharedHarness()
	rec := metaUpdate(h, "alice", models.RoleOperator, "tpl-alice", `{"shared":true}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("un operador no debería poder compartir: %d %s", rec.Code, rec.Body.String())
	}
	if b.metas["tpl-alice"].Shared {
		t.Fatal("la plantilla quedó compartida pese al 403")
	}
}

func TestUpdateVMMeta_SharedOnNonTemplateConflicts(t *testing.T) {
	h, b := sharedHarness()
	rec := metaUpdate(h, "admin", models.RoleAdmin, "vm-bob", `{"shared":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("compartir una VM que no es plantilla debería dar 409: %d %s", rec.Code, rec.Body.String())
	}
	if b.metas["vm-bob"].Shared {
		t.Fatal("la VM quedó compartida pese al 409")
	}
	// Same request turning it into a template is accepted.
	rec = metaUpdate(h, "admin", models.RoleAdmin, "vm-bob", `{"template":true,"shared":true}`)
	if rec.Code != http.StatusOK || !b.metas["vm-bob"].Shared {
		t.Fatalf("template+shared en la misma petición debería aceptarse: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateVMMeta_AdminSharesTemplate(t *testing.T) {
	h, b := sharedHarness()
	rec := metaUpdate(h, "admin", models.RoleAdmin, "tpl-alice", `{"shared":true}`)
	if rec.Code != http.StatusOK || !b.metas["tpl-alice"].Shared {
		t.Fatalf("el admin debería poder compartir la plantilla: %d %s", rec.Code, rec.Body.String())
	}
	// Clearing the template flag through the generic path clears Shared.
	rec = metaUpdate(h, "admin", models.RoleAdmin, "tpl-alice", `{"template":false}`)
	if rec.Code != http.StatusOK || b.metas["tpl-alice"].Shared {
		t.Fatalf("quitar la marca de plantilla debería limpiar shared: %d %+v", rec.Code, b.metas["tpl-alice"])
	}
}

func TestUnsetVMTemplate_ClearsShared(t *testing.T) {
	h, b := sharedHarness()
	req := withID(requestAs("admin", models.RoleAdmin), "tpl-shared")
	rec := httptest.NewRecorder()
	h.UnsetVMTemplate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("UnsetVMTemplate devolvió %d: %s", rec.Code, rec.Body.String())
	}
	if m := b.metas["tpl-shared"]; m.Template || m.Shared {
		t.Fatalf("tras quitar la plantilla no debe quedar compartida: %+v", m)
	}
}

func TestCanInstantiateTemplate(t *testing.T) {
	h, b := sharedHarness()
	bob := requestAs("bob", models.RoleOperator)
	if err := h.canInstantiateTemplate(bob, "tpl-alice", b.metas["tpl-alice"]); err == nil {
		t.Error("bob no debería poder instanciar la plantilla no compartida de alice")
	}
	if err := h.canInstantiateTemplate(bob, "tpl-shared", b.metas["tpl-shared"]); err != nil {
		t.Errorf("bob debería poder instanciar la plantilla compartida: %v", err)
	}
}

// End-to-end through the handler: the non-shared foreign template is
// refused with 403 before anything else runs.
func TestInstantiateTemplate_NonSharedForeignForbidden(t *testing.T) {
	h, _ := sharedHarness()
	req := httptest.NewRequest(http.MethodPost, "/api/templates/tpl-alice/instantiate", strings.NewReader(`{"name":"clon-bob"}`))
	req.Header.Set("X-User", "bob")
	req.Header.Set("X-Role", models.RoleOperator)
	rec := httptest.NewRecorder()
	h.InstantiateTemplate(rec, withID(req, "tpl-alice"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("instanciar una plantilla ajena no compartida debería dar 403: %d %s", rec.Code, rec.Body.String())
	}
}
