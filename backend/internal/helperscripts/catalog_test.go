package helperscripts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeUpstream serves a miniature copy of the GitHub contents API plus
// the launchers it advertises, so the importer can be exercised without
// touching the network.
func fakeUpstream(t *testing.T, launchers map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/index", func(w http.ResponseWriter, r *http.Request) {
		type entry struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			DownloadURL string `json:"download_url"`
		}
		var out []entry
		for name := range launchers {
			out = append(out, entry{Name: name, Type: "file", DownloadURL: srv.URL + "/raw/" + name})
		}
		// A directory and a non-script must be ignored by the importer.
		out = append(out, entry{Name: "subdir", Type: "dir"})
		out = append(out, entry{Name: "README.md", Type: "file", DownloadURL: srv.URL + "/raw/README.md"})
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/raw/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/raw/")
		body, ok := launchers[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	t.Cleanup(srv.Close)
	return srv
}

func newTestStore(t *testing.T, srv *httptest.Server) *Store {
	t.Helper()
	s := NewStore(filepath.Join(t.TempDir(), "cache.json"))
	s.HTTP = srv.Client()
	s.indexURL = srv.URL + "/index"
	return s
}

func TestRefresh_ParsesAndPersists(t *testing.T) {
	srv := fakeUpstream(t, map[string]string{
		"jellyfin.sh": jellyfinLauncher,
		"adguard.sh":  adguardLauncher,
		"booklore.sh": "APP=\"BookLore\"\nmsg_error \"no longer available\"\nexit 1\n",
	})
	s := newTestStore(t, srv)

	cat, err := s.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(cat.Scripts) != 2 {
		t.Fatalf("got %d scripts, want 2 (the retired one must be skipped)", len(cat.Scripts))
	}
	if cat.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", cat.Skipped)
	}
	// Sorted by name so the UI list is stable between refreshes.
	if cat.Scripts[0].Name != "Adguard" || cat.Scripts[1].Name != "Jellyfin" {
		t.Errorf("scripts are not sorted by name: %v", []string{cat.Scripts[0].Name, cat.Scripts[1].Name})
	}
	if cat.Scripts[1].Source == "" {
		t.Error("Source must record where the metadata came from")
	}

	// A second store on the same path must see the cache without any
	// network access at all.
	reloaded := NewStore(s.path)
	if got := len(reloaded.Get().Scripts); got != 2 {
		t.Errorf("reloaded cache has %d scripts, want 2", got)
	}
}

func TestRefresh_KeepsPreviousCatalogWhenNothingParses(t *testing.T) {
	good := fakeUpstream(t, map[string]string{"jellyfin.sh": jellyfinLauncher})
	s := newTestStore(t, good)
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Simulate a rate-limited or broken upstream that answers with
	// entries that parse to nothing. Overwriting a working catalog with
	// an empty one would empty the user's app list for no good reason.
	bad := fakeUpstream(t, map[string]string{"x.sh": "not a launcher\n"})
	s.HTTP = bad.Client()
	s.indexURL = bad.URL + "/index"
	if _, err := s.Refresh(context.Background()); err == nil {
		t.Fatal("an all-unparseable refresh must report an error")
	}
	if got := len(s.Get().Scripts); got != 1 {
		t.Errorf("previous catalog was clobbered: %d scripts left", got)
	}
}

func TestStaleAndFind(t *testing.T) {
	srv := fakeUpstream(t, map[string]string{"jellyfin.sh": jellyfinLauncher})
	s := newTestStore(t, srv)

	if !s.Stale() {
		t.Error("an empty catalog must report as stale")
	}
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Stale() {
		t.Error("a freshly refreshed catalog must not be stale")
	}

	if _, ok := s.Find("jellyfin"); !ok {
		t.Error("Find did not return a script that was just imported")
	}
	if _, ok := s.Find("nope"); ok {
		t.Error("Find returned an unknown slug")
	}

	s.Get().FetchedAt = time.Now().Add(-2 * MaxCacheAge)
	if !s.Stale() {
		t.Error("a catalog older than MaxCacheAge must be stale")
	}
}

// Get must never reach the network: opening the page in the UI should
// not make WebKVM call GitHub.
func TestGetDoesNotFetch(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "missing.json"))
	s.HTTP = &http.Client{Transport: refusingTransport{t}}
	if got := len(s.Get().Scripts); got != 0 {
		t.Errorf("expected an empty catalog, got %d scripts", got)
	}
}

type refusingTransport struct{ t *testing.T }

func (r refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Error("Get() performed an HTTP request")
	return nil, http.ErrUseLastResponse
}
