package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ticketChain wires Middleware -> SessionEnforcer as router.go does, with
// the account currently on accountEpoch.
func ticketChain(t *testing.T, accountEpoch int) http.Handler {
	t.Helper()
	m := newTestManager(t)
	lookup := func(string) (bool, int, bool, error) { return false, accountEpoch, true, nil }
	return m.Middleware(SessionEnforcer(lookup, "/api/auth/")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
	))
}

func serve(h http.Handler, url string) int {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, url, nil))
	return rr.Code
}

// Tickets (?ticket= / ?vt=) must carry the minting session's epoch. They
// used to set no HeaderTokenEpoch, which SessionEnforcer read as 0, so
// once any routine user edit moved the account's epoch, SSE / serial /
// VNC returned 401 permanently — even after re-login.
func TestTicket_CarriesSessionEpoch(t *testing.T) {
	h := ticketChain(t, 3)

	tk, err := IssueTicket("alice", "operator", 3)
	if err != nil {
		t.Fatal(err)
	}
	if code := serve(h, "/api/events?ticket="+tk); code != http.StatusOK {
		t.Errorf("ticket minted at current epoch: status %d, want 200", code)
	}

	vt, err := IssueVNCTicket("alice", "operator", "vm-1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if code := serve(h, "/api/vms/vm-1/vnc?vt="+vt); code != http.StatusOK {
		t.Errorf("vnc ticket minted at current epoch: status %d, want 200", code)
	}
}

// Revocation still applies: a ticket minted under an older epoch is refused.
func TestTicket_StaleEpochRejected(t *testing.T) {
	h := ticketChain(t, 4)

	tk, _ := IssueTicket("alice", "operator", 3)
	if code := serve(h, "/api/events?ticket="+tk); code != http.StatusUnauthorized {
		t.Errorf("stale ticket: status %d, want 401", code)
	}
	vt, _ := IssueVNCTicket("alice", "operator", "vm-1", 3)
	if code := serve(h, "/api/vms/vm-1/vnc?vt="+vt); code != http.StatusUnauthorized {
		t.Errorf("stale vnc ticket: status %d, want 401", code)
	}
}
