package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	sharedtls "github.com/lxc/incus/v7/shared/tls"

	"webkvm/internal/audit"
	"webkvm/internal/incusremote"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

type nodesFixture struct {
	h      *Handler
	reg    *nodes.Registry
	router http.Handler
	audit  *audit.Logger
}

func newNodesFixture(t *testing.T) *nodesFixture {
	t.Helper()
	dir := t.TempDir()
	reg, err := nodes.New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	al, err := audit.New(filepath.Join(dir, "audit", "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = al.Close() })
	h := &Handler{nodes: reg, audit: al}
	rt := chi.NewRouter()
	rt.Post("/api/nodes", h.CreateNode)
	rt.Put("/api/nodes/{id}", h.UpdateNode)
	rt.Delete("/api/nodes/{id}", h.DeleteNode)
	rt.Post("/api/nodes/{id}/ping", h.PingNode)
	rt.Post("/api/nodes/test", h.TestNodeConnection)
	return &nodesFixture{h: h, reg: reg, router: rt, audit: al}
}

func (f *nodesFixture) do(method, path, body, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		req.Header.Set("X-Role", role)
	}
	res := httptest.NewRecorder()
	f.router.ServeHTTP(res, req)
	return res
}

func (f *nodesFixture) actions(t *testing.T, action string) []audit.Entry {
	t.Helper()
	entries, _, err := f.audit.List(audit.ListOptions{Action: action}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func bodyJSON(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", res.Body.String(), err)
	}
	return m
}

func TestCreateNodeRejectsReservedAndInvalidNames(t *testing.T) {
	f := newNodesFixture(t)
	for name, code := range map[string]string{
		"local":              "reserved_name",
		"n_0123456789abcdef": "reserved_name",
		"bad name":           "invalid_name",
	} {
		res := f.do(http.MethodPost, "/api/nodes", `{"name":"`+name+`","uri":"qemu+ssh://root@host.example/system"}`, models.RoleAdmin)
		if res.Code != http.StatusBadRequest || bodyJSON(t, res)["code"] != code {
			t.Errorf("name %q: status=%d body=%s want code %s", name, res.Code, res.Body.String(), code)
		}
	}
}

func TestCreateNodeValidatesLibvirtURI(t *testing.T) {
	f := newNodesFixture(t)
	for _, uri := range []string{
		"qemu+tcp://host.example:16509/system",
		"qemu+ssh://u:pw@host.example/system",
		"qemu+ssh://u@host.example/system?command=/bin/evil",
		"qemu+ext://u@host.example/system?command=/bin/evil",
		"qemu+ssh://u@host.example/system?keyfile=/root/.ssh/id_rsa",
	} {
		res := f.do(http.MethodPost, "/api/nodes", `{"name":"n1","uri":"`+uri+`"}`, models.RoleAdmin)
		if res.Code != http.StatusBadRequest || bodyJSON(t, res)["code"] != "invalid_uri" {
			t.Errorf("uri %q: status=%d body=%s", uri, res.Code, res.Body.String())
		}
	}
	res := f.do(http.MethodPost, "/api/nodes", `{"name":"n1","uri":"qemu+tcp://h/system"}`, models.RoleAdmin)
	if !strings.Contains(res.Body.String(), "qemu+ssh") || !strings.Contains(res.Body.String(), "Incus") {
		t.Fatalf("qemu+tcp message should point to qemu+ssh or Incus: %s", res.Body.String())
	}
	if len(f.reg.List()) != 1 {
		t.Fatal("rejected URIs must not create nodes")
	}
}

func TestCreateNodeAuditRedactsURIUserinfo(t *testing.T) {
	f := newNodesFixture(t)
	res := f.do(http.MethodPost, "/api/nodes", `{"name":"n1","uri":"qemu+ssh://deploy@host.example/system"}`, models.RoleAdmin)
	if res.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	entries := f.actions(t, "node.create")
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d", len(entries))
	}
	uri, _ := entries[0].Detail["uri"].(string)
	if strings.Contains(uri, "deploy") || !strings.Contains(uri, "host.example") {
		t.Fatalf("audit uri not redacted: %q", uri)
	}
}

func TestUpdateNodeStatusCodes(t *testing.T) {
	f := newNodesFixture(t)
	a, _ := f.reg.Create("alpha", "qemu+ssh://u@a.example/system")
	_, _ = f.reg.Create("beta", "qemu+ssh://u@b.example/system")
	if res := f.do(http.MethodPut, "/api/nodes/missing", `{"name":"x"}`, models.RoleAdmin); res.Code != http.StatusNotFound {
		t.Errorf("missing: %d", res.Code)
	}
	if res := f.do(http.MethodPut, "/api/nodes/"+a.ID, `{"name":"beta"}`, models.RoleAdmin); res.Code != http.StatusConflict {
		t.Errorf("conflict: %d %s", res.Code, res.Body.String())
	}
	if res := f.do(http.MethodPut, "/api/nodes/"+a.ID, `{"name":"local"}`, models.RoleAdmin); res.Code != http.StatusBadRequest {
		t.Errorf("reserved: %d", res.Code)
	}
	if res := f.do(http.MethodPut, "/api/nodes/"+a.ID, `{"uri":"qemu+ssh://u@a.example/system?command=/bin/x"}`, models.RoleAdmin); res.Code != http.StatusBadRequest {
		t.Errorf("uri options: %d", res.Code)
	}
	if res := f.do(http.MethodPut, "/api/nodes/"+a.ID, `{"uri":"qemu+tcp://a.example/system"}`, models.RoleAdmin); res.Code != http.StatusBadRequest {
		t.Errorf("qemu+tcp: %d", res.Code)
	}
	if res := f.do(http.MethodPut, "/api/nodes/"+a.ID, `{"name":"alpha2","uri":"qemu+ssh://u@a2.example/system"}`, models.RoleAdmin); res.Code != http.StatusOK {
		t.Errorf("ok: %d %s", res.Code, res.Body.String())
	}
}

func TestUpdateNodeLegacyTCPNodeCanStillBeDisabled(t *testing.T) {
	f := newNodesFixture(t)
	n, _ := f.reg.Create("legacy", "qemu+tcp://l.example:16509/system")
	res := f.do(http.MethodPut, "/api/nodes/"+n.ID, `{"uri":"qemu+tcp://l.example:16509/system","enabled":false}`, models.RoleAdmin)
	if res.Code != http.StatusOK {
		t.Fatalf("legacy node update: %d %s", res.Code, res.Body.String())
	}
}

func TestPingNodeUnknownIs404ButFailedProbeIs200(t *testing.T) {
	f := newNodesFixture(t)
	if res := f.do(http.MethodPost, "/api/nodes/missing/ping", "", models.RoleAdmin); res.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d %s", res.Code, res.Body.String())
	}
	n, _ := f.reg.Create("down", "https://127.0.0.1:1")
	res := f.do(http.MethodPost, "/api/nodes/"+n.ID+"/ping", "", models.RoleAdmin)
	if res.Code != http.StatusOK || bodyJSON(t, res)["status"] != "offline" {
		t.Fatalf("failed probe: %d %s", res.Code, res.Body.String())
	}
}

