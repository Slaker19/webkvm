package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"webkvm/internal/audit"
	"webkvm/internal/helperscripts"

	"github.com/go-chi/chi/v5"
)

func newHelperScriptHandler(t *testing.T) *Handler {
	t.Helper()
	lg, err := audit.New(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{
		helperScripts: helperscripts.NewStore(filepath.Join(t.TempDir(), "hs.json")),
		audit:         lg,
	}
}

// Listing must work — and must stay quiet — when nothing has been
// imported yet. The first thing a user does is open the page, and that
// must not error out or trigger a download.
func TestListHelperScripts_EmptyCache(t *testing.T) {
	h := newHelperScriptHandler(t)
	rec := httptest.NewRecorder()
	h.ListHelperScripts(rec, httptest.NewRequest(http.MethodGet, "/api/helper-scripts", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Scripts []helperscripts.Script `json:"scripts"`
		Stale   bool                   `json:"stale"`
		Source  string                 `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Scripts) != 0 {
		t.Errorf("got %d scripts from an empty cache", len(got.Scripts))
	}
	if !got.Stale {
		t.Error("an empty catalog must be reported as stale so the UI can offer a refresh")
	}
	if !strings.HasPrefix(got.Source, "https://github.com/") {
		t.Errorf("Source = %q, want the upstream project URL for attribution", got.Source)
	}
}

// A zero time.Time marshals to "0001-01-01T00:00:00Z", which a browser
// parses happily: the panel showed "last import: 31/12/1" for a catalog
// that had never been imported. Only null is unambiguous.
func TestListHelperScripts_NeverImportedSendsNullDate(t *testing.T) {
	h := newHelperScriptHandler(t)
	rec := httptest.NewRecorder()
	h.ListHelperScripts(rec, httptest.NewRequest(http.MethodGet, "/api/helper-scripts", nil))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if got := string(raw["fetched_at"]); got != "null" {
		t.Errorf("fetched_at = %s, want null for a catalog that was never imported", got)
	}
}

// Deploying resolves a "cs:<slug>" ID through the ordinary appliance
// path. These cover the ways that resolution must refuse rather than
// guess, since the result is third-party code running as root.
func TestCommunityAppliance_Resolution(t *testing.T) {
	h := newHelperScriptHandler(t)

	if _, err := h.communityAppliance("wordpress"); err == nil {
		t.Error("an unprefixed ID must not resolve as a community script")
	}
	if _, err := h.communityAppliance("cs:../../etc/passwd"); err == nil {
		t.Error("a traversal slug must be refused")
	}
	// Present prefix, empty catalog: the error must point at the missing
	// import rather than pretend the app does not exist.
	_, err := h.communityAppliance("cs:adguard")
	if err == nil {
		t.Fatal("an un-imported script must not resolve")
	}
	if !strings.Contains(err.Error(), "import") {
		t.Errorf("error = %q, want a hint that the catalog needs importing", err)
	}
}

func TestHelperAccessPath(t *testing.T) {
	cases := []struct {
		port       int
		path, want string
	}{
		{8096, "/", ":8096/"},
		{3000, "/admin", ":3000/admin"},
		// Port 80 is implicit in a URL; ":80" would be noise.
		{80, "/", "/"},
		// An undeclared port must not produce a ":0" link that cannot work.
		{0, "/web", "/web"},
		{0, "", "/"},
		// A path missing its leading slash would glue onto the host.
		{8080, "app", ":8080/app"},
	}
	for _, c := range cases {
		if got := helperAccessPath(c.port, c.path); got != c.want {
			t.Errorf("helperAccessPath(%d,%q) = %q, want %q", c.port, c.path, got, c.want)
		}
	}
}

func TestGetHelperScriptProvision_UnknownSlug(t *testing.T) {
	h := newHelperScriptHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/helper-scripts/nope/provision", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("slug", "nope")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.GetHelperScriptProvision(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a slug that is not in the catalog", rec.Code)
	}
}
