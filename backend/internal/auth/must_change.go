package auth

import (
	"net/http"
	"strings"
)

// LookupUserMustChange is the function the middleware uses to ask
// the user store "does this user currently have must_change_password
// set?". It's an interface to keep the auth package free of a
// hard dependency on the user package (the user package itself
// imports auth, so importing user here would be a cycle).
//
// Production wires this up to *user.Store.MustChangePassword in
// cmd/server/main.go. Tests can supply their own.
type LookupUserMustChange func(username string) (bool, error)

// MustChangeEnforcer is a middleware that rejects any authenticated
// request from a user whose MustChangePassword flag is true, with
// the exception of a small set of paths the user needs to call to
// recover (auth endpoints, the change-password endpoint, health).
//
// The flag is checked against the live user store on every request
// (not the JWT) so an admin can clear it via the user admin API
// without the user needing to log out and back in.
//
// Returns 403 with a JSON body {"error":"password change required", ...}
// so the frontend can detect it and route to /account.
func MustChangeEnforcer(lookup LookupUserMustChange, exceptPrefixes ...string) func(http.Handler) http.Handler {
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
			must, err := lookup(user)
			if err != nil {
				// If we can't tell, fail closed. The lookup is
				// just a read against an in-memory map; errors
				// here mean the store is broken.
				writeAuthError(w, http.StatusInternalServerError, "user lookup failed")
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
