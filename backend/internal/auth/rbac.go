package auth

import (
	"encoding/json"
	"net/http"
)

// roleContext is a small value that we stash in the request headers
// after parsing the JWT. The authentication middleware already sets
// X-User and X-Role; RequireRole reads X-Role and rejects the request
// if it doesn't match one of the allowed roles.
//
// Allowed roles: "admin", "operator", "viewer" — see models.Role*.
// The hierarchy is admin > operator > viewer for "at least" checks
// (use RequireAtLeast(role)).
const (
	HeaderUser = "X-User"
	HeaderRole = "X-Role"
)

// RequireRole rejects any request whose JWT role is not in `allowed`.
// 401 if no role is set (auth middleware not applied), 403 if role
// not allowed.
func RequireRole(allowed ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := r.Header.Get(HeaderRole)
			if role == "" {
				writeAuthError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			for _, a := range allowed {
				if role == a {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeAuthError(w, http.StatusForbidden, "insufficient role for this action")
		})
	}
}

// RequireAtLeast is a hierarchical check: the role must be at least
// `min` in the chain admin > operator > viewer. Convenience wrapper
// around RequireRole that expands the role set.
func RequireAtLeast(min string) func(http.Handler) http.Handler {
	var allowed []string
	switch min {
	case "viewer":
		allowed = []string{"viewer", "operator", "admin"}
	case "operator":
		allowed = []string{"operator", "admin"}
	case "admin":
		allowed = []string{"admin"}
	default:
		allowed = []string{min}
	}
	return RequireRole(allowed...)
}

func writeAuthError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
