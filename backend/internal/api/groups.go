package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// groupsStore is a small on-disk JSON store of group definitions
// (name + color), shared across all VMs. Membership (which VMs belong to
// which group) is stored in each VM's <webkvm:meta><groups> element.
type groupsStore struct {
	mu      sync.Mutex
	path    string
	byName  map[string]models.Group
}

func newGroupsStore(path string) *groupsStore {
	gs := &groupsStore{path: path, byName: map[string]models.Group{}}
	_ = gs.load()
	return gs
}

func (g *groupsStore) load() error {
	data, err := os.ReadFile(g.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var defs []models.Group
	if err := json.Unmarshal(data, &defs); err != nil {
		return err
	}
	for _, d := range defs {
		g.byName[d.Name] = d
	}
	return nil
}

func (g *groupsStore) save() error {
	if err := os.MkdirAll(filepath.Dir(g.path), 0755); err != nil {
		return err
	}
	defs := make([]models.Group, 0, len(g.byName))
	for _, d := range g.byName {
		defs = append(defs, d)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	data, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		return err
	}
	tmp := g.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, g.path)
}

// all returns the definitions sorted by name.
func (g *groupsStore) all() []models.Group {
	out := make([]models.Group, 0, len(g.byName))
	for _, d := range g.byName {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ListGroups returns all defined groups plus their per-app member counts.
// Member counts are computed from the live libvirt domains.
func (h *Handler) ListGroups(w http.ResponseWriter, r *http.Request) {
	h.gs.mu.Lock()
	defs := h.gs.all()
	h.gs.mu.Unlock()

	counts := map[string]int{}
	if err := h.lv.EnsureConnected(); err == nil {
		allVMs, _ := h.lv.ListDomains()
		for _, vm := range allVMs {
			for _, name := range vm.Groups {
				counts[name]++
			}
		}
	}

	out := make([]models.Group, 0, len(defs))
	for _, d := range defs {
		d.MemberCount = counts[d.Name]
		out = append(out, d)
	}
	jsonResp(w, http.StatusOK, models.GroupList{Groups: out})
}

// CreateGroup creates a new group definition.
func (h *Handler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	var req models.GroupUpsertRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Color == "" {
		req.Color = "#7c3aed" // default accent
	}
	if !isValidHexColor(req.Color) {
		jsonErr(w, http.StatusBadRequest, "color must be a #rrggbb hex string")
		return
	}
	h.gs.mu.Lock()
	defer h.gs.mu.Unlock()
	if _, exists := h.gs.byName[req.Name]; exists {
		jsonErr(w, http.StatusConflict, "group already exists")
		return
	}
	h.gs.byName[req.Name] = models.Group{Name: req.Name, Color: req.Color}
	if err := h.gs.save(); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusCreated, h.gs.byName[req.Name])
}

// UpdateGroup renames or recolors an existing group. The rename
// propagates: every VM that referenced the old name gets updated to the
// new name.
func (h *Handler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	oldName := strings.TrimSpace(chiURLParam(r, "name"))
	var req models.GroupUpsertRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Color != "" && !isValidHexColor(req.Color) {
		jsonErr(w, http.StatusBadRequest, "color must be a #rrggbb hex string")
		return
	}
	h.gs.mu.Lock()
	defer h.gs.mu.Unlock()
	cur, ok := h.gs.byName[oldName]
	if !ok {
		jsonErr(w, http.StatusNotFound, "group not found")
		return
	}
	if req.Color == "" {
		req.Color = cur.Color
	}
	delete(h.gs.byName, oldName)
	if oldName != req.Name {
		if _, exists := h.gs.byName[req.Name]; exists {
			// Roll back the in-memory deletion so we don't lose oldName.
			h.gs.byName[oldName] = cur
			jsonErr(w, http.StatusConflict, "target name already exists")
			return
		}
	}
	h.gs.byName[req.Name] = models.Group{Name: req.Name, Color: req.Color}
	if err := h.gs.save(); err != nil {
		// Try to roll back: simplest is to leave the inconsistency and
		// surface the error so the user can retry.
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Propagate the rename to every VM's metadata. Best-effort: we don't
	// fail the request if a single VM's metadata write fails, but we do
	// log it via the response.
	if oldName != req.Name {
		var renameFailures []string
		if err := h.lv.EnsureConnected(); err == nil {
			allVMs, _ := h.lv.ListDomains()
			for _, vm := range allVMs {
				meta, err := h.lv.GetVMMeta(vm.ID)
				if err != nil {
					continue
				}
				changed := false
				for i, g := range meta.Groups {
					if g == oldName {
						meta.Groups[i] = req.Name
						changed = true
					}
				}
				if changed {
					if err := h.lv.SetVMMeta(vm.ID, meta); err != nil {
						renameFailures = append(renameFailures, vm.ID)
					}
				}
			}
		}
		if len(renameFailures) > 0 {
			jsonResp(w, http.StatusOK, map[string]any{
				"status":   "renamed",
				"failures": renameFailures,
			})
			return
		}
	}
	jsonResp(w, http.StatusOK, h.gs.byName[req.Name])
}

// DeleteGroup removes the group definition and scrubs the tag from every
// VM that referenced it.
func (h *Handler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chiURLParam(r, "name"))
	h.gs.mu.Lock()
	if _, ok := h.gs.byName[name]; !ok {
		h.gs.mu.Unlock()
		jsonErr(w, http.StatusNotFound, "group not found")
		return
	}
	delete(h.gs.byName, name)
	if err := h.gs.save(); err != nil {
		h.gs.mu.Unlock()
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.gs.mu.Unlock()

	// Scrub the tag from every VM's metadata.
	if err := h.lv.EnsureConnected(); err == nil {
		allVMs, _ := h.lv.ListDomains()
		for _, vm := range allVMs {
			meta, err := h.lv.GetVMMeta(vm.ID)
			if err != nil {
				continue
			}
			filtered := meta.Groups[:0]
			for _, g := range meta.Groups {
				if g != name {
					filtered = append(filtered, g)
				}
			}
			if len(filtered) != len(meta.Groups) {
				_ = h.lv.SetVMMeta(vm.ID, models.VMMeta{
					Alias: meta.Alias, Notes: meta.Notes, Cover: meta.Cover,
					Groups: filtered, UpdatedAt: meta.UpdatedAt,
				})
			}
		}
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

func isValidHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// chiURLParam is a tiny wrapper to keep the imports tidy.
func chiURLParam(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}
