package api

import (
	"testing"
)

// V13-BCK-06: a credential omitted (nil), blank, or masked ("••••",
// "****") preserves the stored secret; only a real value replaces it.
// This is what lets the Edit form render a masked placeholder without
// nuking the stored password/keys when the user saves without touching
// them.
func TestResolvedSecret_PreservesBlankAndMask(t *testing.T) {
	const stored = "S3cr3t!orig"
	ptr := func(s string) *string { return &s }

	cases := []struct {
		name string
		in   *string
		want string
	}{
		{"omitted (nil) preserves", nil, stored},
		{"blank preserves", ptr(""), stored},
		{"spaces preserve", ptr("   "), stored},
		{"bullet mask preserves", ptr("••••••••"), stored},
		{"star mask preserves", ptr("*******"), stored},
		{"mixed mask preserves", ptr("••**••**"), stored},
		{"new value replaces", ptr("fresh"), "fresh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvedSecret(tc.in, stored); got != tc.want {
				t.Errorf("resolvedSecret(%v, %q) = %q, want %q", tc.in, stored, got, tc.want)
			}
		})
	}
}

func TestIsSecretMask(t *testing.T) {
	for _, masked := range []string{"•", "••••", "****", "••**"} {
		if !isSecretMask(masked) {
			t.Errorf("isSecretMask(%q) = false, want true", masked)
		}
	}
	for _, real := range []string{"hunter2", "a", "•real•", " "} {
		if isSecretMask(real) {
			t.Errorf("isSecretMask(%q) = true, want false", real)
		}
	}
}