func newIncusDeleteFixture(t *testing.T) (*nodesFixture, nodes.Node) {
	t.Helper()
	f := newNodesFixture(t)
	cert, key, err := sharedtls.GenerateMemCert(true, false)
	if err != nil {
		t.Fatal(err)
	}
	srvCert, _, err := sharedtls.GenerateMemCert(false, false)
	if err != nil {
		t.Fatal(err)
	}
	id := incusremote.CertificateFingerprint(string(cert))
	if err := f.reg.IncusCredentials().Save(id, nodes.IncusCredentials{ClientCertificate: string(cert), ClientKey: string(key), ServerCertificate: string(srvCert)}); err != nil {
		t.Fatal(err)
	}
	n, err := f.reg.CreateTyped("incus1", "https://incus.example:8443", nodes.BackendIncus, "default", id, incusremote.CertificateFingerprint(string(srvCert)))
	if err != nil {
		t.Fatal(err)
	}
	return f, n
}

func stubRevoke(t *testing.T, fn func(ctx context.Context, endpoint string, c incusremote.Credentials, project string) error) {
	t.Helper()
	prev := incusRevoke
	incusRevoke = fn
	t.Cleanup(func() { incusRevoke = prev })
}

func TestDeleteNodeUnknownIs404WithoutAudit(t *testing.T) {
	f := newNodesFixture(t)
	res := f.do(http.MethodDelete, "/api/nodes/ghost", "", models.RoleAdmin)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d", res.Code)
	}
	if got := f.actions(t, "node.delete"); len(got) != 0 {
		t.Fatalf("fake audit entry written: %+v", got)
	}
}

