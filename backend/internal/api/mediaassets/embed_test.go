package mediaassets

import (
	"testing"
)

func TestNamesAndRead(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("expected at least one embedded media asset")
	}

	foundLinux := false
	for _, name := range names {
		if name == "os-linux.svg" {
			foundLinux = true
		}
		data, err := Read(name)
		if err != nil {
			t.Fatalf("failed to read embedded asset %s: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("asset %s is empty", name)
		}
	}

	if !foundLinux {
		t.Error("expected os-linux.svg to be present in embedded assets")
	}

	// Nonexistent asset should return error
	if _, err := Read("nonexistent.png"); err == nil {
		t.Error("expected error reading nonexistent asset")
	}
}
