package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
	sharedtls "github.com/lxc/incus/v7/shared/tls"

	"webkvm/internal/incusremote"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// newTrustedIncusMock serves the subset of the Incus REST API that the remote
// handlers touch after connecting. Any presented client certificate is
// treated as trusted; requests without one are rejected.
func newTrustedIncusMock(t *testing.T) *httptest.Server {
	t.Helper()
	write := func(w http.ResponseWriter, metadata any) {
		b, err := json.Marshal(metadata)
		if err != nil {
			t.Errorf("marshal Incus test response: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, b)
	}
	fail := func(w http.ResponseWriter, code int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, `{"type":"error","error":%q,"error_code":%d}`, msg, code)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			fail(w, http.StatusForbidden, "not authorized")
			return
		}
		switch r.URL.Path {
		case "/1.0":
			write(w, api.Server{
				ServerUntrusted: api.ServerUntrusted{
					APIExtensions: []string{"certificate_project", "projects", "network", "storage"},
					APIStatus:     "stable", APIVersion: "1.0", Auth: "trusted",
				},
				Environment: api.ServerEnvironment{Server: "incus", ServerName: "test-incus", ServerVersion: "6.0", KernelArchitecture: "x86_64"},
			})
		case "/1.0/instances":
			write(w, []api.Instance{{Name: "ct1", Type: "container", Status: "Running", StatusCode: api.Running}})
		case "/1.0/instances/ct1":
			write(w, api.Instance{Name: "ct1", Type: "container", Status: "Running", StatusCode: api.Running})
		case "/1.0/networks":
			write(w, []api.Network{{Name: "other-br0", Type: "bridge", Managed: true}})
		case "/1.0/storage-pools":
			write(w, []api.StoragePool{})
		case "/1.0/profiles":
			write(w, []string{"/1.0/profiles/default"})
		default:
			fail(w, http.StatusNotFound, "not found")
		}
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

// newEnrolledIncusNode registers an enabled Incus node whose credentials are
// stored on disk, so connectIncusNode can open an authenticated client.
func newEnrolledIncusNode(t *testing.T, server *httptest.Server) (*nodes.Registry, nodes.Node) {
	t.Helper()
	registry, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	clientCert, clientKey, err := sharedtls.GenerateMemCert(true, false)
	if err != nil {
		t.Fatal(err)
	}
	serverCert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	credentialID := incusremote.CertificateFingerprint(string(clientCert))
	if err := registry.IncusCredentials().Save(credentialID, nodes.IncusCredentials{
		ClientCertificate: string(clientCert), ClientKey: string(clientKey), ServerCertificate: serverCert,
	}); err != nil {
		t.Fatal(err)
	}
	node, err := registry.CreateTyped("incus-remote", server.URL, nodes.BackendIncus, "default", credentialID, incusremote.CertificateFingerprint(serverCert))
	if err != nil {
		t.Fatal(err)
	}
	return registry, node
}

func TestOperatorForIncusKeepsClientContextAliveAfterConnect(t *testing.T) {
	server := newTrustedIncusMock(t)
	registry, node := newEnrolledIncusNode(t, server)
	h := &Handler{nodes: registry}

	op, release, err := h.operatorFor(node.ID)
	if err != nil {
		t.Fatalf("operatorFor() error = %v", err)
	}
	defer release()
	vm, err := op.GetDomain("ct1")
	if err != nil {
		t.Fatalf("GetDomain() after connect failed: %v", err)
	}
	if vm.Name != "ct1" {
		t.Fatalf("GetDomain() = %+v, want ct1", vm)
	}
}

func TestFederateVMsIncusKeepsClientContextAliveAfterConnect(t *testing.T) {
	server := newTrustedIncusMock(t)
	registry, node := newEnrolledIncusNode(t, server)
	h := &Handler{nodes: registry}

	vms := h.federateVMs(nil, models.RoleAdmin)
	if len(vms) != 1 || vms[0].Name != "ct1" || vms[0].Node != node.Name {
		t.Fatalf("federateVMs() = %+v, want ct1 tagged with node %q", vms, node.Name)
	}
	if got := h.federateVMs(nil, models.RoleOperator); len(got) != 0 {
		t.Fatalf("non-admin federation listed remote Incus instances: %+v", got)
	}
}

func TestCreateRemoteIncusVMKeepsClientContextAliveAfterConnect(t *testing.T) {
	server := newTrustedIncusMock(t)
	registry, node := newEnrolledIncusNode(t, server)
	h := &Handler{nodes: registry}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vms", nil).WithContext(context.Background())
	h.createRemoteIncusVM(res, req, models.CreateVMRequest{
		Name: "remote-ct", Type: "container", Image: "images:alpine/3.20", Network: "incusbr0",
	}, node)

	// The mock only exposes other-br0, so a live client reaches network
	// validation (400). A cancelled client context fails earlier with 502.
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", res.Code, http.StatusBadRequest, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "incusbr0") || strings.Contains(res.Body.String(), "context canceled") {
		t.Fatalf("unexpected error body: %s", res.Body.String())
	}
}
