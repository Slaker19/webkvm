// Package configstore is a typed, persistent, hot-reloadable key-value
// configuration store backing the Settings page. Values are persisted to
// {DataDir}/config.json and exposed to the UI as a JSON schema so the
// editor can render type-aware inputs without hardcoding field names.
//
// Design notes
//
//   - The store owns the schema. The schema is a list of "fields", each
//     with a Key, Section, Type, Default, Description, HotReload flag and
//     optional Enum. Adding a new configurable knob is a one-line change
//     in defaults.go — the UI picks it up automatically.
//
//   - HotReload=true means the change applies immediately (the field
//     has a live consumer that re-reads on every access). Otherwise the
//     store marks the value as "restart-required" and exposes a flag in
//     GET /api/settings so the UI can show a banner.
//
//   - The store does NOT pull config from the environment anymore. The
//     legacy env var path in package config is preserved for backward
//     compatibility but the canonical source of truth is this store.
//     A migration step in cmd/server copies env values into the store
//     on first boot.
package configstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// FieldType is the wire type the editor uses to render the right input.
type FieldType string

const (
	FieldBool     FieldType = "bool"
	FieldInt      FieldType = "int"
	FieldString   FieldType = "string"
	FieldDuration FieldType = "duration"
	FieldEnum     FieldType = "enum"
	FieldList     FieldType = "list"     // []string
	FieldSecret   FieldType = "secret"   // string, never returned in plain text
	FieldJSON     FieldType = "json"     // raw JSON object/array
)

