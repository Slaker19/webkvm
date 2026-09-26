package api

import "testing"

// V13-D-01: the tag-set intersection predicate that powers both the
// ListVMs visibility filter and requireVMAccess fallback.
func TestVMHasAnyTagFromSlice(t *testing.T) {
	allowed := map[string]bool{"prod": true, "backup": true}
	cases := []struct {
		name string
		tags []string
		want bool
	}{
		{"direct match", []string{"prod"}, true},
		{"any-of match", []string{"dev", "backup"}, true},
		{"no match", []string{"dev", "staging"}, false},
		{"empty vm tags", nil, false},
		{"empty allowlist", []string{"prod"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := allowed
			if tc.name == "empty allowlist" {
				a = map[string]bool{}
			}
			if got := vmHasAnyTagFromSlice(tc.tags, a); got != tc.want {
				t.Errorf("vmHasAnyTagFromSlice(%v) = %v, want %v", tc.tags, got, tc.want)
			}
		})
	}
}
