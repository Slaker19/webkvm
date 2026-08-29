package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// VNC console tickets solve a different problem than the single-use
// tickets in ticket.go: the noVNC console opens in a NEW TAB (first
// hop: GET /console/{id}) which then opens its own WebSocket (second
// hop: GET /api/vms/{id}/vnc), and may need to reconnect several
// times over a real work session. A single-use, 30s ticket can't
// cover that — the first hop would burn it before the second ever
// happens. So this ticket is reusable within its lifetime instead of
// single-use, but scoped to one VM ID (checked by the caller) and a
// short, fixed allowlist of endpoints (enforced by the jwt middleware),
// and still far shorter-lived than the session JWT it replaces in the
// console URL (an hour instead of the full login session).

const VNCTicketTTL = 1 * time.Hour

type vncTicketEntry struct {
	user, role, vmID string
	expires          time.Time
}

var (
	vncTicketMu    sync.Mutex
	vncTicketStore = map[string]vncTicketEntry{}
)

// IssueVNCTicket mints a reusable ticket scoped to vmID, valid for
// VNCTicketTTL.
func IssueVNCTicket(user, role, vmID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tk := hex.EncodeToString(b)
	vncTicketMu.Lock()
	defer vncTicketMu.Unlock()
	now := time.Now()
	for k, e := range vncTicketStore {
		if now.After(e.expires) {
			delete(vncTicketStore, k)
		}
	}
	vncTicketStore[tk] = vncTicketEntry{user: user, role: role, vmID: vmID, expires: now.Add(VNCTicketTTL)}
	return tk, nil
}

// CheckVNCTicket validates a ticket against the expected vmID. Unlike
// ConsumeTicket, it does NOT delete the entry — the same ticket may
// be checked again for reconnects and follow-up requests until it
// expires.
func CheckVNCTicket(tk, vmID string) (user, role string, ok bool) {
	vncTicketMu.Lock()
	defer vncTicketMu.Unlock()
	e, found := vncTicketStore[tk]
	if !found || time.Now().After(e.expires) || e.vmID != vmID {
		return "", "", false
	}
	return e.user, e.role, true
}
