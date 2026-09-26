package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"webkvm/internal/appliances"
	"webkvm/internal/helperscripts"

	"github.com/go-chi/chi/v5"
)

// The community-scripts catalog is third-party shell that runs as root
// inside the container it provisions. WebKVM treats it accordingly:
//
//   - the catalog is metadata only, and importing it executes nothing;
//   - refreshing is an explicit admin action, never a side effect of
//     opening a page, so simply browsing the UI cannot make the server
//     reach out to GitHub;
//   - the exact script that would run is readable before running it;
//   - every refresh is audited.
//
// Deploying is intentionally not wired here: it belongs to the appliance
// deploy path, which already carries the confirmation flow, the target
// selection and the audit trail.

// helperAccessPath renders the "how do I reach this" hint shown after a
// deploy, in the ":<port><path>" form the built-in entries already use.
//
// Port 80 is left implicit because appending ":80" to a URL is noise,
// and an unknown port yields a bare path rather than a ":0" that would
// produce a link that cannot work.
func helperAccessPath(port int, webPath string) string {
	if webPath == "" {
		webPath = "/"
	}
	if !strings.HasPrefix(webPath, "/") {
		webPath = "/" + webPath
	}
	if port <= 0 || port == 80 {
		return webPath
	}
	return fmt.Sprintf(":%d%s", port, webPath)
}

// communityAppliance resolves a "cs:<slug>" ID into the same Appliance
// shape the curated catalog uses, so DeployAppliance can treat both
// alike.
//
// Failure is reported rather than silently substituted: deploying the
// wrong application because a slug was mistyped would be worse than a
// clear 404.
func (h *Handler) communityAppliance(id string) (appliances.Appliance, error) {
	slug, ok := helperscripts.SlugFromApplianceID(id)
	if !ok {
		return appliances.Appliance{}, fmt.Errorf("unknown appliance %s", id)
	}
	if h.helperScripts == nil {
		return appliances.Appliance{}, fmt.Errorf("community scripts are not available")
	}
	sc, found := h.helperScripts.Find(slug)
	if !found {
		return appliances.Appliance{}, fmt.Errorf("unknown community script %q; import the catalog first", slug)
	}
	a, err := helperscripts.ToAppliance(sc)
	if err != nil {
		return appliances.Appliance{}, err
	}
	return appliances.Appliance{
		ID:               a.ID,
		Name:             a.Name,
		Description:      a.Description,
		Category:         a.Category,
		VCPUs:            a.VCPUs,
		RAMMB:            a.RAMMB,
		DiskGB:           a.DiskGB,
		BaseImage:        a.BaseImage,
		DefaultType:      a.DefaultType,
		Port:             a.Port,
		WebPath:          a.WebPath,
		IsHelperScript:   a.IsHelperScript,
		ProvisionScript:  a.ProvisionScript,
		DocumentationURL: a.DocumentationURL,
	}, nil
}

// scriptWithDeployID is a catalog entry plus the ID the deploy endpoint
// expects. Sending it saves the frontend from rebuilding the "cs:" ID
// itself, which would put the namespacing rule in two places.
type scriptWithDeployID struct {
	helperscripts.Script
	DeployID string `json:"deploy_id"`
	// InstallURL lets the confirmation dialog link to the exact code that
	// will run as root. Deriving it in the frontend would duplicate the
	// upstream layout in a second place, free to drift when upstream
	// moves a directory.
	InstallURL string `json:"install_url"`
}

func withDeployIDs(scripts []helperscripts.Script) []scriptWithDeployID {
	out := make([]scriptWithDeployID, 0, len(scripts))
	for _, s := range scripts {
		out = append(out, scriptWithDeployID{
			Script:     s,
			DeployID:   helperscripts.ApplianceID(s.Slug),
			InstallURL: helperscripts.InstallURL(s.Slug),
		})
	}
	return out
}

// fetchedAt renders the import timestamp for the API, as null when the
// catalog has never been imported.
//
// A zero time.Time marshals to "0001-01-01T00:00:00Z", which is a
// perfectly valid date as far as the browser is concerned: the UI parsed
// it and proudly displayed "31/12/1" as the last import of a catalog
// that had never been imported at all. null cannot be mistaken for one.
func fetchedAt(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ListHelperScripts returns the cached catalog. It never fetches.
func (h *Handler) ListHelperScripts(w http.ResponseWriter, r *http.Request) {
	cat := h.helperScripts.Get()
	jsonResp(w, http.StatusOK, map[string]any{
		"scripts":    withDeployIDs(cat.Scripts),
		"fetched_at": fetchedAt(cat.FetchedAt),
		"skipped":    cat.Skipped,
		"stale":      h.helperScripts.Stale(),
		"source":     helperscripts.RepoURL,
	})
}

// RefreshHelperScripts re-imports the upstream catalog. Admin-only: it
// makes the server perform several hundred outbound requests, which is
// not something a read-only user should be able to trigger.
func (h *Handler) RefreshHelperScripts(w http.ResponseWriter, r *http.Request) {
	// Detached from the request context on purpose. The import takes
	// minutes; if it were cancelled when the browser tab closed, the
	// user would be left with a half-refreshed catalog and no way to
	// tell why.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cat, err := h.helperScripts.Refresh(ctx)
	if err != nil {
		h.audit.Log(auditFor(r, "helperscripts.refresh_failed", "", map[string]interface{}{"error": err.Error()}))
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "helperscripts.refresh", "", map[string]interface{}{
		"imported": len(cat.Scripts),
		"skipped":  cat.Skipped,
	}))
	jsonResp(w, http.StatusOK, map[string]any{
		"scripts":    withDeployIDs(cat.Scripts),
		"fetched_at": fetchedAt(cat.FetchedAt),
		"skipped":    cat.Skipped,
		"stale":      false,
	})
}

// GetHelperScriptProvision returns the exact script that a deploy would
// execute, so an admin can read third-party code before trusting it.
// Restricted to admins because the response embeds the upstream URLs and
// the full shim.
func (h *Handler) GetHelperScriptProvision(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	sc, ok := h.helperScripts.Find(slug)
	if !ok {
		jsonErr(w, http.StatusNotFound, "unknown script")
		return
	}
	script, err := helperscripts.BuildProvisionScript(sc)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"slug":          sc.Slug,
		"name":          sc.Name,
		"deploy_id":     helperscripts.ApplianceID(sc.Slug),
		"install_url":   helperscripts.InstallURL(sc.Slug),
		"launcher_url":  helperscripts.LauncherURL(sc.Slug),
		"provision":     script,
		"third_party":   true,
		"runs_as_root":  true,
		"license":       "MIT (community-scripts ORG)",
		"needs_gpu":     sc.NeedsGPU,
		"port":          sc.Port,
		"web_path":      sc.WebPath,
		"recommended":   map[string]any{"vcpus": sc.VCPUs, "ram_mb": sc.RAMMB, "disk_gb": sc.DiskGB},
		"base_os":       sc.OS,
		"base_version":  sc.Version,
		"upstream_site": sc.Website,
	})
}
