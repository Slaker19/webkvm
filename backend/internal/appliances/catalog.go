// Package appliances provides a curated, operator-editable catalog of
// official virtual machine disk images (QCOW2 / raw) that can be
// downloaded and deployed as a ready-to-run VM with recommended
// resources.
//
// Security: the catalog is seeded from built-in defaults whose URLs come
// only from official project mirrors. The API lets an admin edit or add
// entries, but every URL is validated against a whitelist of official
// domains (see source.go), so the backend cannot be turned into an
// SSRF/generic downloader and an admin cannot silently point a template
// at a non-official source. Downloads are still routed through the same
// DNS-rebind-safe transport used for ISO downloads, and the result is
// validated (magic bytes + minimum size) before a VM is created.
package appliances

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Appliance is one catalog entry.
type Appliance struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"` // router | home | nas | cloud | app
	URL         string `json:"url"`
	// Format is the resulting disk format: "qcow2" or "raw".
	Format string `json:"format"`
	// Compression of the download: "none", "gz", "xz" or "bz2".
	Compression string `json:"compression"`
	// SizeBytes is the expected download size (0 = unknown; the
	// deploy still validates the result with magic bytes + minimum).
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// Recommended resources.
	VCPUs  int   `json:"vcpus"`
	RAMMB  int64 `json:"ram_mb"`
	DiskGB int64 `json:"disk_gb"`
	// CloudInitSupported reports whether the image accepts NoCloud
	// seed provisioning (most cloud images do).
	CloudInitSupported bool   `json:"cloud_init_supported"`
	Notes              string `json:"notes,omitempty"`
	// Builtin marks an entry that shipped with the binary. Admins may
	// still edit or delete it, but the UI asks for a stricter double
	// confirmation. Not editable.
	Builtin bool `json:"builtin,omitempty"`
	// BuiltinOverride marks a builtin whose provision script was replaced
	// by an admin after v2 (persisted across restarts; empty = embedded).
	BuiltinOverride bool `json:"builtin_override,omitempty"`
	// BaseImageID is the ID of another appliance to use as the disk
	// image when ProvisionScript is set (e.g. an app installed on the
	// Ubuntu cloud base). When empty, URL is used directly.
	BaseImageID string `json:"base_image_id,omitempty"`
	// ProvisionScript is a bash script run on first boot (via
	// cloud-init) to install the application on the base image. Builtins
	// fall back to the copy embedded in the binary when no override is
	// stored; admins may replace it (SetProvision("")) to restore that
	// default. It is only served through the dedicated provision
	// endpoint — never in catalog listings.
	ProvisionScript string `json:"provision_script,omitempty"`
}

// MaxScriptBytes caps user-supplied provisioning scripts.
const MaxScriptBytes = 64 << 10

// NormalizeScript validates and canonicalizes a user-supplied provision
// script: size cap, no NUL bytes, and an auto-prepended #!/bin/bash
// shebang when missing.
func NormalizeScript(s string) (string, error) {
	s = strings.TrimRight(s, "\r\n\t ")
	if s == "" {
		return "", nil
	}
	if len(s) > MaxScriptBytes {
		return "", fmt.Errorf("provision script too large (max %d bytes)", MaxScriptBytes)
	}
	if strings.ContainsRune(s, '\x00') {
		return "", fmt.Errorf("provision script contains NUL bytes")
	}
	if !strings.HasPrefix(s, "#!") {
		s = "#!/bin/bash\n" + s
	}
	return s, nil
}

// Store is a persistent, on-disk JSON store of the appliance catalog.
// It mirrors the pattern used by groupsStore / firewall.Store: seeded
// from built-in defaults on first run, then editable by admins.
type Store struct {
	mu    sync.Mutex
	path  string
	items map[string]Appliance
}

// NewStore loads (or seeds) the catalog at path. If the file does not
// exist it is initialized from Defaults and persisted.
func NewStore(path string) *Store {
	s := &Store{path: path, items: map[string]Appliance{}}
	if err := s.load(); err == nil && len(s.items) > 0 {
		return s
	}
	// Seed from defaults if the file is missing/empty.
	for _, a := range Defaults {
		a.Builtin = true
		a.ProvisionScript = provisionScripts[a.ID]
		s.items[a.ID] = a
	}
	_ = s.save()
	return s
}

// layoutVersion bumps whenever embedded builtin scripts change in a way
// that must invalidate previously persisted copies. v2 drops any builtin
// script stored under v1 so updated binaries ship their fixed scripts.
const layoutVersion = 2

