package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/models"
)

// selectorRouter exposes GET /pools (selector-aware) and /ifaces (local only);
// the picked handler reports whether it ran on a per-node clone.
func selectorRouter(f dispatchFixture) http.Handler {
	r := chi.NewRouter()
	r.Get("/pools", f.h.withNodeSelector(func(x *Handler) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			if x.isRemote() {
				w.Header().Set("X-Ran-On", x.remote.node.Name)
			} else {
				w.Header().Set("X-Ran-On", "local")
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	r.Get("/ifaces", f.h.listNodeLocalOnly(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ran-On", "local")
		w.WriteHeader(http.StatusOK)
	}))
	return r
}

func doSel(h http.Handler, path, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Role", role)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}

func TestNodeSelectorLocalAndRemote(t *testing.T) {
	f := newDispatchFixture(t)
	h := selectorRouter(f)

	// No selector / local selectors keep running locally, for any role.
	for _, p := range []string{"/pools", "/pools?node=local", "/pools?node="} {
		if res := doSel(h, p, models.RoleOperator); res.Code != http.StatusOK || res.Header().Get("X-Ran-On") != "local" {
			t.Fatalf("%s: %d ran on %q", p, res.Code, res.Header().Get("X-Ran-On"))
		}
	}
	// A remote node runs the same handler on the per-node clone (admin).
	res := doSel(h, "/pools?node=remote", models.RoleAdmin)
	if res.Code != http.StatusOK || res.Header().Get("X-Ran-On") != "remote" {
		t.Fatalf("remote admin: %d ran on %q", res.Code, res.Header().Get("X-Ran-On"))
	}
}

func TestNodeSelectorGuards(t *testing.T) {
	f := newDispatchFixture(t)
	h := selectorRouter(f)

	// 403 before 404 for any non-admin, including unknown nodes.
	for _, role := range []string{models.RoleOperator, ""} {
		for _, p := range []string{"/pools?node=remote", "/pools?node=ghost"} {
			if res := doSel(h, p, role); res.Code != http.StatusForbidden {
				t.Fatalf("%s as %q: got %d want 403", p, role, res.Code)
			}
		}
	}
	if res := doSel(h, "/pools?node=ghost", models.RoleAdmin); res.Code != http.StatusNotFound || decode(t, res)["code"] != "node_not_found" {
		t.Fatalf("unknown node: %d %v", res.Code, decode(t, res))
	}
	off := false
	if _, err := f.h.nodes.Update(f.remote.ID, "", "", &off); err != nil {
		t.Fatal(err)
	}
	if res := doSel(h, "/pools?node=remote", models.RoleAdmin); res.Code != http.StatusNotFound || decode(t, res)["code"] != "node_disabled" {
		t.Fatalf("disabled node: %d %v", res.Code, decode(t, res))
	}
}

func TestNodeSelectorTypedPoolErrors(t *testing.T) {
	f := newDispatchFixture(t)
	h := selectorRouter(f)
	*f.fail = errors.New("ssh: handshake failed: ssh: unable to authenticate")
	res := doSel(h, "/pools?node=remote", models.RoleAdmin)
	b := decode(t, res)
	if res.Code == http.StatusOK || b["code"] == "" || b["node_id"] != f.remote.ID {
		t.Fatalf("expected a typed node error, got %d %v", res.Code, b)
	}
	if body := res.Body.String(); len(body) > 0 && (strings.Contains(body, "handshake") || strings.Contains(body, "unable to authenticate")) {
		t.Fatalf("raw ssh error leaked: %s", body)
	}
}

func TestListNodeLocalOnlyRemoteIs501(t *testing.T) {
	f := newDispatchFixture(t)
	h := selectorRouter(f)
	if res := doSel(h, "/ifaces", models.RoleOperator); res.Code != http.StatusOK {
		t.Fatalf("local: %d", res.Code)
	}
	res := doSel(h, "/ifaces?node=remote", models.RoleAdmin)
	b := decode(t, res)
	if res.Code != http.StatusNotImplemented || b["code"] != "remote_unsupported" || b["node"] != "remote" {
		t.Fatalf("remote interfaces: %d %v", res.Code, b)
	}
	if res := doSel(h, "/ifaces?node=remote", models.RoleOperator); res.Code != http.StatusForbidden {
		t.Fatalf("operator remote interfaces: %d want 403", res.Code)
	}
}
