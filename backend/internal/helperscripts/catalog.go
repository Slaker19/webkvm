package helperscripts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Upstream coordinates. The Incus fork is used rather than the better
// known Proxmox one because WebKVM creates Incus containers: the ct/
// launchers there already target that runtime. The install/ scripts are
// byte-identical between the two forks, so nothing is lost.
const (
	repoOwner = "community-scripts"
	repoName  = "Incus"
	repoRef   = "main"

	apiListURL = "https://api.github.com/repos/" + repoOwner + "/" + repoName + "/contents/ct?ref=" + repoRef
	rawBase    = "https://raw.githubusercontent.com/" + repoOwner + "/" + repoName + "/" + repoRef
)

// RepoURL is the upstream project page, for attribution in the UI.
const RepoURL = "https://github.com/" + repoOwner + "/" + repoName

// InstallURL returns the upstream installer for a slug. This is the
// script that actually runs inside the container.
func InstallURL(slug string) string {
	return rawBase + "/install/" + slug + "-install.sh"
}

// LauncherURL returns the upstream ct/ launcher, for attribution links.
func LauncherURL(slug string) string {
	return rawBase + "/ct/" + slug + ".sh"
}

// Catalog is the cached, parsed upstream index.
type Catalog struct {
	// FetchedAt is when the cache was last refreshed.
	FetchedAt time.Time `json:"fetched_at"`
	// Scripts are the installable applications, sorted by name.
	Scripts []Script `json:"scripts"`
	// Skipped counts upstream entries that are not installable
	// (withdrawn stubs), kept so the UI can be honest about the gap
	// between "611 files upstream" and what is offered.
	Skipped int `json:"skipped"`
}

// Store caches the catalog on disk so the UI does not hit GitHub on
// every page load, and so a network outage does not empty the list.
type Store struct {
	mu   sync.Mutex
	path string
	cat  *Catalog
	// HTTP lets tests inject a client; nil uses a bounded default.
	HTTP *http.Client
	// indexURL overrides the upstream index, so tests can exercise the
	// importer against a local server instead of GitHub.
	indexURL string
}

// NewStore returns a store backed by path. The cache is loaded lazily:
// construction must not block startup on network or disk.
func NewStore(path string) *Store { return &Store{path: path} }

// MaxCacheAge is how long a cached catalog is served before a refresh
// is suggested. Upstream adds apps often, but not hourly.
const MaxCacheAge = 24 * time.Hour

func (s *Store) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Get returns the cached catalog, loading it from disk on first use.
// It never fetches from the network: refreshing is an explicit,
// admin-triggered action so that merely opening the page cannot make
// WebKVM reach out to GitHub.
func (s *Store) Get() *Catalog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cat == nil {
		s.cat = s.loadLocked()
	}
	return s.cat
}

// Stale reports whether the cache is missing or older than MaxCacheAge.
func (s *Store) Stale() bool {
	c := s.Get()
	return c == nil || len(c.Scripts) == 0 || time.Since(c.FetchedAt) > MaxCacheAge
}

func (s *Store) loadLocked() *Catalog {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return &Catalog{}
	}
	var c Catalog
	if json.Unmarshal(b, &c) != nil {
		return &Catalog{}
	}
	return &c
}

type ghEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	DownloadURL string `json:"download_url"`
}

// Refresh downloads the upstream index and every launcher, parses them
// and replaces the cache. It is slow by nature (hundreds of small HTTP
// requests) so callers should run it from an explicit admin action with
// a generous context, never from a page load.
func (s *Store) Refresh(ctx context.Context) (*Catalog, error) {
	entries, err := s.listLaunchers(ctx)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("upstream index is empty")
	}

	// Bounded concurrency: enough to make 600 requests reasonable,
	// low enough to stay a polite client and avoid GitHub rate limits.
	const workers = 8
	type result struct {
		script Script
		ok     bool
	}
	jobs := make(chan ghEntry)
	out := make(chan result, len(entries))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				slug := strings.TrimSuffix(e.Name, ".sh")
				body, err := s.fetch(ctx, e.DownloadURL)
				if err != nil {
					out <- result{}
					continue
				}
				sc, ok := Parse(slug, body)
				sc.Source = LauncherURL(slug)
				out <- result{script: sc, ok: ok}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, e := range entries {
			select {
			case <-ctx.Done():
				return
			case jobs <- e:
			}
		}
	}()

	go func() { wg.Wait(); close(out) }()

	cat := &Catalog{FetchedAt: time.Now().UTC()}
	for r := range out {
		if r.ok {
			cat.Scripts = append(cat.Scripts, r.script)
		} else {
			cat.Skipped++
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(cat.Scripts) == 0 {
		// Never overwrite a good cache with nothing: a rate-limited or
		// offline refresh would otherwise wipe a working catalog.
		return nil, fmt.Errorf("no installable scripts parsed; keeping previous catalog")
	}
	sort.Slice(cat.Scripts, func(i, j int) bool {
		return strings.ToLower(cat.Scripts[i].Name) < strings.ToLower(cat.Scripts[j].Name)
	})

	s.mu.Lock()
	s.cat = cat
	s.mu.Unlock()
	s.save(cat)
	return cat, nil
}

func (s *Store) listLaunchers(ctx context.Context) ([]ghEntry, error) {
	url := apiListURL
	if s.indexURL != "" {
		url = s.indexURL
	}
	body, err := s.fetch(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("list upstream scripts: %w", err)
	}
	var entries []ghEntry
	if err := json.Unmarshal([]byte(body), &entries); err != nil {
		return nil, fmt.Errorf("parse upstream index: %w", err)
	}
	out := entries[:0]
	for _, e := range entries {
		if e.Type == "file" && strings.HasSuffix(e.Name, ".sh") && e.DownloadURL != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

// maxScriptBytes caps a single downloaded file. Launchers are a few KB;
// anything vastly larger means the URL is not what we think it is.
const maxScriptBytes = 512 << 10

func (s *Store) fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "WebKVM-helper-script-importer")
	resp, err := s.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxScriptBytes))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// save persists the cache, tolerating a read-only or missing directory:
// an uncacheable catalog still works for the current process.
func (s *Store) save(c *Catalog) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0644) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// Find returns one script by slug.
func (s *Store) Find(slug string) (Script, bool) {
	for _, sc := range s.Get().Scripts {
		if sc.Slug == slug {
			return sc, true
		}
	}
	return Script{}, false
}
