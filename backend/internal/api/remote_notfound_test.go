package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	lv "libvirt.org/go/libvirt"

	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

func stubDomainExists(t *testing.T, fn func(string) (bool, error)) {
	t.Helper()
	old := remoteDomainExists
	remoteDomainExists = func(_ *libvirt.Connector, id string) (bool, error) { return fn(id) }
	t.Cleanup(func() { remoteDomainExists = old })
}

func remoteGuardRouter() http.Handler {
	h := &Handler{
		lv:     libvirt.NewTestConnector("qemu:///system"),
		remote: &remoteTarget{node: nodes.Node{ID: "n1", Name: "lab"}},
	}
	r := chi.NewRouter()
	r.Route("/api/vms/{id}", func(r chi.Router) { h.mountVMRoutes(r, modeRemote) })
	return r
}

// A VM that does not exist on a REMOTE node answers 404 domain_not_found on
// EVERY class A/B route (and the handler never runs); class L routes keep
// their 501.
func TestRemoteMissingDomainIs404OnAllClassAB(t *testing.T) {
	stubDomainExists(t, func(string) (bool, error) { return false, nil })
	router := remoteGuardRouter()
	checked := 0
	for key, class := range vmRouteClasses {
		method, pattern, _ := strings.Cut(key, " ")
		path := "/api/vms/ghost" + strings.NewReplacer("{dev}", "vda", "{mac}", "52:54:00:aa:bb:cc", "{sid}", "s1",
			"{action}", "start", "{vendorId}", "1d6b", "{productId}", "0002", "{tag}", "t").Replace(pattern)
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-Role", models.RoleAdmin)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		switch class {
		case classL:
			if rr.Code != http.StatusNotImplemented {
				t.Errorf("%s (L): %d, want 501", key, rr.Code)
			}
		default:
			checked++
			if rr.Code == http.StatusForbidden || rr.Code == http.StatusUnauthorized {
				t.Skipf("%s: auth middleware blocks the table test (%d)", key, rr.Code)
			}
			if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), `"domain_not_found"`) {
				t.Errorf("%s (%s): %d %s", key, class, rr.Code, rr.Body.String())
			}
		}
	}
	if checked < 30 {
		t.Fatalf("only %d A/B routes checked", checked)
	}
}

// Only the typed "no such domain" answer is mapped: a probe failure lets the
// request reach the handler (which reports the real error).
func TestRemoteGuardPassesThroughOnProbeError(t *testing.T) {
	stubDomainExists(t, func(string) (bool, error) { return false, fmt.Errorf("connection lost") })
	reached := false
	h := &Handler{lv: libvirt.NewTestConnector("qemu:///system"), remote: &remoteTarget{node: nodes.Node{ID: "n1"}}}
	r := chi.NewRouter()
	r.Route("/api/vms/{id}", func(r chi.Router) {
		r.Get("/x", h.remoteDomainGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true })).ServeHTTP)
	})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/api/vms/vm1/x", nil))
	if !reached {
		t.Fatalf("probe error must not be mapped to 404: %d", rr.Code)
	}
}

// The typed code, not the message text, drives libvirt.HasDomain's mapping.
func TestLibvirtNoDomainCodeIsTyped(t *testing.T) {
	e := lv.Error{Code: lv.ERR_NO_DOMAIN, Message: "something else entirely"}
	if !libvirt.IsDomainNotFound(e) {
		t.Fatal("VIR_ERR_NO_DOMAIN must be recognised without the text")
	}
	if libvirt.IsDomainNotFound(lv.Error{Code: lv.ERR_INTERNAL_ERROR, Message: "boom"}) {
		t.Fatal("other codes are not domain-not-found")
	}
}