func TestDeleteIncusNodeRevokeFailureNeedsForce(t *testing.T) {
	f, n := newIncusDeleteFixture(t)
	stubRevoke(t, func(context.Context, string, incusremote.Credentials, string) error {
		return errors.New("server unreachable")
	})

	res := f.do(http.MethodDelete, "/api/nodes/"+n.ID, "", models.RoleAdmin)
	body := bodyJSON(t, res)
	if res.Code != http.StatusBadGateway || body["code"] != "incus_revoke_failed" || !strings.Contains(res.Body.String(), "force=true") {
		t.Fatalf("no force: %d %s", res.Code, res.Body.String())
	}
	if _, ok := f.reg.Get(n.ID); !ok {
		t.Fatal("node must be kept without force")
	}
	if res := f.do(http.MethodDelete, "/api/nodes/"+n.ID+"?force=true", "", models.RoleOperator); res.Code != http.StatusForbidden {
		t.Fatalf("force by non-admin: %d", res.Code)
	}

	res = f.do(http.MethodDelete, "/api/nodes/"+n.ID+"?force=true", "", models.RoleAdmin)
	body = bodyJSON(t, res)
	warning, _ := body["warning"].(string)
	if res.Code != http.StatusOK || !strings.Contains(warning, "incus config trust remove "+n.CredentialID) {
		t.Fatalf("force: %d %s", res.Code, res.Body.String())
	}
	if _, ok := f.reg.Get(n.ID); ok {
		t.Fatal("node still present after force delete")
	}
	if _, err := f.reg.IncusCredentials().Load(n.CredentialID); err == nil {
		t.Fatal("credentials still stored after force delete")
	}
	entries := f.actions(t, "node.delete")
	if len(entries) != 1 || entries[0].Detail["force"] != true {
		t.Fatalf("force delete must be audited: %+v", entries)
	}
}

func TestDeleteIncusNodeCredentialDeleteFailureStill200(t *testing.T) {
	f, n := newIncusDeleteFixture(t)
	stubRevoke(t, func(context.Context, string, incusremote.Credentials, string) error {
		// Make the local credential entry undeletable after it was loaded.
		root := filepath.Dir(f.reg.Path())
		matches, _ := filepath.Glob(filepath.Join(root, "*", n.CredentialID))
		if len(matches) == 0 {
			return errors.New("test setup: credential dir not found")
		}
		if err := os.RemoveAll(matches[0]); err != nil {
			return err
		}
		return os.WriteFile(matches[0], []byte("x"), 0o600)
	})
	res := f.do(http.MethodDelete, "/api/nodes/"+n.ID, "", models.RoleAdmin)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "credentials could not be removed") {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if _, ok := f.reg.Get(n.ID); ok {
		t.Fatal("node should be deleted")
	}
	if len(f.actions(t, "node.delete")) != 1 {
		t.Fatal("delete must still be audited")
	}
}

func TestCreateIncusNodeValidatesNameBeforeConsumingToken(t *testing.T) {
	f := newNodesFixture(t)
	if _, err := f.reg.Create("taken", "qemu+ssh://u@t.example/system"); err != nil {
		t.Fatal(err)
	}
	enrolls := 0
	prev := incusEnroll
	incusEnroll = func(context.Context, string, string, string) (incusremote.Credentials, incusremote.ServerInfo, error) {
		enrolls++
		return incusremote.Credentials{}, incusremote.ServerInfo{}, errors.New("must not be called")
	}
	t.Cleanup(func() { incusEnroll = prev })
	for _, name := range []string{"taken", "local", "bad name"} {
		res := f.do(http.MethodPost, "/api/nodes", `{"backend":"incus","name":"`+name+`","uri":"https://incus.example:8443","trust_token":"tok"}`, models.RoleAdmin)
		if res.Code != http.StatusBadRequest && res.Code != http.StatusConflict {
			t.Errorf("name %q: status %d", name, res.Code)
		}
	}
	if enrolls != 0 {
		t.Fatalf("one-time token consumed %d times for invalid names", enrolls)
	}
}

