package nodes

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateUIURL(t *testing.T) {
	ok := map[string]string{
		"":                           "",
		"  ":                         "",
		"https://192.168.50.31:8080": "https://192.168.50.31:8080",
		"http://host.lan":            "http://host.lan",
		"HTTPS://Host.LAN:8443/":     "https://host.lan:8443",
		" http://10.1.2.3 ":          "http://10.1.2.3",
		"https://[2001:db8::1]:8080": "https://[2001:db8::1]:8080",
	}
	for in, want := range ok {
		got, err := ValidateUIURL(in)
		if err != nil || got != want {
			t.Errorf("ValidateUIURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"ftp://h", "javascript:alert(1)", "//host", "host.lan", "https://", "https://u@h", "https://u:p@h",
		"https://h?x=1", "https://h/?", "https://h#f", "https://h/path", "https://h/a/", "https://localhost",
		"https://127.0.0.1:8080", "https://[::1]", "http://0.0.0.0", "http://[::]", "https://169.254.1.1",
		"https://[fe80::1]", "http://ho st", "http://h\x00st", "http://h\nst", "https://h:99999", "https://h:0",
		"https://" + strings.Repeat("a", 250) + ".lan",
	}
	for _, in := range bad {
		_, err := ValidateUIURL(in)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Code != CodeInvalidUIURL {
			t.Errorf("%q should be invalid_ui_url, got %v", in, err)
		}
	}
}

func TestUIURLPersistenceAndUpdateSemantics(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	n, err := r.CreateWithUI("lab1", "qemu+ssh://u@10.20.30.10/system", BackendLibvirt, "", "", "", "https://192.168.50.31:8080/")
	if err != nil || n.UIURL != "https://192.168.50.31:8080" {
		t.Fatalf("create: %+v %v", n, err)
	}
	r2, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r2.Get(n.ID); got.UIURL != "https://192.168.50.31:8080" {
		t.Fatalf("not persisted: %q", got.UIURL)
	}
	// omitted keeps
	if got, err := r2.Update(n.ID, "lab1b", "", nil); err != nil || got.UIURL != "https://192.168.50.31:8080" {
		t.Fatalf("omit must keep: %+v %v", got, err)
	}
	// change
	v := "http://lab.lan"
	if got, err := r2.UpdateWithUI(n.ID, "", "", &v, nil); err != nil || got.UIURL != "http://lab.lan" {
		t.Fatalf("change: %+v %v", got, err)
	}
	// invalid keeps old value and errors
	bad := "ftp://x"
	if _, err := r2.UpdateWithUI(n.ID, "", "", &bad, nil); err == nil {
		t.Fatal("invalid must fail")
	}
	if got, _ := r2.Get(n.ID); got.UIURL != "http://lab.lan" {
		t.Fatalf("invalid update changed value: %q", got.UIURL)
	}
	// clear
	empty := ""
	if got, err := r2.UpdateWithUI(n.ID, "", "", &empty, nil); err != nil || got.UIURL != "" {
		t.Fatalf("clear: %+v %v", got, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "nodes.json"))
	if strings.Contains(string(data), "ui_url") {
		t.Fatal("empty ui_url must be omitted from nodes.json")
	}
}

func TestCreateRejectsInvalidUIURL(t *testing.T) {
	r, _ := New(t.TempDir(), "qemu:///system")
	_, err := r.CreateWithUI("lab1", "qemu+ssh://u@h.example/system", BackendLibvirt, "", "", "", "https://localhost")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Code != CodeInvalidUIURL {
		t.Fatalf("got %v", err)
	}
}

func TestOldNodesFileLoadsWithoutUIURL(t *testing.T) {
	dir := t.TempDir()
	old := `[{"id":"n_0123456789abcdef","name":"old","uri":"qemu+ssh://u@10.0.0.2/system","type":"remote","enabled":true,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}]`
	if err := os.WriteFile(filepath.Join(dir, "nodes.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	n, ok := r.Get("n_0123456789abcdef")
	if !ok || n.UIURL != "" {
		t.Fatalf("%+v %v", n, ok)
	}
	b, _ := json.Marshal(n)
	if strings.Contains(string(b), "ui_url") {
		t.Fatal("omitempty expected")
	}
}
