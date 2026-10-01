package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webkvm/internal/netguard"
)

func TestJailEndpoints(t *testing.T) {
	jail := netguard.NewJail(3, 1*time.Minute, 15*time.Minute, nil)
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
	for i := 0; i < 3; i++ {
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
}