func TestCreateIncusNodeRevokeCleanupSurvivesExhaustedContext(t *testing.T) {
	f := newNodesFixture(t)
	cert, key, _ := sharedtls.GenerateMemCert(true, false)
	srv, _, _ := sharedtls.GenerateMemCert(false, false)
	reqCtx, cancelReq := context.WithCancel(context.Background())
	prevE, prevR := incusEnroll, incusRevoke
	t.Cleanup(func() { incusEnroll, incusRevoke = prevE, prevR })
	incusEnroll = func(context.Context, string, string, string) (incusremote.Credentials, incusremote.ServerInfo, error) {
		cancelReq() // the request context is gone right after the token is consumed
		return incusremote.Credentials{ClientCertificate: string(cert), ClientKey: string(key), ServerCertificate: string(srv)}, incusremote.ServerInfo{Name: "s"}, nil
	}
	var revokeCtxErr error
	revoked := false
	incusRevoke = func(ctx context.Context, _ string, _ incusremote.Credentials, _ string) error {
		revoked = true
		revokeCtxErr = ctx.Err()
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("cleanup context has no deadline")
		}
		return nil
	}
	// An invalid project makes CreateTyped fail after Enroll succeeded.
	req := httptest.NewRequest(http.MethodPost, "/api/nodes", strings.NewReader(`{"backend":"incus","name":"ok-name","uri":"https://incus.example:8443","trust_token":"t","project":"bad project!"}`)).WithContext(reqCtx)
	res := httptest.NewRecorder()
	f.router.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d %s", res.Code, res.Body.String())
	}
	if !revoked || revokeCtxErr != nil {
		t.Fatalf("revoke called=%v ctxErr=%v: cleanup must use a fresh context", revoked, revokeCtxErr)
	}
}

func TestTestNodeConnectionRejectsOptionURIs(t *testing.T) {
	f := newNodesFixture(t)
	for _, uri := range []string{"qemu+ssh://u@h.example/system?command=/bin/x", "qemu+tcp://h.example/system"} {
		res := f.do(http.MethodPost, "/api/nodes/test", `{"uri":"`+uri+`"}`, models.RoleAdmin)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%q: %d", uri, res.Code)
		}
	}
}

func TestNodesAPIUIURL(t *testing.T) {
	f := newNodesFixture(t)
	res := f.do(http.MethodPost, "/api/nodes", `{"name":"lab1","uri":"qemu+ssh://u@10.20.30.10/system","ui_url":"https://192.168.50.31:8080/"}`, "admin")
	if res.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Code, res.Body)
	}
	var n nodes.Node
	_ = json.Unmarshal(res.Body.Bytes(), &n)
	if n.UIURL != "https://192.168.50.31:8080" {
		t.Fatalf("ui_url = %q", n.UIURL)
	}
	// omitted keeps
	res = f.do(http.MethodPut, "/api/nodes/"+n.ID, `{"name":"lab1b"}`, "admin")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"ui_url":"https://192.168.50.31:8080"`) {
		t.Fatalf("omit: %d %s", res.Code, res.Body)
	}
	// invalid -> 400 invalid_ui_url
	res = f.do(http.MethodPut, "/api/nodes/"+n.ID, `{"ui_url":"ftp://x"}`, "admin")
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), `"code":"invalid_ui_url"`) {
		t.Fatalf("invalid: %d %s", res.Code, res.Body)
	}
	res = f.do(http.MethodPost, "/api/nodes", `{"name":"lab2","uri":"qemu+ssh://u@10.20.30.11/system","ui_url":"https://localhost"}`, "admin")
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), `"code":"invalid_ui_url"`) {
		t.Fatalf("create invalid: %d %s", res.Code, res.Body)
	}
	// clear
	res = f.do(http.MethodPut, "/api/nodes/"+n.ID, `{"ui_url":""}`, "admin")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "ui_url") {
		t.Fatalf("clear: %d %s", res.Code, res.Body)
	}
}
