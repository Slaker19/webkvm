package remotebrowse

import (
	"context"
	"strings"
	"testing"
)

func TestBrowseValidation(t *testing.T) {
	ctx := context.Background()

	// 1. Missing host or source dir
	_, err := Browse(ctx, Request{Format: "nfs", Host: "", SourceDir: "/export"})
	if err == nil || !strings.Contains(err.Error(), "host and source_dir are required") {
		t.Fatalf("expected host required error, got %v", err)
	}

	_, err = Browse(ctx, Request{Format: "nfs", Host: "192.168.1.1", SourceDir: ""})
	if err == nil || !strings.Contains(err.Error(), "host and source_dir are required") {
		t.Fatalf("expected source_dir required error, got %v", err)
	}

	// 2. Unsupported format
	_, err = Browse(ctx, Request{Format: "ftp", Host: "192.168.1.1", SourceDir: "/export"})
	if err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("expected unsupported format error, got %v", err)
	}

	// 3. Invalid control characters in subpath
	_, err = Browse(ctx, Request{Format: "nfs", Host: "192.168.1.1", SourceDir: "/export", Subpath: "foo\nbar"})
	if err == nil || !strings.Contains(err.Error(), "invalid subpath") {
		t.Fatalf("expected invalid subpath error, got %v", err)
	}

	// 4. Invalid quotes or semicolons in cifs subpath
	_, err = Browse(ctx, Request{Format: "cifs", Host: "192.168.1.1", SourceDir: "share", Subpath: "foo;ls"})
	if err == nil || !strings.Contains(err.Error(), "invalid subpath for cifs") {
		t.Fatalf("expected invalid subpath for cifs error, got %v", err)
	}
}
