package nodes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryCreateLocal(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("list = %d, want 1", len(got))
	}
	if !got[0].IsLocal() {
		t.Fatal("first node should be local")
	}
	if got[0].URI != "qemu:///system" {
		t.Fatalf("uri = %q", got[0].URI)
	}
}

func TestRegistryCreateRemote(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	n, err := r.Create("node-2", "qemu+ssh://root@10.0.0.2/system")
	if err != nil {
		t.Fatal(err)
	}
	if n.IsLocal() {
		t.Fatal("expected remote node")
	}
	if n.Name != "node-2" {
		t.Fatalf("name = %q", n.Name)
	}
	if r.nodes[n.ID] == nil {
		t.Fatal("not in map")
	}
}

func TestRegistryUpdate(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	n, _ := r.Create("node-2", "qemu+ssh://root@10.0.0.2/system")
	enabled := false
	got, err := r.Update(n.ID, "node-2-renamed", "qemu+ssh://root@10.0.0.3/system", &enabled)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "node-2-renamed" || got.URI != "qemu+ssh://root@10.0.0.3/system" || got.Enabled {
		t.Fatalf("update: %+v", got)
	}
}

func TestRegistryCannotEditLocalURI(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	_, err := r.Update("local", "local", "qemu+tcp://other/system", nil)
	if err == nil {
		t.Fatal("expected error changing local URI")
	}
}

func TestRegistryDeleteRemote(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	n, _ := r.Create("n2", "qemu+ssh://root@10.0.0.2/system")
	if err := r.Delete(n.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get(n.ID); ok {
		t.Fatal("still in registry")
	}
}

func TestRegistryCannotDeleteLocal(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	if err := r.Delete("local"); err == nil {
		t.Fatal("expected error deleting local")
	}
}

func TestRegistryPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	_, _ = r.Create("n2", "qemu+ssh://root@10.0.0.2/system")
	r2, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.List(); len(got) != 2 {
		t.Fatalf("after reopen list = %d, want 2", len(got))
	}
}

func TestRegistryFileExists(t *testing.T) {
	dir := t.TempDir()
	_, _ = New(dir, "qemu:///system")
	if _, err := os.Stat(filepath.Join(dir, "nodes.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryDuplicateName(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, "qemu:///system")
	_, _ = r.Create("dup", "qemu+ssh://root@10.0.0.2/system")
	if _, err := r.Create("dup", "qemu+ssh://root@10.0.0.3/system"); err == nil {
		t.Fatal("expected dup error")
	}
}
