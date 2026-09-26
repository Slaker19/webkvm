package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type browseLocalRequest struct {
	// Path is the directory to list. Empty means "start at the top",
	// which is answered with the mount points a pool can sensibly live
	// under rather than a raw listing of /.
	Path string `json:"path"`
}

type browseLocalEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Writable is false for a directory the server cannot create files
	// in. Shown rather than hidden: an operator who mounted a disk
	// read-only needs to see it and understand why it is refused, not
	// wonder where it went.
	Writable bool `json:"writable"`
}

type browseLocalResponse struct {
	Path    string             `json:"path"`
	Parent  string             `json:"parent"`
	Entries []browseLocalEntry `json:"entries"`
}

// browseLocalRoots are the directories the picker starts from.
//
// Opening at "/" would put every system directory one click away from
// being chosen as a pool root, and all but a couple of them are on the
// deny list anyway. These are the places a mounted disk actually shows
// up, plus the pools directory itself.
func (h *Handler) browseLocalRoots() []browseLocalEntry {
	candidates := []string{"/mnt", "/media", "/srv", "/opt"}
	if h.cfg != nil {
		candidates = append(candidates, h.cfg.PoolsDir())
	}
	seen := map[string]bool{}
	out := make([]browseLocalEntry, 0, len(candidates))
	for _, c := range candidates {
		if seen[c] {
			continue
		}
		seen[c] = true
		st, err := os.Stat(c)
		if err != nil || !st.IsDir() {
			continue
		}
		out = append(out, browseLocalEntry{
			Name:     c,
			Path:     c,
			Writable: dirWritable(c),
		})
	}
	return out
}

// dirWritable reports whether the server can create entries in dir.
//
// Tested by actually creating and removing a file rather than reading
// the mode bits: the server runs as root, for whom the mode bits say
// yes on a read-only mount that will still refuse the write.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".webkvm-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// BrowseLocal lists the subdirectories of a path on the server, for the
// folder picker in the storage-pool creation form. Admin-only (see
// router.go), matching BrowseRemote and pool creation itself.
//
// Typing the path by hand is still allowed everywhere this is offered;
// this exists because that is the step where a typo silently roots a
// pool somewhere it should not be.
//
// The listing is restricted the same way pool creation is: a path the
// deny list refuses is never shown, so the picker cannot walk the
// operator into a directory the create call would then reject.
func (h *Handler) BrowseLocal(w http.ResponseWriter, r *http.Request) {
	var req browseLocalRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	path := strings.TrimSpace(req.Path)
	if path == "" || path == "/" {
		jsonResp(w, http.StatusOK, browseLocalResponse{
			Path:    "",
			Parent:  "",
			Entries: h.browseLocalRoots(),
		})
		return
	}
	// Clean before validating so "/mnt/../etc" cannot slip past the
	// deny list by spelling a denied directory indirectly.
	path = filepath.Clean(path)
	if err := validatePoolPath(path); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid path: "+err.Error())
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "cannot read directory: "+err.Error())
		return
	}
	if !st.IsDir() {
		jsonErr(w, http.StatusBadRequest, "not a directory")
		return
	}
	des, err := os.ReadDir(path)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "cannot read directory: "+err.Error())
		return
	}
	entries := make([]browseLocalEntry, 0, len(des))
	for _, de := range des {
		if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		child := filepath.Join(path, de.Name())
		// A child the deny list refuses is left out entirely: showing
		// it would let the operator descend into a directory that pool
		// creation is going to reject anyway.
		if validatePoolPath(child) != nil {
			continue
		}
		entries = append(entries, browseLocalEntry{
			Name:     de.Name(),
			Path:     child,
			Writable: dirWritable(child),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	// An empty parent sends the picker back to the root list rather
	// than to "/", which is not a browsable location here.
	parent := filepath.Dir(path)
	if parent == path || parent == "/" || validatePoolPath(parent) != nil {
		parent = ""
	}
	jsonResp(w, http.StatusOK, browseLocalResponse{
		Path:    path,
		Parent:  parent,
		Entries: entries,
	})
}
