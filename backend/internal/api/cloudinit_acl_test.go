package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/cloudinit"
	"webkvm/internal/models"
)

func snippetRequest(method, path, user, role string, body any, id string) *http.Request {
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
	if id != "" {
		rctx.URLParams.Add("id", id)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// A-4: Un operador no debe poder modificar ni borrar snippets creados por otro usuario.
func TestCloudInitSnippets_AislamientoEntreOperadores(t *testing.T) {
	dir := t.TempDir()
	store, err := cloudinit.NewSnippetStore(dir)
	if err != nil {
		t.Fatalf("snippet store: %v", err)
	}
	h := &Handler{snippets: store}

	// 1. Alice crea un snippet propio
	createBody := map[string]any{
		"name":    "alice-database-config",
		"type":    "user-data",
		"content": "#cloud-config\npackage_upgrade: true",
	}
	createReq := snippetRequest(http.MethodPost, "/api/cloudinit/snippets", "alice", models.RoleOperator, createBody, "")
	createRec := httptest.NewRecorder()
	h.CreateCloudInitSnippet(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("alice deberia poder crear snippet; got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created cloudinit.Snippet
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created snippet: %v", err)
	}
	if created.ID == "" {
		t.Fatal("snippet creado sin ID")
	}

	// 2. Bob intenta MODIFICAR el snippet de Alice -> debe dar 403
	updateBody := map[string]any{
		"name":    "modificado-por-bob",
		"content": "# backdoor",
	}
	updateReq := snippetRequest(http.MethodPut, "/api/cloudinit/snippets/"+created.ID, "bob", models.RoleOperator, updateBody, created.ID)
	updateRec := httptest.NewRecorder()
	h.UpdateCloudInitSnippet(updateRec, updateReq)

	if updateRec.Code != http.StatusForbidden {
		t.Fatalf("bob modifico el snippet de alice sin permiso; got %d: %s", updateRec.Code, updateRec.Body.String())
	}

	// 3. Bob intenta BORRAR el snippet de Alice -> debe dar 403
	delReq := snippetRequest(http.MethodDelete, "/api/cloudinit/snippets/"+created.ID, "bob", models.RoleOperator, nil, created.ID)
	delRec := httptest.NewRecorder()
	h.DeleteCloudInitSnippet(delRec, delReq)

	if delRec.Code != http.StatusForbidden {
		t.Fatalf("bob borro el snippet de alice sin permiso; got %d: %s", delRec.Code, delRec.Body.String())
	}

	// 4. Alice (propietaria) si puede actualizar su propio snippet
	aliceUpdateReq := snippetRequest(http.MethodPut, "/api/cloudinit/snippets/"+created.ID, "alice", models.RoleOperator, updateBody, created.ID)
	aliceUpdateRec := httptest.NewRecorder()
	h.UpdateCloudInitSnippet(aliceUpdateRec, aliceUpdateReq)

	if aliceUpdateRec.Code != http.StatusOK {
		t.Fatalf("alice deberia poder actualizar su propio snippet; got %d: %s", aliceUpdateRec.Code, aliceUpdateRec.Body.String())
	}

	// 5. Admin si puede borrar el snippet
	adminDelReq := snippetRequest(http.MethodDelete, "/api/cloudinit/snippets/"+created.ID, "admin", models.RoleAdmin, nil, created.ID)
	adminDelRec := httptest.NewRecorder()
	h.DeleteCloudInitSnippet(adminDelRec, adminDelReq)

	if adminDelRec.Code != http.StatusOK {
		t.Fatalf("el admin deberia poder borrar el snippet; got %d: %s", adminDelRec.Code, adminDelRec.Body.String())
	}
}

// B-2: ListCloudInitSnippets y GetCloudInitSnippet no deben filtrar snippets privados de otros usuarios
func TestCloudInitSnippets_LecturaAislada(t *testing.T) {
	dir := t.TempDir()
	store, err := cloudinit.NewSnippetStore(dir)
	if err != nil {
		t.Fatalf("snippet store: %v", err)
	}
	h := &Handler{snippets: store}

	// Alice crea un snippet propio
	createBody := map[string]any{
		"name":    "alice-secret-keys",
		"type":    "user-data",
		"content": "#cloud-config\nssh_authorized_keys:\n  - ssh-ed25519 AAAA...",
	}
	createReq := snippetRequest(http.MethodPost, "/api/cloudinit/snippets", "alice", models.RoleOperator, createBody, "")
	createRec := httptest.NewRecorder()
	h.CreateCloudInitSnippet(createRec, createReq)

	var created cloudinit.Snippet
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)

	// Bob lista snippets: NO debe ver el snippet de Alice en la lista
	listReq := snippetRequest(http.MethodGet, "/api/cloudinit/snippets", "bob", models.RoleOperator, nil, "")
	listRec := httptest.NewRecorder()
	h.ListCloudInitSnippets(listRec, listReq)

	var list []cloudinit.Snippet
	_ = json.Unmarshal(listRec.Body.Bytes(), &list)
	for _, sn := range list {
		if sn.ID == created.ID {
			t.Fatalf("fuga en ListCloudInitSnippets: bob ve el snippet privado de alice (id=%s, name=%s)", sn.ID, sn.Name)
		}
	}

	// Bob intenta obtener directamente el snippet de Alice por ID -> 404 (anti-enumeracion)
	getReq := snippetRequest(http.MethodGet, "/api/cloudinit/snippets/"+created.ID, "bob", models.RoleOperator, nil, created.ID)
	getRec := httptest.NewRecorder()
	h.GetCloudInitSnippet(getRec, getReq)

	if getRec.Code != http.StatusNotFound {
		t.Fatalf("GetCloudInitSnippet por parte de bob deberia dar 404 Not Found; got %d: %s", getRec.Code, getRec.Body.String())
	}

	// Alice si puede ver su propio snippet por ID -> 200
	aliceGetReq := snippetRequest(http.MethodGet, "/api/cloudinit/snippets/"+created.ID, "alice", models.RoleOperator, nil, created.ID)
	aliceGetRec := httptest.NewRecorder()
	h.GetCloudInitSnippet(aliceGetRec, aliceGetReq)

	if aliceGetRec.Code != http.StatusOK {
		t.Fatalf("alice deberia poder leer su propio snippet; got %d: %s", aliceGetRec.Code, aliceGetRec.Body.String())
	}

	// Admin si puede ver el snippet de Alice por ID -> 200
	adminGetReq := snippetRequest(http.MethodGet, "/api/cloudinit/snippets/"+created.ID, "admin", models.RoleAdmin, nil, created.ID)
	adminGetRec := httptest.NewRecorder()
	h.GetCloudInitSnippet(adminGetRec, adminGetReq)

	if adminGetRec.Code != http.StatusOK {
		t.Fatalf("admin deberia poder leer cualquier snippet; got %d: %s", adminGetRec.Code, adminGetRec.Body.String())
	}
}

// Bug 6: every snippet resolution path must apply the read rule, not
// just GET/List. Bob previewing (or referencing in a VM create) Alice's
// snippet id must not expand her user-data.
func TestCloudInitSnippets_PreviewNoLeeSnippetAjeno(t *testing.T) {
	dir := t.TempDir()
	store, err := cloudinit.NewSnippetStore(dir)
	if err != nil {
		t.Fatalf("snippet store: %v", err)
	}
	h := &Handler{snippets: store}
	const secret = "#cloud-config\n# alice-secret-token-123"
	sn, err := store.Create(cloudinit.Snippet{Name: "alice-private", Type: "user-data", Content: secret, Owner: "alice"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, body := range []map[string]any{
		{"snippet_id": sn.ID},
		{"snippet_ids": []string{sn.ID}},
	} {
		rec := httptest.NewRecorder()
		h.PreviewCloudInit(rec, snippetRequest(http.MethodPost, "/api/cloudinit/preview", "bob", models.RoleOperator, body, ""))
		if bytes.Contains(rec.Body.Bytes(), []byte("alice-secret-token-123")) {
			t.Fatalf("bob leyo el snippet de alice via preview %v: %s", body, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		h.PreviewCloudInit(rec, snippetRequest(http.MethodPost, "/api/cloudinit/preview", "alice", models.RoleOperator, body, ""))
		if !bytes.Contains(rec.Body.Bytes(), []byte("alice-secret-token-123")) {
			t.Fatalf("alice deberia ver su propio snippet via preview %v: %d %s", body, rec.Code, rec.Body.String())
		}
	}

	// The shared scrubber used by VM/template/appliance create and reapply.
	ci := &models.CloudInitRequest{SnippetID: sn.ID, SnippetIDs: []string{sn.ID}}
	h.scrubCloudInitSnippets(snippetRequest(http.MethodPost, "/api/vms", "bob", models.RoleOperator, nil, ""), ci)
	if ci.SnippetID != "" || len(ci.SnippetIDs) != 0 {
		t.Fatalf("scrub no elimino referencias ajenas: %+v", ci)
	}
	ci = &models.CloudInitRequest{SnippetID: sn.ID, SnippetIDs: []string{sn.ID}}
	h.scrubCloudInitSnippets(snippetRequest(http.MethodPost, "/api/vms", "admin", models.RoleAdmin, nil, ""), ci)
	if ci.SnippetID != sn.ID || len(ci.SnippetIDs) != 1 {
		t.Fatalf("scrub elimino referencias legitimas del admin: %+v", ci)
	}
}
