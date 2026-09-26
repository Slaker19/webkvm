package cloudinit

import (
	"strings"
	"testing"
)

func TestSnippetsBuiltinPresets(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSnippetStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	list := store.List()
	if len(list) < 7 {
		t.Fatalf("expected at least 7 built-in presets, got %d", len(list))
	}
	docker, ok := store.Get("preset-docker")
	if !ok || !docker.IsPreset {
		t.Fatalf("expected preset-docker to exist and be marked as preset")
	}
	if !strings.Contains(docker.Content, "docker-ce") {
		t.Errorf("expected docker-ce in preset-docker content, got:\n%s", docker.Content)
	}
	nginx, ok := store.Get("preset-nginx")
	if !ok || !nginx.IsPreset {
		t.Fatalf("expected preset-nginx to exist and be marked as preset")
	}
	if !strings.Contains(nginx.Content, "nginx") {
		t.Errorf("expected nginx in preset-nginx content, got:\n%s", nginx.Content)
	}
	wireguard, ok := store.Get("preset-wireguard")
	if !ok || !wireguard.IsPreset {
		t.Fatalf("expected preset-wireguard to exist and be marked as preset")
	}
	if !strings.Contains(wireguard.Content, "wireguard") {
		t.Errorf("expected wireguard in preset-wireguard content, got:\n%s", wireguard.Content)
	}
}

func TestSnippetsCustomCRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSnippetStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sn, err := store.Create(Snippet{
		Name:    "Custom Script",
		Type:    "user-data",
		Content: "#cloud-config\nruncmd:\n  - echo hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sn.ID == "" {
		t.Fatal("expected generated ID")
	}
	fetched, ok := store.Get(sn.ID)
	if !ok || fetched.Name != "Custom Script" {
		t.Fatalf("expected to fetch created snippet, got %+v", fetched)
	}

	// Update
	updated, err := store.Update(Snippet{
		ID:      sn.ID,
		Name:    "Updated Script",
		Content: "#cloud-config\nruncmd:\n  - echo updated\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Updated Script" {
		t.Errorf("expected updated name, got %s", updated.Name)
	}

	// Persistence across re-open
	store2, err := NewSnippetStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	fetched2, ok := store2.Get(sn.ID)
	if !ok || fetched2.Name != "Updated Script" {
		t.Fatalf("snippet failed to persist across re-open: %+v", fetched2)
	}

	// Delete
	if err := store.Delete(sn.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(sn.ID); ok {
		t.Fatal("expected snippet to be deleted")
	}
}

func TestCannotMutatePresetSnippets(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewSnippetStore(dir)
	_, err := store.Update(Snippet{ID: "preset-docker", Name: "Hacked"})
	if err == nil {
		t.Fatal("expected error when trying to update preset snippet")
	}
	err = store.Delete("preset-docker")
	if err == nil {
		t.Fatal("expected error when trying to delete preset snippet")
	}
}

func TestExpandTemplate(t *testing.T) {
	raw := "User is {{ .VM.User }} on host {{ .VM.Hostname }} with IP {{ .VM.IP }}"
	var vars TemplateVars
	vars.VM.User = "ubuntu"
	vars.VM.Hostname = "srv-01"
	vars.VM.IP = "192.168.1.50"

	expanded, err := ExpandTemplate(raw, vars)
	if err != nil {
		t.Fatal(err)
	}
	expected := "User is ubuntu on host srv-01 with IP 192.168.1.50"
	if expanded != expected {
		t.Errorf("got %q, want %q", expanded, expected)
	}
}

func TestBuildUserDataWithCustomContent(t *testing.T) {
	cfg := Config{
		CustomUserData: "#cloud-config\nruncmd:\n  - echo direct\n",
	}
	ud := BuildUserData(cfg)
	if !strings.Contains(ud, "echo direct") {
		t.Errorf("custom user-data missing from output:\n%s", ud)
	}

	// With user and custom data
	cfg2 := Config{
		User:           "demo",
		Password:       "secret123",
		CustomUserData: "runcmd:\n  - systemctl restart myapp\n",
	}
	ud2 := BuildUserData(cfg2)
	if !strings.Contains(ud2, "users:\n  - name: demo") || !strings.Contains(ud2, "systemctl restart myapp") {
		t.Errorf("merged user-data malformed:\n%s", ud2)
	}
}
