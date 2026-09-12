// Package auth cookie helpers for V13-SEC-01: the session JWT moves from
// the JavaScript-visible Authorization header to an HttpOnly cookie, so an
// XSS can no longer exfiltrate the token. A second, non-HttpOnly CSRF
// cookie backs a double-submit CSRF token for state-changing requests.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
)

const (
	// SessionCookieName holds the session JWT. HttpOnly + SameSite=Lax.
	SessionCookieName = "webkvm_session"
	// CSRFCookieName holds the double-submit CSRF token (non-HttpOnly so
	// the SPA can echo it back as the X-CSRF-Token header).
	CSRFCookieName = "webkvm_csrf"
)

// SessionToken returns the value of the session cookie, or "".
func SessionToken(r *http.Request) string {
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// CSRFValue returns the value of the CSRF cookie, or "".
func CSRFValue(r *http.Request) string {
	c, err := r.Cookie(CSRFCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// SetSessionCookie writes the HttpOnly session cookie with maxAge seconds.
func SetSessionCookie(w http.ResponseWriter, token string, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

// ClearSessionCookie deletes the session cookie.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// SetCSRFCookie writes the non-HttpOnly CSRF cookie. maxAge must match
// the session cookie's own MaxAge (the caller's TokenTTL) — leaving it
// unset made this a browser SESSION cookie while the JWT session cookie
// is long-lived, so closing and reopening the browser silently dropped
// the CSRF cookie while the login itself was still valid: the SPA would
// restore a logged-in UI from the surviving session cookie but with an
// empty csrfState, and the very first mutating request (e.g. opening a
// VM/host terminal ticket) failed with "invalid csrf token" until the
// user logged out and back in.
func SetCSRFCookie(w http.ResponseWriter, value string, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: CSRFCookieName, Value: value, Path: "/",
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

// ClearCSRFCookie deletes the CSRF cookie.
func ClearCSRFCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: CSRFCookieName, Value: "", Path: "/",
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// NewCSRFValue mints a fresh 256-bit CSRF token.
func NewCSRFValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CSRFValid reports whether the request's X-CSRF-Token header matches the
// CSRF cookie, in constant time. An empty header or cookie is invalid.
func CSRFValid(header, cookie string) bool {
	if header == "" || cookie == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(cookie)) == 1
}

// IsUnsafeMethod reports whether the HTTP method mutates state and thus
// requires CSRF protection when the session is cookie-authenticated.
func IsUnsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}