package api

import (
	"net/http"
	"strings"
)

// defaultMaxBodyBytes caps the size of an ordinary JSON request body.
//
// 1 MiB is far above anything the API legitimately accepts: the largest
// JSON payloads in the tree are a firewall ruleset import and a
// cloud-init snippet, both of which are bounded well below this.
const defaultMaxBodyBytes int64 = 1 << 20

// bulkUploadPrefixes are the routes that stream large binaries (ISOs,
// disk images, OVAs, VM imports, backup restores). They install their
// OWN, much larger http.MaxBytesReader — see UploadISO, UploadDisk and
// friends in storage.go — so the global cap must not apply to them.
// Everything else — including /api/firewall/import, which installs its
// own 1 MB cap, and the appliance deploy endpoints, which take small
// JSON — is covered by the global limit.
var bulkUploadPrefixes = []string{
	"/api/storage/upload-iso",
	"/api/storage/upload-disk",
	"/api/media/upload",
	"/api/vms/import",
}

// ownLimitRoutes are per-resource upload routes whose path carries an
// id segment, so they cannot be matched by prefix. Each entry is a
// (prefix, suffix) pair matching prefix + <one non-empty segment> +
// suffix; the handler installs its own MaxBytesReader (UploadCover:
// 8 MB), which the global 1 MiB cap would otherwise silently override.
var ownLimitRoutes = []struct{ prefix, suffix string }{
	{"/api/vms/", "/cover"},
}

// exemptFromGlobalLimit reports whether path is a route that installs
// its own, larger body limit.
func exemptFromGlobalLimit(path string) bool {
	for _, p := range bulkUploadPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	for _, rt := range ownLimitRoutes {
		if !strings.HasPrefix(path, rt.prefix) || !strings.HasSuffix(path, rt.suffix) {
			continue
		}
		if len(path) <= len(rt.prefix)+len(rt.suffix) {
			continue
		}
		seg := path[len(rt.prefix) : len(path)-len(rt.suffix)]
		if !strings.Contains(seg, "/") {
			return true
		}
	}
	return false
}

// limitRequestBody caps how many bytes a handler can read from a request.
//
// Handlers across the tree call json.NewDecoder(r.Body).Decode(&req)
// with no bound, and Go's decoder happily allocates whatever it is fed.
// Measured against the live backend: one 200 MB POST to /api/nodes —
// rejected with 400 "uri is required", i.e. a request that never even
// reached any business logic — took the process RSS from 100 MB to
// 754 MB. The memory is only returned to the OS lazily, so a handful of
// concurrent requests from any authenticated user (and, on the
// unauthenticated login route, from anyone at all) is enough to OOM the
// backend and take every VM console and the whole UI down with it.
//
// MaxBytesReader makes the read fail past the limit, so the decoder
// stops early and the handler answers 400 instead of allocating.
func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		if exemptFromGlobalLimit(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, defaultMaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
