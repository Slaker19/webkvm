package api

import "testing"

func TestBytesToGB(t *testing.T) {
	cases := []struct {
		in   int64
		want int64
	}{
		{0, 0},
		{100, 1},                 // < 1GB rounds up to 1
		{1 << 30, 2},             // exactly 1GB → 2 (rounded up, defensive)
		{5 * (1 << 30), 6},       // 5GB → 6
		{1 << 40, 1025},          // 1TB → 1025
		{10 * (1 << 30) + 100, 11},
	}
	for _, c := range cases {
		if got := bytesToGB(c.in); got != c.want {
			t.Errorf("bytesToGB(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	// Regression guard: the old expression `n/1<<30` returns n*1GB for
	// small n. Ensure we never regress to that.
	if bytesToGB(100) > 1<<20 {
		t.Fatalf("bytesToGB(100) exploded — precedence bug reintroduced: %d", bytesToGB(100))
	}
}