type storeFile struct {
	Version int         `json:"version"`
	Items   []Appliance `json:"items"`
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return err
		}
		return err
	}
	var items []Appliance
	if err := json.Unmarshal(data, &items); err != nil {
		// try v2 wrapper
		var sf storeFile
		if err2 := json.Unmarshal(data, &sf); err2 != nil || len(sf.Items) == 0 {
			return err
		}
		items = sf.Items
	}
	staleV1 := false
	{
		var probe []map[string]any
		if json.Unmarshal(data, &probe) == nil && len(probe) > 0 {
			staleV1 = true // bare array => v1 file
		}
	}
	for _, a := range items {
		// v1 files froze a copy of every builtin script at seed time;
		// discard those so updated binaries take effect (admins can
		// re-override after upgrading — overrides made under v2 persist).
		if staleV1 && a.Builtin && !a.BuiltinOverride {
			a.ProvisionScript = ""
		}
		if a.ProvisionScript == "" {
			if scr, ok := provisionScripts[a.ID]; ok {
				a.ProvisionScript = scr
			}
		}
		s.items[a.ID] = a
	}
	return nil
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	items := make([]Appliance, 0, len(s.items))
	for _, a := range s.items {
		items = append(items, a)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	sf := storeFile{Version: layoutVersion, Items: items}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns all appliances sorted by ID.
func (s *Store) List() []Appliance {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Appliance, 0, len(s.items))
	for _, a := range s.items {
		// Strip the provision script from API responses.
		a.ProvisionScript = ""
		items = append(items, a)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

// Get returns an appliance by ID.
func (s *Store) Get(id string) (Appliance, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.items[id]
	if !ok {
		return Appliance{}, false
	}
	a.ProvisionScript = ""
	return a, true
}

// GetProvision returns the effective provision script for an ID (used
// by the deploy path and the provision endpoint): the stored script
// (custom or admin override) with a fallback to the embedded copy for
// builtins.
func (s *Store) GetProvision(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.items[id]
	if !ok {
		return "", false
	}
	if a.ProvisionScript == "" {
		if scr, ok := provisionScripts[a.ID]; ok {
			return scr, true
		}
	}
	return a.ProvisionScript, true
}

// ProvisionInfo reports whether an appliance is builtin and whether its
// script currently overrides the embedded default.
func (s *Store) ProvisionInfo(id string) (script string, isBuiltin bool, overridden bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, exists := s.items[id]
	if !exists {
		return "", false, false, false
	}
	script = a.ProvisionScript
	isBuiltin = a.Builtin
	embedded, hasEmbedded := provisionScripts[id]
	overridden = hasEmbedded && script != "" && script != embedded
	if script == "" && hasEmbedded {
		script = embedded
	}
	return script, isBuiltin, overridden, true
}

// SetProvision stores (or clears, with "") the provision script of an
// appliance. Builtins with an empty script fall back to the embedded
// default at read time.
func (s *Store) SetProvision(id, script string) error {
	norm, err := NormalizeScript(script)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.items[id]
	if !ok {
		return fmt.Errorf("appliance %q not found", id)
	}
	if norm != "" && a.Builtin {
		a.BuiltinOverride = true
	} else if a.Builtin {
		// cleared -> fall back to the embedded default again
		a.BuiltinOverride = false
	}
	a.ProvisionScript = norm
	s.items[id] = a
	return s.save()
}

// Create adds a new appliance. It rejects a duplicate ID and validates
// the source URL against the official whitelist.
func (s *Store) Create(a Appliance) error {
	if a.ID == "" || a.Name == "" || a.URL == "" {
		return fmt.Errorf("id, name and url are required")
	}
	if err := ValidateSourceURL(a.URL); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[a.ID]; exists {
		return fmt.Errorf("an appliance with id %q already exists", a.ID)
	}
	norm, err := NormalizeScript(a.ProvisionScript)
	if err != nil {
		return err
	}
	a.ProvisionScript = norm
	a.Builtin = false
	s.items[a.ID] = a
	return s.save()
}

// Update replaces an existing appliance (only the editable fields). The
// provision script is preserved from the existing entry.
func (s *Store) Update(id string, a Appliance) error {
	if a.URL != "" {
		if err := ValidateSourceURL(a.URL); err != nil {
			return err
		}
	}
	s.mu.Lock()
	existing, ok := s.items[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("appliance %q not found", id)
	}
	// Preserve fields that are not editable via the API.
	a.ID = id
	a.Builtin = existing.Builtin
	a.ProvisionScript = existing.ProvisionScript
	s.items[id] = a
	err := s.save()
	s.mu.Unlock()
	return err
}

// Delete removes an appliance.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return fmt.Errorf("appliance %q not found", id)
	}
	delete(s.items, id)
	return s.save()
}
