package backupstore

import "testing"

// TestCompressionNormalization locks the mapping between the operator's
// free-form choice and the two supported archive codecs, plus the
// filename extension derivation and the zstd level clamp. Both the
// store (defaults/updates) and the runner (archive naming) depend on
// these being stable.
func TestCompressionNormalization(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", CompressionZstd},
		{"zstd", CompressionZstd},
		{"ZSTD", CompressionZstd},
		{"zst", CompressionZstd}, // unknown -> safe default
		{"gzip", CompressionGzip},
		{"GZip", CompressionGzip},
		{" gzip ", CompressionGzip},
		{"bogus", CompressionZstd},
	}
	for _, c := range cases {
		if got := normalizeCompression(c.in); got != c.want {
			t.Errorf("normalizeCompression(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	if got := archiveExt(CompressionZstd); got != ".tar.zst" {
		t.Errorf("archiveExt(zstd) = %q, want .tar.zst", got)
	}
	if got := archiveExt(CompressionGzip); got != ".tar.gz" {
		t.Errorf("archiveExt(gzip) = %q, want .tar.gz", got)
	}
	if got := archiveExt(""); got != ".tar.zst" {
		t.Errorf("archiveExt(empty) = %q, want .tar.zst", got)
	}

	levels := map[int]int{-5: 0, 0: 0, 1: 1, 19: 19, 22: 22, 30: 22}
	for in, want := range levels {
		if got := normalizeZstdLevel(in); got != want {
			t.Errorf("normalizeZstdLevel(%d) = %d, want %d", in, got, want)
		}
	}
}
