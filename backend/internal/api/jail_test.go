package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"webkvm/internal/netguard"
)

func TestJailEndpoints(t *testing.T) {
	tempDir := t.TempDir()
	jail, err := netguard.New(tempDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{jail: jail}

	// 1. Initial list empty
	r := httptest.NewRequest(http.MethodGet, "/api/settings/jail", nil)
	w := httptest.NewRecorder()
	h.ListJailedIPs(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Ban an IP
	attacker := "198.51.100.99"
	for i := 0; i < 5; i++ {
		jail.RecordFailure(attacker, "bad password")
	}

	// 2. List contains IP
	w2 := httptest.NewRecorder()
	h.ListJailedIPs(w2, r)
	if !strings.Contains(w2.Body.String(), attacker) {
		t.Fatalf("expected response to contain %s, got: %s", attacker, w2.Body.String())
	}

	// 3. Unban
	unbanBody := strings.NewReader(`{"ip":"` + attacker + `"}`)
	rUnban := httptest.NewRequest(http.MethodPost, "/api/settings/jail/unban", unbanBody)
	wUnban := httptest.NewRecorder()
	h.UnbanJailedIP(wUnban, rUnban)
	if wUnban.Code != http.StatusOK {
		t.Fatalf("expected 200 on unban, got %d: %s", wUnban.Code, wUnban.Body.String())
	}

	// 4. Verify unbanned
	w3 := httptest.NewRecorder()
	h.ListJailedIPs(w3, r)
	if strings.Contains(w3.Body.String(), attacker) {
		t.Fatalf("expected %s to be unbanned", attacker)
	}

	// 5. Manual Ban
	manualIP := "203.0.113.123"
	banReqBody, _ := json.Marshal(map[string]any{
		"ip":           manualIP,
		"reason":       "Brute force scan",
		"jail":         "manual",
		"duration_sec": 3600,
	})
	rManual := httptest.NewRequest(http.MethodPost, "/api/settings/jail/ban", bytes.NewReader(banReqBody))
	wManual := httptest.NewRecorder()
	h.ManualBanIP(wManual, rManual)
	if wManual.Code != http.StatusOK {
		t.Fatalf("expected 200 on manual ban, got %d: %s", wManual.Code, wManual.Body.String())
	}

	// Verify banned
	wManualCheck := httptest.NewRecorder()
	h.ListJailedIPs(wManualCheck, r)
	if !strings.Contains(wManualCheck.Body.String(), manualIP) {
		t.Fatalf("expected %s to be banned", manualIP)
	}

	// 6. Whitelist operations
	wWhiteList := httptest.NewRecorder()
	rWhiteList := httptest.NewRequest(http.MethodGet, "/api/settings/jail/whitelist", nil)
	h.GetJailWhitelist(wWhiteList, rWhiteList)
	if wWhiteList.Code != http.StatusOK || !strings.Contains(wWhiteList.Body.String(), "127.0.0.0/8") {
		t.Fatalf("failed to get whitelist: %s", wWhiteList.Body.String())
	}

	// Add to whitelist
	addNet := "198.51.100.0/24"
	addWhiteReq, _ := json.Marshal(map[string]any{
		"cidr":        addNet,
		"description": "Test Admin Subnet",
	})
	rAddWhite := httptest.NewRequest(http.MethodPost, "/api/settings/jail/whitelist", bytes.NewReader(addWhiteReq))
	wAddWhite := httptest.NewRecorder()
	h.AddJailWhitelist(wAddWhite, rAddWhite)
	if wAddWhite.Code != http.StatusOK {
		t.Fatalf("failed to add whitelist: %s", wAddWhite.Body.String())
	}

	// Remove from whitelist
	delWhiteReq, _ := json.Marshal(map[string]any{"cidr": addNet})
	rDelWhite := httptest.NewRequest(http.MethodDelete, "/api/settings/jail/whitelist", bytes.NewReader(delWhiteReq))
	wDelWhite := httptest.NewRecorder()
	h.RemoveJailWhitelist(wDelWhite, rDelWhite)
	if wDelWhite.Code != http.StatusOK {
		t.Fatalf("failed to remove whitelist: %s", wDelWhite.Body.String())
	}

	// 7. Custom Jails
	customReq, _ := json.Marshal(netguard.JailDefinition{
		ID:          "postgres",
		Name:        "PostgreSQL DB",
		Enabled:     true,
		MaxAttempts: 3,
		WindowSec:   120,
		BanDuration: 1800,
		Port:        5432,
		Description: "Database brute force guard",
	})
	rCustom := httptest.NewRequest(http.MethodPost, "/api/settings/jail/custom", bytes.NewReader(customReq))
	wCustom := httptest.NewRecorder()
	h.AddCustomJail(wCustom, rCustom)
	if wCustom.Code != http.StatusOK {
		t.Fatalf("failed to add custom jail: %s", wCustom.Body.String())
	}

	// Delete custom jail
	rDelCustom := httptest.NewRequest(http.MethodDelete, "/api/settings/jail/custom/postgres", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "postgres")
	rDelCustom = rDelCustom.WithContext(context.WithValue(rDelCustom.Context(), chi.RouteCtxKey, rctx))
	wDelCustom := httptest.NewRecorder()
	h.DeleteCustomJail(wDelCustom, rDelCustom)
	if wDelCustom.Code != http.StatusOK {
		t.Fatalf("failed to delete custom jail: %s", wDelCustom.Body.String())
	}
}
