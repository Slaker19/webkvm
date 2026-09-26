package api

import "testing"

func TestCoverFilename_StripsCacheBusterQueryString(t *testing.T) {
	got := coverFilename("/api/covers/abc-123.jpg?v=1699999999")
	if got != "abc-123.jpg" {
		t.Errorf("coverFilename = %q, want %q", got, "abc-123.jpg")
	}
}

func TestCoverFilename_NoQueryString(t *testing.T) {
	got := coverFilename("/api/covers/abc-123.png")
	if got != "abc-123.png" {
		t.Errorf("coverFilename = %q, want %q", got, "abc-123.png")
	}
}

func TestCoverFilename_Empty(t *testing.T) {
	if got := coverFilename(""); got != "." {
		// filepath.Base("") == "." — callers must check for this, same
		// as the existing base != "." guard in DeleteCover.
		t.Errorf("coverFilename(\"\") = %q, want %q", got, ".")
	}
}

func TestCoverFilename_RejectsPathTraversalAttempt(t *testing.T) {
	// filepath.Base collapses this to the last segment; the caller
	// (DeleteCover) still validates the joined path stays inside the
	// covers dir, but coverFilename itself should never hand back a
	// traversal token unexamined.
	got := coverFilename("/api/covers/../../etc/passwd?v=1")
	if got != "passwd" {
		t.Errorf("coverFilename = %q, want %q", got, "passwd")
	}
}
