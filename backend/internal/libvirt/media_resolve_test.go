package libvirt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

func TestResolveMediaPath(t *testing.T) {
	tmpDir := t.TempDir()

	isoPoolDir := filepath.Join(tmpDir, "webkvm-isos")
	diskPoolDir := filepath.Join(tmpDir, "webkvm-disks")
	otherIsoDir := filepath.Join(tmpDir, "other-isos")
	if err := os.MkdirAll(isoPoolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(diskPoolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherIsoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create test files
	alpinePath := filepath.Join(isoPoolDir, "alpine-virt.iso")
	if err := os.WriteFile(alpinePath, []byte("fake-iso"), 0o644); err != nil {
		t.Fatal(err)
	}

	diskImgPath := filepath.Join(diskPoolDir, "appliance.img")
	if err := os.WriteFile(diskImgPath, []byte("fake-img"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create duplicate in otherIsoDir for ambiguity test
	dupAlpinePath := filepath.Join(otherIsoDir, "ambiguous.iso")
	if err := os.WriteFile(dupAlpinePath, []byte("fake-dup"), 0o644); err != nil {
		t.Fatal(err)
	}
	dupAlpine2Path := filepath.Join(isoPoolDir, "ambiguous.iso")
	if err := os.WriteFile(dupAlpine2Path, []byte("fake-dup2"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		DataDir: tmpDir,
	}

	// Connector with nil conn but mockable via pools
	c := &Connector{
		cfg: cfg,
	}

	// 1. Empty input
	got, err := c.resolveMediaPath("")
	if err != nil || got != "" {
		t.Fatalf("resolveMediaPath(\"\") = (%q, %v), want (\"\", nil)", got, err)
	}

	// 2. Absolute existing file
	got, err = c.resolveMediaPath(alpinePath)
	if err != nil || got != alpinePath {
		t.Fatalf("resolveMediaPath(%q) = (%q, %v), want (%q, nil)", alpinePath, got, err, alpinePath)
	}

	// 3. Absolute non-existent file
	missingPath := filepath.Join(tmpDir, "nonexistent.iso")
	got, err = c.resolveMediaPath(missingPath)
	if err == nil {
		t.Fatalf("resolveMediaPath(%q) expected error, got %q", missingPath, got)
	}

	// 4. Absolute directory
	got, err = c.resolveMediaPath(isoPoolDir)
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("resolveMediaPath(%q) expected directory error, got (%q, %v)", isoPoolDir, got, err)
	}

	// Test relative resolution via custom storage pool list
	// We verify resolveMediaPath logic when pools are available
	pools := []models.StoragePool{
		{Name: "webkvm-isos", Path: isoPoolDir, Purpose: PoolPurposeISO},
		{Name: "other-isos", Path: otherIsoDir, Purpose: PoolPurposeISO},
		{Name: "webkvm-disks", Path: diskPoolDir, Purpose: PoolPurposeDisk},
	}

	// Helper to run resolution against specific pool set
	resolveWithPools := func(media string, poolList []models.StoragePool) (string, error) {
		var matches []string
		var matchPools []string

		for _, p := range poolList {
			if p.Purpose == PoolPurposeISO && p.Path != "" {
				target := filepath.Join(p.Path, media)
				if stat, err := os.Stat(target); err == nil && !stat.IsDir() {
					matches = append(matches, target)
					matchPools = append(matchPools, p.Name)
				}
			}
		}

		if len(matches) == 0 {
			for _, p := range poolList {
				if p.Purpose != PoolPurposeISO && p.Path != "" {
					target := filepath.Join(p.Path, media)
					if stat, err := os.Stat(target); err == nil && !stat.IsDir() {
						matches = append(matches, target)
						matchPools = append(matchPools, p.Name)
					}
				}
			}
		}

		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("ambiguous media name %q found in multiple storage pools (%s); specify the full path",
				media, strings.Join(matchPools, ", "))
		}
		return "", fmt.Errorf("media file %q not found in any storage pool", media)
	}
	resolved, err := resolveWithPools("alpine-virt.iso", pools)
	if err != nil || resolved != alpinePath {
		t.Fatalf("resolveWithPools(alpine-virt.iso) = (%q, %v), want %q", resolved, err, alpinePath)
	}

	// 6. Relative file in non-ISO pool (fallback pass)
	resolved, err = resolveWithPools("appliance.img", pools)
	if err != nil || resolved != diskImgPath {
		t.Fatalf("resolveWithPools(appliance.img) = (%q, %v), want %q", resolved, err, diskImgPath)
	}

	// 7. Ambiguous file in multiple pools
	_, err = resolveWithPools("ambiguous.iso", pools)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("resolveWithPools(ambiguous.iso) expected ambiguity error, got %v", err)
	}

	// 8. Not found anywhere
	_, err = resolveWithPools("ghost.iso", pools)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("resolveWithPools(ghost.iso) expected not found error, got %v", err)
	}
}
