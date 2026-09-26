package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
)

var errListDomains = errors.New("libvirt unreachable")

// fakeVMBackend answers ListDomains with a fixed fleet so the ACL
// filters can be exercised without libvirt.
type fakeVMBackend struct {
	compute.Backend
	vms []models.VM
	err error
}

func (f *fakeVMBackend) ListDomains() ([]models.VM, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vms, nil
}

func aclRequest(user, role string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/alerts/active", nil)
	req.Header.Set("X-User", user)
	req.Header.Set("X-Role", role)
	return req
}

// TestListActiveAlertsRespectsACL: every active alert carries a vm_id,
// and the endpoint returned the fleet-wide list to any authenticated
// caller. A viewer scoped to their own VMs could therefore enumerate
// the ids of machines their ACL is meant to hide, along with the rule
// metrics and thresholds configured for them.
func TestListActiveAlertsRespectsACL(t *testing.T) {
	h := &Handler{compute: &fakeVMBackend{vms: []models.VM{
		{ID: "vm-mine", OwnerID: "alice"},
		{ID: "vm-theirs", OwnerID: "bob"},
	}}}

	alerts := []map[string]any{
		{"vm_id": "vm-mine"},
		{"vm_id": "vm-theirs"},
		{"vm_id": ""}, // host-wide
	}

	got := h.filterAlertsByACL(aclRequest("alice", models.RoleViewer), alerts)
	if len(got) != 2 {
		t.Fatalf("viewer saw %d alerts, want 2 (own VM + host-wide): %v", len(got), got)
	}
	for _, a := range got {
		if a["vm_id"] == "vm-theirs" {
			t.Error("viewer was shown an alert for a VM they cannot see")
		}
	}

	// Admins keep the full picture.
	if all := h.filterAlertsByACL(aclRequest("root", models.RoleAdmin), alerts); len(all) != 3 {
		t.Errorf("admin saw %d alerts, want all 3", len(all))
	}
}

// TestListActiveAlertsFailsClosed: if the VM list cannot be read there
// is no way to know which alerts are in scope, so a non-admin must get
// only the host-wide ones rather than everything.
func TestListActiveAlertsFailsClosed(t *testing.T) {
	h := &Handler{compute: &fakeVMBackend{err: errListDomains}}
	alerts := []map[string]any{
		{"vm_id": "vm-a"},
		{"vm_id": ""},
	}
	got := h.filterAlertsByACL(aclRequest("alice", models.RoleViewer), alerts)
	if len(got) != 1 {
		t.Fatalf("got %d alerts, want only the host-wide one: %v", len(got), got)
	}
	if got[0]["vm_id"] != "" {
		t.Errorf("leaked a per-VM alert on the error path: %v", got[0])
	}
}

// TestListAllTagsRespectsACL: the tag vocabulary was built from every
// VM on the host, so a restricted viewer received the tag list of the
// whole fleet — effectively a directory of project names — and the
// filter chips it produced matched nothing they could see.
func TestListAllTagsRespectsACL(t *testing.T) {
	h := &Handler{
		// ListAllTags early-returns on a nil connector; it never
		// dereferences it, so an empty value is enough here.
		lv: &libvirt.Connector{},
		compute: &fakeVMBackend{vms: []models.VM{
			{ID: "vm-mine", OwnerID: "alice", Tags: []string{"mine"}},
			{ID: "vm-theirs", OwnerID: "bob", Tags: []string{"secret-project"}},
		}},
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	req.Header.Set("X-User", "alice")
	req.Header.Set("X-Role", models.RoleViewer)
	h.ListAllTags(rr, req)

	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, tag := range body.Tags {
		if tag == "secret-project" {
			t.Errorf("viewer was shown a tag that only exists on another owner's VM: %v", body.Tags)
		}
	}
}
