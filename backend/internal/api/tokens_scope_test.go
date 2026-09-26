package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/models"
	"webkvm/internal/tokens"
)

// B-1: CreateToken debe rechazar scopes granulares que no se aplican en tiempo de ejecucion
func TestCreateToken_RechazaScopesGranularesNoSoportados(t *testing.T) {
	dir := t.TempDir()
	store, err := tokens.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{tokens: store}

	// Token con scopes no soportados -> debe dar 400 Bad Request
	body, _ := json.Marshal(map[string]any{
		"name":   "ci-token",
		"scopes": []string{"vms:read"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tokens", bytes.NewReader(body))
	req.Header.Set("X-User", "alice")
	req.Header.Set("X-Role", models.RoleOperator)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.CreateToken(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("se esperaba 400 Bad Request al solicitar scopes granulares; got %d: %s", rec.Code, rec.Body.String())
	}

	// Token sin scopes -> permitido (201 Created)
	bodyOk, _ := json.Marshal(map[string]any{
		"name": "valid-token",
	})
	reqOk := httptest.NewRequest(http.MethodPost, "/api/tokens", bytes.NewReader(bodyOk))
	reqOk.Header.Set("X-User", "alice")
	reqOk.Header.Set("X-Role", models.RoleOperator)
	reqOk.Header.Set("Content-Type", "application/json")

	recOk := httptest.NewRecorder()
	h.CreateToken(recOk, reqOk)

	if recOk.Code != http.StatusCreated {
		t.Fatalf("se esperaba 201 Created sin scopes; got %d: %s", recOk.Code, recOk.Body.String())
	}
}
