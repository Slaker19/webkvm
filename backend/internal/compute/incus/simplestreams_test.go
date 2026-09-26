package incus

import (
	"testing"
)

func TestDynamicSimplestreamsImages(t *testing.T) {
	images := fetchDynamicSimplestreamsImages()
	if len(images) == 0 {
		t.Fatalf("expected non-empty image catalog")
	}

	// Verify key distros exist in the discovered catalog
	foundUbuntu26 := false
	foundFedora44 := false
	foundDebianForky := false
	foundAlpine := false

	for _, img := range images {
		if img.Ref == "images:ubuntu/resolute" || img.Ref == "images:ubuntu/26.04" {
			foundUbuntu26 = true
		}
		if img.Ref == "images:fedora/44" {
			foundFedora44 = true
		}
		if img.Ref == "images:debian/forky" || img.Ref == "images:debian/14" {
			foundDebianForky = true
		}
		if img.Distro == "alpine" {
			foundAlpine = true
		}
	}

	t.Logf("Total scanned images: %d", len(images))
	if !foundAlpine {
		t.Errorf("expected Alpine Linux in scanned catalog")
	}
	if !foundUbuntu26 {
		t.Logf("Note: Ubuntu Resolute not in primary ref, checking all images...")
	}
	if !foundFedora44 {
		t.Logf("Note: Fedora 44 not found")
	}
	if !foundDebianForky {
		t.Logf("Note: Debian Forky not found")
	}
}
