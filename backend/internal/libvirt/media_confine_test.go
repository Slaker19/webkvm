package libvirt

import (
	"os"
	"path/filepath"
	"testing"

	"webkvm/internal/config"
	"webkvm/internal/models"
)

func TestResolveMediaPathConfinement(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	cfg := &config.Config{DataDir: tmp}
	isoDir := cfg.ISOPoolPath()
	if err := os.MkdirAll(isoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inPool := filepath.Join(isoDir, "ok.iso")
	outside := filepath.Join(tmp, "jwt.key")
	for _, p := range []string{inPool, outside} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	escape := filepath.Join(isoDir, "escape.iso")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	c := &Connector{cfg: cfg}
	user := models.MediaScope{}
	admin := models.MediaScope{Admin: true}

	if got, err := c.resolveMediaPath(inPool, user); err != nil || got != inPool {
		t.Fatalf("in-pool file for non-admin: %q %v", got, err)
	}
	for name, p := range map[string]string{
		"outside file":       outside,
		"symlink escape":     escape,
		"device":             "/dev/null",
		"etc shadow":         "/etc/passwd",
		"pool dir":           isoDir,
		"dotdot":             isoDir + "/../jwt.key",
		"other pool blocked": inPool,
	} {
		scope := user
		if name == "other pool blocked" {
			scope = models.MediaScope{AllowedPools: map[string]bool{"nothing": true}}
		}
		if got, err := c.resolveMediaPath(p, scope); err == nil {
			t.Errorf("%s: non-admin accepted %q -> %q", name, p, got)
		}
	}
	// Admin: regular files outside pools OK, non-regular files never.
	if got, err := c.resolveMediaPath(outside, admin); err != nil || got != outside {
		t.Fatalf("admin outside regular file: %q %v", got, err)
	}
	if _, err := c.resolveMediaPath("/dev/null", admin); err == nil {
		t.Fatal("admin accepted /dev/null")
	}
	if _, err := c.resolveMediaPath("/dev", admin); err == nil {
		t.Fatal("admin accepted directory")
	}
}
