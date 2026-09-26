package auth

import (
	"testing"
	"time"
)

func TestIssueAndCheckVNCTicket(t *testing.T) {
	// Clean up global state before and after
	vncTicketMu.Lock()
	vncTicketStore = make(map[string]vncTicketEntry)
	vncTicketMu.Unlock()
	t.Cleanup(func() {
		vncTicketMu.Lock()
		vncTicketStore = make(map[string]vncTicketEntry)
		vncTicketMu.Unlock()
	})

	// 1. Issue a valid ticket
	tk, err := IssueVNCTicket("bob", "viewer", "vm-1", 0)
	if err != nil {
		t.Fatalf("IssueVNCTicket: %v", err)
	}
	if len(tk) != 64 {
		t.Errorf("got ticket length %d, want 64", len(tk))
	}

	// 2. Check it
	u, r, _, ok := CheckVNCTicket(tk, "vm-1")
	if !ok || u != "bob" || r != "viewer" {
		t.Errorf("CheckVNCTicket valid: got %q, %q, %v; want bob, viewer, true", u, r, ok)
	}

	// 3. Re-check works (reusable)
	_, _, _, ok = CheckVNCTicket(tk, "vm-1")
	if !ok {
		t.Errorf("CheckVNCTicket should allow reuse")
	}

	// 4. Invalid vmID fails
	_, _, _, ok = CheckVNCTicket(tk, "vm-2")
	if ok {
		t.Errorf("CheckVNCTicket allowed wrong vmID")
	}

	// 5. Cleanup on issue: manually insert expired
	vncTicketMu.Lock()
	vncTicketStore["expired"] = vncTicketEntry{
		user:    "alice",
		expires: time.Now().Add(-1 * time.Minute),
	}
	vncTicketMu.Unlock()

	_, err = IssueVNCTicket("carol", "admin", "vm-3", 0)
	if err != nil {
		t.Fatalf("IssueVNCTicket 2: %v", err)
	}

	vncTicketMu.Lock()
	_, foundExpired := vncTicketStore["expired"]
	vncTicketMu.Unlock()
	if foundExpired {
		t.Errorf("IssueVNCTicket failed to clean up expired tickets")
	}

	// 6. Check expired ticket
	_, _, _, ok = CheckVNCTicket("expired", "vm-3")
	if ok {
		t.Errorf("CheckVNCTicket allowed expired ticket")
	}
}
