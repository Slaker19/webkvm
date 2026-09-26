package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Console tickets authorize a SINGLE WebSocket connection to an embedded
// terminal (VM serial / host PTY). They are minted by authenticated API
// calls, live 30 seconds, are consumed on first use, and carry the role
// they were issued for (host terminal re-checks admin). This replaces
// long-lived JWTs traveling in URLs, which leak into browser history,
// proxy logs and Referer headers.

const TicketTTL = 30 * time.Second

type ticketEntry struct {
	user    string
	role    string
	epoch   int
	expires time.Time
}

var (
	ticketMu    sync.Mutex
	ticketStore = map[string]ticketEntry{}
)

// TokenEpoch returns the session epoch Middleware stamped on an
// authenticated request (HeaderTokenEpoch), for passing to IssueTicket /
// IssueVNCTicket. The header is always overwritten by Middleware, never
// taken from the client.
func TokenEpoch(r *http.Request) int {
	epoch, _ := strconv.Atoi(r.Header.Get(HeaderTokenEpoch))
	return epoch
}

// IssueTicket mints a fresh single-use ticket bound to the given user
// and role. epoch is the session epoch of the credential that minted it
// (see TokenEpoch); the middleware replays it into HeaderTokenEpoch so
// SessionEnforcer compares the ticket against the account's current
// epoch exactly as it would the minting session's own token.
func IssueTicket(user, role string, epoch int) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tk := hex.EncodeToString(b)
	ticketMu.Lock()
	defer ticketMu.Unlock()
	// Opportunistic cleanup of stale entries.
	now := time.Now()
	for k, e := range ticketStore {
		if now.After(e.expires) {
			delete(ticketStore, k)
		}
	}
	ticketStore[tk] = ticketEntry{user: user, role: role, epoch: epoch, expires: now.Add(TicketTTL)}
	return tk, nil
}

// ConsumeTicket validates and burns a ticket, returning its user, role
// and the session epoch it was minted under. Single-use: the entry is
// deleted whether or not it is valid.
func ConsumeTicket(tk string) (user string, role string, epoch int, ok bool) {
	ticketMu.Lock()
	defer ticketMu.Unlock()
	e, ok := ticketStore[tk]
	delete(ticketStore, tk)
	if !ok || time.Now().After(e.expires) {
		return "", "", 0, false
	}
	return e.user, e.role, e.epoch, true
}
