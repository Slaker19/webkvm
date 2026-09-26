package appliances

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewStoreSeedsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "appliances.json")
	s := NewStore(path)

	// The store must be seeded with the built-in defaults.
	items := s.List()
	if len(items) < len(Defaults) {
		t.Fatalf("expected at least %d seeded appliances, got %d", len(Defaults), len(items))
	}
	// Every default must be marked builtin.
	for _, a := range items {
		if !a.Builtin {
			t.Errorf("appliance %q should be builtin", a.ID)
		}
	}
	// The file must be persisted.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected appliances.json to be persisted: %v", err)
	}
}

func TestStoreCreateUpdateDelete(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "appliances.json"))

	// Create.
	app := Appliance{
		ID:          "my-custom",
		Name:        "Custom",
		Category:    "cloud",
		URL:         "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		Format:      "qcow2",
		Compression: "none",
		VCPUs:       1,
		RAMMB:       1024,
		DiskGB:      5,
	}
	if err := s.Create(app); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if _, ok := s.Get("my-custom"); !ok {
		t.Fatal("created appliance not found")
	}

	// Duplicate ID rejected.
	if err := s.Create(app); err == nil {
		t.Fatal("expected duplicate ID error")
	}

	// Update.
	app2 := app
	app2.Name = "Custom 2"
	app2.VCPUs = 2
	if err := s.Update("my-custom", app2); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	got, _ := s.Get("my-custom")
	if got.Name != "Custom 2" || got.VCPUs != 2 {
		t.Fatalf("update not applied: %+v", got)
	}

	// Delete.
	if err := s.Delete("my-custom"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, ok := s.Get("my-custom"); ok {
		t.Fatal("appliance still present after delete")
	}
	// Deleting a missing appliance errors.
	if err := s.Delete("my-custom"); err == nil {
		t.Fatal("expected delete error for missing appliance")
	}
}


func TestProvisionScriptPreservedInUpdate(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "appliances.json"))

	// wordpress is a builtin app with an embedded provision script.
	before, ok := s.GetProvision("wordpress")
	if !ok || before == "" {
		t.Fatal("expected wordpress to have an embedded provision script")
	}

	// Update the URL; the provision script must be preserved.
	wp, _ := s.Get("wordpress")
	wp.Name = "WordPress Updated"
	if err := s.Update("wordpress", wp); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	after, ok := s.GetProvision("wordpress")
	if !ok || after != before {
		t.Fatal("provision script lost after update")
	}

	// Provision script must not be exposed via the API-facing Get/List.
	if got, _ := s.Get("wordpress"); got.ProvisionScript != "" {
		t.Fatal("provision script must not be exposed via Get")
	}
}

func TestHelperScriptsMetadataAndNormalization(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "appliances.json"))

	pihole, ok := s.Get("pihole")
	if !ok {
		t.Fatal("expected pihole helper app to be in store")
	}
	if !pihole.IsHelperScript || pihole.DefaultType != "container" || pihole.Port != 80 {
		t.Errorf("unexpected pihole metadata: %+v", pihole)
	}

	// Test NormalizeScript
	norm, err := NormalizeScript("apt-get update\napt-get install -y curl\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(norm, "#!/bin/bash\n") {
		t.Errorf("expected auto-prepended shebang, got: %s", norm)
	}

	// Reject NUL bytes
	_, err = NormalizeScript("echo \x00 bad")
	if err == nil {
		t.Fatal("expected NUL byte rejection")
	}
}
