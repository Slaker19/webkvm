package appliances

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeStoreFile persists a storeFile (v2) with the given items for tests.
func writeStoreFile(t *testing.T, path string, version int, items []Appliance) {
	t.Helper()
	sf := storeFile{Version: version, Items: items}
	b, _ := json.MarshalIndent(sf, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestMigrationV3_RefreshesBuiltinDefaults: un store v2 con URLs viejas/
// rotas debe, al cargar con layout v3, adoptar los defaults del binario
// (URL, SizeBytes, resources, compression, notes) para builtins NO
// customizados.
func TestMigrationV3_RefreshesBuiltinDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "appliances.json")

	// Simulamos un store v2 con la URL vieja/rota de debian-13 y un
	// SizeBytes viejo.
	var deb Appliance
	for _, d := range Defaults {
		if d.ID == "debian-13" {
			deb = d
			break
		}
	}
	if deb.ID == "" {
		t.Fatal("debian-13 not found in Defaults")
	}
	stale := deb
	stale.Builtin = true
	stale.URL = "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2"
	stale.SizeBytes = 325000000

	writeStoreFile(t, path, 2, []Appliance{stale})

	s := NewStore(path)
	got, ok := s.Get("debian-13")
	if !ok {
		t.Fatal("debian-13 missing after load")
	}
	// la URL rota debe haber sido reemplazada por el default actual
	if got.URL != deb.URL {
		t.Errorf("URL = %q, want %q (default del binario)", got.URL, deb.URL)
	}
	if got.SizeBytes != deb.SizeBytes {
		t.Errorf("SizeBytes = %d, want %d", got.SizeBytes, deb.SizeBytes)
	}
	if got.VCPUs != deb.VCPUs || got.RAMMB != deb.RAMMB || got.DiskGB != deb.DiskGB {
		t.Errorf("resources not migrated: got %d/%d/%d want %d/%d/%d",
			got.VCPUs, got.RAMMB, got.DiskGB, deb.VCPUs, deb.RAMMB, deb.DiskGB)
	}
	if got.Compression != deb.Compression {
		t.Errorf("compression = %q, want %q", got.Compression, deb.Compression)
	}
}

// TestMigrationV3_PreservesCustomized: un builtin con Customized=true
// (admin lo editó por la API) NO debe ver sus defaults sobreescritos —
// la URL que el admin eligió se conserva.
func TestMigrationV3_PreservesCustomized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "appliances.json")

	var deb Appliance
	for _, d := range Defaults {
		if d.ID == "debian-13" {
			deb = d
			break
		}
	}
	custom := deb
	custom.Builtin = true
	custom.URL = "https://custom.example/mirror/debian13.qcow2"
	custom.Customized = true

	writeStoreFile(t, path, 2, []Appliance{custom})

	s := NewStore(path)
	got, _ := s.Get("debian-13")
	if got.URL != custom.URL {
		t.Errorf("customized URL overwritten: got %q want %q", got.URL, custom.URL)
	}
}

// TestMigrationV3_PreservesScriptOverride: un builtin con
// BuiltinOverride=true (script reemplazado por admin) conserva su script
// Y su URL, aunque no tenga Customized (caso v2 donde solo el script fue
// editado).
func TestMigrationV3_PreservesScriptOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "appliances.json")

	var deb Appliance
	for _, d := range Defaults {
		if d.ID == "debian-13" {
			deb = d
			break
		}
	}
	override := deb
	override.Builtin = true
	override.URL = "https://custom.example/debian13.qcow2"
	override.BuiltinOverride = true
	override.ProvisionScript = "#!/bin/bash\necho custom\n"

	writeStoreFile(t, path, 2, []Appliance{override})

	s := NewStore(path)
	got, _ := s.Get("debian-13")
	scr, _ := s.GetProvision("debian-13")
	if scr != override.ProvisionScript {
		t.Errorf("override script lost: got %q want %q", scr, override.ProvisionScript)
	}
	if got.URL != override.URL {
		t.Errorf("override URL lost: got %q want %q", got.URL, override.URL)
	}
}

// TestMigrationV3_KeepsNonBuiltinUntouched: entries no-builtin (custom)
// jamás se tocan.
func TestMigrationV3_KeepsNonBuiltinUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "appliances.json")

	custom := Appliance{
		ID: "my-custom", Name: "Mi App", URL: "https://custom.example/a.qcow2",
		Format: "qcow2", Compression: "none", SizeBytes: 12345,
		Builtin: false,
	}
	writeStoreFile(t, path, 2, []Appliance{custom})

	s := NewStore(path)
	got, _ := s.Get("my-custom")
	if got.URL != custom.URL || got.SizeBytes != custom.SizeBytes {
		t.Errorf("non-builtin modified: %+v", got)
	}
}

// TestMigrationV3_PersistsVersion3: tras cargar, el fichero se reescribe
// con version 3.
func TestMigrationV3_PersistsVersion3(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "appliances.json")

	var deb Appliance
	for _, d := range Defaults {
		if d.ID == "debian-13" {
			deb = d
		}
	}
	deb.Builtin = true
	writeStoreFile(t, path, 2, []Appliance{deb})

	NewStore(path)
	var sf storeFile
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &sf); err != nil {
		t.Fatal(err)
	}
	if sf.Version != 3 {
		t.Errorf("version after migration = %d, want 3", sf.Version)
	}
}

// TestUpdateMarksCustomized: un Update vía API marca Customized para
// que futuras migraciones no lo pisen.
func TestUpdateMarksCustomized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "appliances.json")
	s := NewStore(path)

	upd := Appliance{Name: "Debian 13 (trixie)", Description: "x", URL: "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.qcow2", Format: "qcow2"}
	if err := s.Update("debian-13", upd); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("debian-13")
	if !got.Customized {
		t.Error("Update did not mark Customized")
	}
}
