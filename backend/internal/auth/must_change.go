package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"webkvm/internal/models"
)

// LookupUserSession is the function the middleware uses to ask the user
// store "does this user currently have must_change_password set, and what
// session epoch are they on?". It's an interface to keep the auth package
// free of a hard dependency on the user package (the user package itself
// imports auth, so importing user here would be a cycle).
//
// Production wires this up to *user.Store.SessionStatus in cmd/server/main.go.
// Tests can supply their own. One call answers all questions, so adding
// the epoch and active checks reuses the exact store hit MustChangeEnforcer already
// made — no new per-request lookup.
type LookupUserSession func(username string) (mustChange bool, epoch int, active bool, err error)

// SessionEnforcer is a middleware that rejects any authenticated request
// from a user whose account is inactive, whose MustChangePassword flag is true,
// or whose token's session epoch (see Claims.SessionEpoch / HeaderTokenEpoch)
// no longer matches the account's current one — i.e. an admin has forced that
// account to log out everywhere since this token was issued. A small set
// of paths is exempted so the user can still recover (auth endpoints, the
// change-password endpoint, health).
//
// Both checks are read against the live user store on every request (not
// just the JWT), so an admin's action takes effect immediately without
// the affected user needing to already be logged out.
//
// Returns 401 for an inactive or revoked session ({"error":"account is disabled"} or
// {"error":"session revoked, please log in again"}), and 403 for a pending password change
// (JSON body {"error":"password change required"}, unchanged from before).
func SessionEnforcer(lookup LookupUserSession, exceptPrefixes ...string) func(http.Handler) http.Handler {
	exceptSet := make(map[string]bool, len(exceptPrefixes))
	for _, p := range exceptPrefixes {
		exceptSet[p] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := r.Header.Get(HeaderUser)
			if user == "" {
				// Not authenticated yet — let the auth middleware
				// (which runs before this) handle the response.
				next.ServeHTTP(w, r)
				return
			}
			// Always allow the excepted paths so the user can
			// self-recover (change password) or self-inspect (me).
			for p := range exceptSet {
				if strings.HasPrefix(r.URL.Path, p) {
					next.ServeHTTP(w, r)
					return
				}
			}
			must, epoch, active, err := lookup(user)
			if err != nil {
				// A deleted account is an expected case (token
				// outlives the user), not a server failure.
				if errors.Is(err, models.ErrUserNotFound) {
					writeAuthError(w, http.StatusUnauthorized, "account not found")
					return
				}
				// If we can't tell, fail closed. The lookup is
				// just a read against an in-memory map; errors
				// here mean the store is broken.
				writeAuthError(w, http.StatusInternalServerError, "user lookup failed")
				return
			}
			if !active {
				writeAuthError(w, http.StatusUnauthorized, "account is disabled")
				return
			}
			// Missing header (a ticket/API-token-authenticated request,
			// which carries no JWT claims here) reads as epoch 0 — the
			// same value every pre-epoch-feature token has, so this only
			// ever rejects a request whose account HAS been revoked
			// since, which is the correct fail-safe direction.
			tokenEpoch, _ := strconv.Atoi(r.Header.Get(HeaderTokenEpoch))
			if tokenEpoch != epoch {
				writeAuthError(w, http.StatusUnauthorized, "session revoked, please log in again")
				return
			}
			if must {
				writeAuthError(w, http.StatusForbidden, "password change required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
