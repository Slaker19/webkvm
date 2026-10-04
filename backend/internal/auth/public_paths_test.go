package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The public allowlist in Middleware is a second gate, independent of the
// chi router: a route mounted outside an auth group is still rejected here
// unless it is listed. That is deliberate defence in depth, and it is also
// easy to forget when adding a route, so both halves are pinned here.
func TestMiddleware_PublicPaths(t *testing.T) {
	_, mux := buildMiddlewareMux(t)

	public := []struct {
		path, why string
	}{
		{"/api/health", "load-balancer probe"},
		{"/api/auth/login", "login itself cannot require a session"},
		{"/api/media/custom:x.png/raw", "<img src> cannot set headers"},
		{"/api/covers/abc.png", "legacy cover images"},
		{"/api/branding", "logo and favicon render on the login screen"},
		{"/api/system/cert", "certificate download for first-time trust"},
		{"/api/metrics/grafana-dashboard", "official grafana dashboard json download"},
		{"/api/metrics/alert-rules", "prometheus alertmanager rules yaml download"},
	}
	for _, tc := range public {
		req := httptest.NewRequest("GET", tc.path, nil)
		rr := httptest.NewRecorder()
		mux.Handler.ServeHTTP(rr, req)
		if rr.Code == http.StatusUnauthorized {
			t.Errorf("%s must be public (%s), got 401", tc.path, tc.why)
		}
	}
}

// The counterpart: everything around the public media/branding routes must
// stay authenticated. A prefix match that was slightly too generous would
// expose listing, upload and delete.
func TestMiddleware_NeighbouringPathsStayPrivate(t *testing.T) {
	_, mux := buildMiddlewareMux(t)

	private := []string{
		"/api/media",
		"/api/media/",
		"/api/media/upload",
		"/api/media/apply-usage",
		"/api/media/custom:x.png",
		"/api/branding/secret",
		"/api/brandingx",
		"/api/settings",
		"/api/users",
	}
	for _, p := range private {
		req := httptest.NewRequest("GET", p, nil)
		rr := httptest.NewRecorder()
		mux.Handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s must require auth, got %d", p, rr.Code)
		}
	}
}
