package api

import "testing"

func TestIsNewerFixSuffix(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.1.12-fix01", "0.1.12", true},
		{"v0.1.12-fix01", "0.1.12", true},
		{"0.1.12-fix02", "0.1.12-fix01", true},
		{"0.1.12-fix01", "0.1.12-fix01", false},
		{"0.1.12", "0.1.12-fix01", false},
		{"0.1.13", "0.1.12-fix01", true},
		{"0.1.12-fix", "0.1.12", true},
	}
	for _, tc := range cases {
		if got := isNewer(tc.latest, tc.current); got != tc.want {
			t.Errorf("isNewer(%q,%q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}
