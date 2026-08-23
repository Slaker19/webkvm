package appliances

import (
	"os"
	"path/filepath"
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

func TestValidateSourceURL(t *testing.T) {
	valid := []string{
		"https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		"https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2",
		"https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2",
		"https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2",
		"https://download.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-44-1.7.x86_64.qcow2",
		"https://geo.mirror.pkgbuild.com/images/latest/Arch-Linux-x86_64-cloudimg.qcow2",
		"https://github.com/home-assistant/operating-system/releases/download/18.2/haos_ova-18.2.qcow2.xz",
		"https://mirror.opnsense.org/releases/26.7/OPNsense-26.7-vga-amd64.img.bz2",
	}
	for _, u := range valid {
		if err := ValidateSourceURL(u); err != nil {
			t.Errorf("expected %q to be valid, got %v", u, err)
		}
	}

	invalid := []string{
		"http://cloud-images.ubuntu.com/x.img", // not https
		"https://evil.example.com/x.qcow2",     // unknown host
		"https://cloud-images.ubuntu.com.evil.com/x.qcow2", // hostname mismatch
		"ftp://cloud-images.ubuntu.com/x.img", // not http(s)
	}
	for _, u := range invalid {
		if err := ValidateSourceURL(u); err == nil {
			t.Errorf("expected %q to be rejected", u)
		}
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
