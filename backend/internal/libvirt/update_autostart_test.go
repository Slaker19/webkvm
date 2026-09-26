package libvirt

import (
	"testing"

	"webkvm/internal/models"
)

// TestUpdateDomain_AppliesAutostart guards the KVM PATCH path: the
// autostart flag lives outside the domain XML, so UpdateDomain must set
// it explicitly on the redefined handle (it used to be ignored).
func TestUpdateDomain_AppliesAutostart(t *testing.T) {
	c := NewConnector(testConnURI, nil)
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	for _, want := range []bool{true, false} {
		v := want
		if _, err := c.UpdateDomain("test", models.UpdateVMRequest{Autostart: &v}); err != nil {
			t.Fatalf("UpdateDomain(autostart=%v): %v", want, err)
		}
		got, err := c.GetDomainAutostart("test")
		if err != nil {
			t.Fatalf("GetDomainAutostart: %v", err)
		}
		if got != want {
			t.Errorf("autostart = %v after PATCH autostart=%v", got, want)
		}
	}
}
