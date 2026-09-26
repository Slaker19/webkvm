package api

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"

	"io/fs"

	"webkvm/internal/frontend"
)

// Security response headers applied to every route.
//
// Until now only the standalone console page set these (see console.go),
// which left the SPA and the whole API without a CSP, without
// clickjacking protection and leaking the full URL as a Referer to any
// third-party link the operator clicks. Doing it as one middleware
// rather than per-handler means a new route cannot silently opt out.

// indexScriptHashes holds the CSP hashes of the inline <script> blocks
// in the built index.html.
//
// The SPA ships exactly one inline script: the early theme hydration
// that must run before first paint to avoid a flash of the wrong theme,
// so it cannot be moved into a bundle. Hashing it keeps the policy free
// of 'unsafe-inline', which is the difference between a CSP that stops
// injected script and one that merely documents an intention.
var indexScriptHashes = computeIndexScriptHashes()

var inlineScriptRe = regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>(.*?)</script>`)

func computeIndexScriptHashes() []string {
	distFS, err := fs.Sub(frontend.FS, "dist")
	if err != nil {
		return nil
	}
	data, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range inlineScriptRe.FindAllStringSubmatch(string(data), -1) {
		body := m[1]
		if strings.TrimSpace(body) == "" {
			continue // external script: covered by 'self'
		}
		// The hash must cover the element's exact text content, with no
		// trimming — a single stripped newline invalidates it.
		sum := sha256.Sum256([]byte(body))
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return out
}

// contentSecurityPolicy is assembled once at startup.
//
// Everything the UI loads is same-origin: the bundles, the CSS, the
// self-hosted Inter woff2 files, and the console iframes. The few
// non-'self' sources below are each needed by a real feature:
//
//   - img-src data: — the 2FA QR code is generated client-side into a
//     data: URL, and blob: covers object URLs used for previews.
//   - style-src 'unsafe-inline' — Svelte emits inline style attributes
//     for computed sizing (e.g. Avatar's width/height). Attribute
//     styles cannot be hashed, and the alternative would be rewriting
//     unrelated components to chase a header.
//   - connect-src ws:/wss: — the serial console WebSocket.
//
// frame-ancestors 'self' allows the in-app console iframe while still
// refusing embedding by other origins; form-action and base-uri 'none'
// close the usual injection escape hatches.
var contentSecurityPolicy = buildCSP()

func buildCSP() string {
	scriptSrc := "'self'"
	if len(indexScriptHashes) > 0 {
		scriptSrc += " " + strings.Join(indexScriptHashes, " ")
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + scriptSrc,
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self'",
		"connect-src 'self' ws: wss:",
		"frame-src 'self'",
		"frame-ancestors 'self'",
		"form-action 'self'",
		"base-uri 'none'",
		"object-src 'none'",
	}, "; ")
}

// cspFallbackWriter applies the default policy only when the handler did
// not set one of its own, so an error response on the console path is
// still covered without ever producing two CSP headers.
type cspFallbackWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *cspFallbackWriter) ensureCSP() {
	if w.wrote {
		return
	}
	w.wrote = true
	if w.Header().Get("Content-Security-Policy") == "" {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	}
}

func (w *cspFallbackWriter) WriteHeader(code int) {
	w.ensureCSP()
	w.ResponseWriter.WriteHeader(code)
}

// Write covers handlers that never call WriteHeader explicitly; headers
// are frozen on the first write, so the policy must be set before it.
func (w *cspFallbackWriter) Write(b []byte) (int, error) {
	w.ensureCSP()
	return w.ResponseWriter.Write(b)
}

// Wrapping a ResponseWriter silently drops any interface it also
// implements. The console websocket lives under /api/ today and so never
// reaches this wrapper, but a future route moved under /console/ would
// fail its upgrade with a confusing "not a hijacker" error. Forwarding
// both keeps that trap from being set.
func (w *cspFallbackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hj.Hijack()
}

func (w *cspFallbackWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// securityHeaders sets the headers on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		// strict-origin-when-cross-origin, not no-referrer: same-origin
		// navigation keeps working normally while external links (app
		// websites, upstream docs) never receive the internal path or
		// query string.
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// The UI needs none of these, and denying them means an injected
		// script cannot quietly reach for the camera or the location.
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")

		// HSTS is only meaningful over TLS, and asserting it on a plain
		// HTTP install would pin a hostname to HTTPS that may not be
		// served — locking the operator out of their own panel. TLS is
		// optional in WebKVM, so the header follows the actual scheme.
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		// The console page ships its own, looser policy (it is a
		// self-contained document with inline script and style). Two CSP
		// headers are enforced as an intersection by browsers, which
		// would break it, so ours must not simply be added on that path.
		//
		// Skipping the path outright was not enough either: a request
		// rejected before the handler runs (401, rate limit) never
		// reaches the code that sets the console's own policy, and went
		// out with no CSP at all. So on that path the header is applied
		// at write time, and only if nothing downstream set one.
		if !strings.HasPrefix(r.URL.Path, "/console/") {
			h.Set("Content-Security-Policy", contentSecurityPolicy)
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&cspFallbackWriter{ResponseWriter: w}, r)
	})
}
