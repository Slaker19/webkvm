package appliances

import (
	"path/filepath"
	"testing"
)

func TestValidateLinkURL(t *testing.T) {
	ok := []string{"", "https://pi-hole.net", "http://example.org/docs"}
	bad := []string{"javascript:alert(1)", "JaVaScRiPt:alert(1)", "data:text/html,x", "vbscript:x", "//evil.example", "https://", "ftp://x.org"}
	for _, u := range ok {
		if err := ValidateLinkURL(u); err != nil {
			t.Errorf("%q rejected: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := ValidateLinkURL(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestStoreRejectsUnsafeDocumentationURL(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "appliances.json"))
	app := Appliance{
		ID: "doc-url", Name: "X", Category: "cloud",
		URL:              "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		Format:           "qcow2",
		Compression:      "none",
		DocumentationURL: "javascript:alert(1)",
	}
	if err := s.Create(app); err == nil {
		t.Fatal("create must reject a javascript: documentation_url")
	}
	app.DocumentationURL = "https://example.org/docs"
	if err := s.Create(app); err != nil {
		t.Fatal(err)
	}
	app.DocumentationURL = "javascript:alert(1)"
	if err := s.Update("doc-url", app); err == nil {
		t.Fatal("update must reject a javascript: documentation_url")
	}
}