// Field is a single configurable setting.
type Field struct {
	Key         string      `json:"key"`
	Section     string      `json:"section"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Type        FieldType   `json:"type"`
	Default     interface{} `json:"default"`
	Enum        []string    `json:"enum,omitempty"`    // for FieldEnum
	HotReload   bool        `json:"hot_reload"`        // applies without restart
	Advanced    bool        `json:"advanced,omitempty"` // collapsed by default in UI
	Min         *float64    `json:"min,omitempty"`     // for FieldInt
	Max         *float64    `json:"max,omitempty"`     // for FieldInt
	Placeholder string      `json:"placeholder,omitempty"`
}

// Schema is the full set of configurable fields.
type Schema struct {
	Fields []Field `json:"fields"`
}

// ByKey returns the field with the given key, or false if missing.
func (s Schema) ByKey(key string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Keys returns the field keys in stable order.
func (s Schema) Keys() []string {
	out := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		out = append(out, f.Key)
	}
	sort.Strings(out)
	return out
}

// Set is a snapshot of every value. Keys are field keys; values match
// the field's Type. Missing keys are filled with their default.
type Set map[string]interface{}

// File is the on-disk JSON shape. Versioning lets us migrate in the future.
type File struct {
	Version int       `json:"version"`
	Values  Set       `json:"values"`
	SavedAt time.Time `json:"saved_at"`
}

// Store is the in-memory + on-disk config store. Safe for concurrent use.
type Store struct {
	mu            sync.RWMutex
	schema        Schema
	path          string
	values        Set
	pendingBefore map[string]struct{} // non-hot-reload keys changed since last restart
}

// New loads (or creates) the store at {dataDir}/config.json.
func New(dataDir string, schema Schema) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("configstore: mkdir: %w", err)
	}
	s := &Store{
		schema:        schema,
		path:          filepath.Join(dataDir, "config.json"),
		values:        Set{},
		pendingBefore: map[string]struct{}{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Schema returns the field schema. The returned slice is a copy; callers
// may not mutate the store's internal schema.
func (s *Store) Schema() Schema {
	out := make([]Field, len(s.schema.Fields))
	copy(out, s.schema.Fields)
	return Schema{Fields: out}
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		// First run: fill with defaults and persist so the file exists.
		s.values = s.defaults()
		return s.saveLocked()
	}
	if err != nil {
		return fmt.Errorf("configstore: read %s: %w", s.path, err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("configstore: parse %s: %w", s.path, err)
	}
	s.values = f.Values
	s.fillDefaults()
	return nil
}

// save writes to disk, locking internally. Used by Load and the public API.
func (s *Store) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked writes to disk assuming the caller already holds the lock
// (write or read).
func (s *Store) saveLocked() error {
	values := make(Set, len(s.values))
	for k, v := range s.values {
		values[k] = v
	}

	f := File{Version: 1, Values: values, SavedAt: time.Now().UTC()}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("configstore: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("configstore: write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("configstore: rename: %w", err)
	}
	return nil
}

func (s *Store) defaults() Set {
	out := Set{}
	for _, f := range s.schema.Fields {
		out[f.Key] = f.Default
	}
	return out
}

// fillDefaults adds any missing keys with their default. Used after
// loading an existing config file that pre-dates a new field.
func (s *Store) fillDefaults() {
	for _, f := range s.schema.Fields {
		if _, ok := s.values[f.Key]; !ok {
			s.values[f.Key] = f.Default
		}
	}
}

// Get returns the value for key. Returns the default if the key is
// unknown or the stored value has the wrong type.
func (s *Store) Get(key string) (interface{}, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.schema.ByKey(key)
	if !ok {
		return nil, false
	}
	v, present := s.values[key]
	if !present {
		return f.Default, true
	}
	return v, true
}

// GetString returns the value coerced to string, or "" if missing/wrong type.
func (s *Store) GetString(key string) string {
	v, _ := s.Get(key)
	if str, ok := v.(string); ok {
		return str
	}
	return ""
}

// GetInt returns the value coerced to int, or 0 if missing/wrong type.
func (s *Store) GetInt(key string) int {
	v, _ := s.Get(key)
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// GetBool returns the value coerced to bool.
func (s *Store) GetBool(key string) bool {
	v, _ := s.Get(key)
	b, _ := v.(bool)
	return b
}

// GetDuration returns the value coerced to time.Duration. Accepts
// nanoseconds (as JSON numbers) or a Go duration string ("5m", "1h").
func (s *Store) GetDuration(key string) time.Duration {
	v, _ := s.Get(key)
	switch n := v.(type) {
	case float64:
		return time.Duration(n)
	case string:
		d, _ := time.ParseDuration(n)
		return d
	}
	return 0
}

// GetList returns the value as []string.
func (s *Store) GetList(key string) []string {
	v, _ := s.Get(key)
	switch list := v.(type) {
	case []string:
		return list
	case []interface{}:
		out := make([]string, 0, len(list))
		for _, x := range list {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// Snapshot returns a deep copy of all current values, suitable for
// sending to the UI. Secret fields are masked.
func (s *Store) Snapshot() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]interface{}, len(s.values))
	for _, f := range s.schema.Fields {
		v, ok := s.values[f.Key]
		if !ok {
			v = f.Default
		}
		if f.Type == FieldSecret {
			if str, ok := v.(string); ok && str != "" {
				out[f.Key] = "********"
			} else {
				out[f.Key] = ""
			}
		} else {
			out[f.Key] = v
		}
	}
	return out
}

// PendingRestart returns the set of keys whose most recent change
// requires a restart to take effect. Keys are tracked in-memory from
// the last SetMany call. After a restart the set starts empty (the
// values are already in effect), so the banner disappears.
func (s *Store) PendingRestart() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.pendingBefore))
	for k := range s.pendingBefore {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalDefault(f Field, v interface{}) bool {
	if v == nil && f.Default == nil {
		return true
	}
	a, _ := json.Marshal(v)
	b, _ := json.Marshal(f.Default)
	return string(a) == string(b)
}

// SetMany validates and applies a batch of changes. Returns the list of
// keys that were applied and the list of keys that failed validation.
// On any error no values are persisted (all-or-nothing semantics).
func (s *Store) SetMany(in Set) (applied []string, failed map[string]string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	backup := make(Set, len(s.values))
	for k, v := range s.values {
		backup[k] = v
	}
	failed = map[string]string{}
	for k, v := range in {
		f, ok := s.schema.ByKey(k)
		if !ok {
			failed[k] = "unknown setting"
			continue
		}
		if errStr := validateValue(f, v); errStr != "" {
			failed[k] = errStr
			continue
		}
		s.values[k] = coerce(f, v)
		applied = append(applied, k)
	}
	if len(failed) > 0 {
		// Rollback.
		s.values = backup
		return nil, failed, nil
	}
	if err := s.saveLocked(); err != nil {
		s.values = backup
		return nil, map[string]string{"_": err.Error()}, err
	}
	for _, k := range applied {
		f, ok := s.schema.ByKey(k)
		if ok && !f.HotReload {
			s.pendingBefore[k] = struct{}{}
		}
	}
	sort.Strings(applied)
	return applied, nil, nil
}

// ClearPending removes all pending-restart tracking. Called by
// ApplyRestartSettings before the process restarts so the next
// boot doesn't show stale pending flags.
func (s *Store) ClearPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingBefore = map[string]struct{}{}
}

// Reset restores all values to their defaults and persists.
func (s *Store) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = s.defaults()
	s.pendingBefore = map[string]struct{}{}
	return s.saveLocked()
}

func validateValue(f Field, v interface{}) string {
	if v == nil {
		return ""
	}
	switch f.Type {
	case FieldBool:
		if _, ok := v.(bool); !ok {
			return "expected bool"
		}
	case FieldInt:
		var n float64
		switch x := v.(type) {
		case float64:
			n = x
		case int:
			n = float64(x)
		default:
			return "expected int"
		}
		if f.Min != nil && n < *f.Min {
			return fmt.Sprintf("must be >= %v", *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return fmt.Sprintf("must be <= %v", *f.Max)
		}
	case FieldString, FieldSecret, FieldDuration:
		if _, ok := v.(string); !ok {
			return "expected string"
		}
		if f.Type == FieldDuration {
			if _, ok := v.(string); ok {
				if _, err := time.ParseDuration(v.(string)); err != nil {
					return "invalid duration (e.g. 5m, 1h30m)"
				}
			}
		}
	case FieldEnum:
		str, ok := v.(string)
		if !ok {
			return "expected string"
		}
		valid := false
		for _, e := range f.Enum {
			if e == str {
				valid = true
				break
			}
		}
		if !valid {
			return "value not in enum: " + str
		}
	case FieldList:
		switch v.(type) {
		case []interface{}, []string:
		default:
			return "expected list of strings"
		}
	}
	return ""
}

func coerce(f Field, v interface{}) interface{} {
	switch f.Type {
	case FieldInt:
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	case FieldList:
		if list, ok := v.([]interface{}); ok {
			out := make([]string, 0, len(list))
			for _, x := range list {
				if str, ok := x.(string); ok {
					out = append(out, str)
				}
			}
			return out
		}
	}
	return v
}

// Path returns the on-disk location of the config file. Useful for logs.
func (s *Store) Path() string {
	return s.path
}
