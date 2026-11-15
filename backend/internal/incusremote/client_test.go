package incusremote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"default port", "https://incus.example", "https://incus.example:8443", true},
		{"explicit port", "https://incus.example:9443/", "https://incus.example:9443", true},
		{"reject plaintext", "http://incus.example", "", false},
		{"reject credentials", "https://user@incus.example", "", false},
		{"reject API path", "https://incus.example/1.0", "", false},
		{"reject query", "https://incus.example?token=x", "", false},
		{"reject invalid port", "https://incus.example:70000", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeEndpoint(tt.in)
			if (err == nil) != tt.ok {
				t.Fatalf("NormalizeEndpoint() error = %v, want success=%v", err, tt.ok)
			}
			if err == nil && got != tt.want {
				t.Fatalf("NormalizeEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecodeTrustTokenChecksEndpointFingerprintAndExpiry(t *testing.T) {
	const fingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	valid := api.CertificateAddToken{
		ClientName:  "webkvm",
		Fingerprint: fingerprint,
		Addresses:   []string{"incus.example:8443"},
		Secret:      "one-time-secret",
		ExpiresAt:   time.Now().Add(time.Minute),
	}
	encode := func(token api.CertificateAddToken) string {
		b, err := json.Marshal(token)
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(b)
	}

	if _, err := DecodeTrustToken(encode(valid), fingerprint); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if _, err := DecodeTrustToken(encode(valid), strings.Repeat("f", 64)); err == nil {
		t.Fatal("token for a different server fingerprint was accepted")
	}
	valid.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err := DecodeTrustToken(encode(valid), fingerprint); err == nil {
		t.Fatal("expired trust token was accepted")
	}
}

func TestTestEndpointPinsCertificateBeforeRequest(t *testing.T) {
	mock := newIncusMock(t, incusMockOptions{})
	defer mock.Close()
	token := makeTrustToken(t, mock.fingerprint())

	info, credentials, err := TestEndpoint(context.Background(), mock.URL, token)
	if err != nil {
		t.Fatalf("TestEndpoint() error = %v", err)
	}
	if mock.requests.Load() == 0 {
		t.Fatal("expected read-only Incus API request after the pin matched")
	}
	if mock.trustedRequests.Load() != 0 {
		t.Fatalf("TestEndpoint presented a client certificate on %d requests before enrollment", mock.trustedRequests.Load())
	}
	if credentials.ServerCertificate == "" || credentials.ClientCertificate != "" || credentials.ClientKey != "" {
		t.Fatalf("TestEndpoint must only return the pinned server certificate: %+v", credentials)
	}
	if info.Name != "" || info.Version != "" {
		t.Fatalf("untrusted preflight must not fabricate server details: %+v", info)
	}

	mock.requests.Store(0)
	if _, _, err := TestEndpoint(context.Background(), mock.URL, makeTrustToken(t, strings.Repeat("f", 64))); err == nil {
		t.Fatal("mismatched server fingerprint was accepted")
	}
	if mock.requests.Load() != 0 {
		t.Fatalf("sent %d API requests before validating the trust-token fingerprint", mock.requests.Load())
	}
}

func TestTestEndpointRejectsNonIncusHTTPSServer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"hello":"world"}}`)
	}))
	defer server.Close()
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(server.Certificate().Raw))

	_, _, err := TestEndpoint(context.Background(), server.URL, makeTrustToken(t, fingerprint))
	if err == nil || !strings.Contains(err.Error(), "not an Incus server") {
		t.Fatalf("TestEndpoint() error = %v, want rejection of a non-Incus endpoint", err)
	}
}

func TestEnrollRestrictsClientCertificateToProject(t *testing.T) {
	mock := newIncusMock(t, incusMockOptions{})
	defer mock.Close()
	credentials, info, err := Enroll(context.Background(), mock.URL, makeTrustToken(t, mock.fingerprint()), "containers")
	if err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}
	if info.Version != "6.0" || info.Name != "test-incus" || credentials.ClientCertificate == "" || credentials.ClientKey == "" {
		t.Fatalf("Enroll() returned incomplete credentials or server info: %+v", info)
	}
	request := mock.lastCertificateRequest()
	if request.TrustToken != "single-use-secret" || !request.Restricted || len(request.Projects) != 1 || request.Projects[0] != "containers" {
		t.Fatalf("client certificate was not restricted to the selected project: %+v", request)
	}
	if mock.deletedCount() != 0 {
		t.Fatal("successful enrollment must not revoke the new certificate")
	}
	if !mock.isTrusted(CertificateFingerprint(credentials.ClientCertificate)) {
		t.Fatal("enrolled certificate is not trusted by the mock")
	}
}

func TestEnrollRevokesCertificateWhenIncusGrantsItUnrestricted(t *testing.T) {
	mock := newIncusMock(t, incusMockOptions{grantUnrestricted: true})
	defer mock.Close()
	_, _, err := Enroll(context.Background(), mock.URL, makeTrustToken(t, mock.fingerprint()), "containers")
	if err == nil {
		t.Fatal("Enroll() accepted a certificate that Incus granted without project restrictions")
	}
	if !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("Enroll() error = %v, want an explanation about restrictions", err)
	}
	if got := mock.deletedCount(); got != 1 {
		t.Fatalf("orphaned certificate deletions = %d, want 1", got)
	}
	if mock.trustedCount() != 0 {
		t.Fatal("unrestricted certificate was left in the Incus trust store")
	}
}

func TestEnrollRevokesCertificateWhenServerLacksProjectRestrictions(t *testing.T) {
	mock := newIncusMock(t, incusMockOptions{extensions: []string{"projects"}})
	defer mock.Close()
	_, _, err := Enroll(context.Background(), mock.URL, makeTrustToken(t, mock.fingerprint()), "containers")
	if err == nil || !strings.Contains(err.Error(), "project-restricted") {
		t.Fatalf("Enroll() error = %v, want missing certificate_project extension", err)
	}
	if got := mock.deletedCount(); got != 1 {
		t.Fatalf("orphaned certificate deletions = %d, want 1", got)
	}
}

func TestEnrollRevokesCertificateWhenProjectIsUnavailable(t *testing.T) {
	mock := newIncusMock(t, incusMockOptions{})
	defer mock.Close()
	_, _, err := Enroll(context.Background(), mock.URL, makeTrustToken(t, mock.fingerprint()), "missing")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("Enroll() error = %v, want unavailable project", err)
	}
	if got := mock.deletedCount(); got != 1 {
		t.Fatalf("orphaned certificate deletions = %d, want 1", got)
	}
}

func TestFetchServerCertificateAcceptsIPEndpointAbsentFromSANs(t *testing.T) {
	// Incus self-signed certificates only carry the hostname plus loopback
	// addresses, so a LAN IP endpoint never matches the SANs. The pinned
	// fingerprint already authenticates the server.
	mock := newIncusMock(t, incusMockOptions{certDNSNames: []string{"incus.internal"}})
	defer mock.Close()
	if _, _, err := TestEndpoint(context.Background(), mock.URL, makeTrustToken(t, mock.fingerprint())); err != nil {
		t.Fatalf("TestEndpoint() rejected an IP endpoint whose certificate only names the host: %v", err)
	}
	if _, _, err := Enroll(context.Background(), mock.URL, makeTrustToken(t, mock.fingerprint()), "containers"); err != nil {
		t.Fatalf("Enroll() rejected an IP endpoint whose certificate only names the host: %v", err)
	}
}

type incusMockOptions struct {
	// grantUnrestricted simulates a trust token issued without project
	// restrictions: Incus ignores Restricted/Projects from the request body.
	grantUnrestricted bool
	// extensions overrides the API extensions advertised to trusted clients.
	extensions []string
	// certDNSNames, when set, serves a certificate whose SANs contain only
	// these names (no IP addresses).
	certDNSNames []string
}

// incusMock emulates the parts of the Incus REST API used during enrollment.
// Requests without a trusted client certificate only see api.ServerUntrusted,
// exactly like cmd/incusd/api_1.0.go.
type incusMock struct {
	*httptest.Server
	t               *testing.T
	opts            incusMockOptions
	requests        atomic.Int32
	trustedRequests atomic.Int32

	mu            sync.Mutex
	trusted       map[string]api.Certificate
	deleted       []string
	lastCertusReq api.CertificatesPost
}

func newIncusMock(t *testing.T, opts incusMockOptions) *incusMock {
	t.Helper()
	m := &incusMock{t: t, opts: opts, trusted: map[string]api.Certificate{}}
	m.Server = httptest.NewUnstartedServer(http.HandlerFunc(m.serve))
	m.Server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	if len(opts.certDNSNames) > 0 {
		m.Server.TLS.Certificates = []tls.Certificate{generateDNSOnlyCertificate(t, opts.certDNSNames)}
	}
	m.Server.StartTLS()
	return m
}

func (m *incusMock) fingerprint() string {
	return fmt.Sprintf("%x", sha256.Sum256(m.Certificate().Raw))
}

func (m *incusMock) lastCertificateRequest() api.CertificatesPost {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastCertusReq
}

func (m *incusMock) deletedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.deleted)
}

func (m *incusMock) trustedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.trusted)
}

func (m *incusMock) isTrusted(fingerprint string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.trusted[fingerprint]
	return ok
}

func (m *incusMock) clientTrusted(r *http.Request) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	return m.isTrusted(fmt.Sprintf("%x", sha256.Sum256(r.TLS.PeerCertificates[0].Raw)))
}

func (m *incusMock) serve(w http.ResponseWriter, r *http.Request) {
	m.requests.Add(1)
	w.Header().Set("Content-Type", "application/json")
	trusted := m.clientTrusted(r)
	if trusted {
		m.trustedRequests.Add(1)
	}
	untrustedInfo := api.ServerUntrusted{
		APIExtensions: nil,
		APIStatus:     "stable",
		APIVersion:    "1.0",
		Auth:          "untrusted",
		AuthMethods:   []string{api.AuthenticationMethodTLS},
	}
	if r.URL.Path == "/1.0" {
		if !trusted {
			writeIncusResponse(m.t, w, untrustedInfo)
			return
		}
		extensions := m.opts.extensions
		if extensions == nil {
			extensions = []string{"certificate_project", "projects", "network", "storage"}
		}
		untrustedInfo.Auth = "trusted"
		untrustedInfo.APIExtensions = extensions
		writeIncusResponse(m.t, w, api.Server{
			ServerUntrusted: untrustedInfo,
			Environment: api.ServerEnvironment{
				Server:             "incus",
				ServerName:         "test-incus",
				ServerVersion:      "6.0",
				KernelArchitecture: "x86_64",
				ServerClustered:    false,
			},
		})
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/1.0/certificates" {
		// Token-based enrollment is the only unauthenticated write.
		var req api.CertificatesPost
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			m.t.Errorf("decode certificate request: %v", err)
			writeIncusError(w, http.StatusBadRequest, "bad request")
			return
		}
		if req.TrustToken != "single-use-secret" && !trusted {
			writeIncusError(w, http.StatusForbidden, "not authorized")
			return
		}
		fingerprint := CertificateFingerprint(req.Certificate)
		if fingerprint == "" {
			writeIncusError(w, http.StatusBadRequest, "invalid certificate")
			return
		}
		granted := api.Certificate{
			CertificatePut: api.CertificatePut{
				Name: req.Name, Type: req.Type, Certificate: req.Certificate,
				Restricted: req.Restricted, Projects: req.Projects, Description: req.Description,
			},
			Fingerprint: fingerprint,
		}
		if m.opts.grantUnrestricted {
			granted.Restricted = false
			granted.Projects = nil
		}
		m.mu.Lock()
		m.lastCertusReq = req
		m.trusted[fingerprint] = granted
		m.mu.Unlock()
		writeIncusResponse(m.t, w, map[string]any{})
		return
	}
	if !trusted {
		writeIncusError(w, http.StatusForbidden, "not authorized")
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/1.0/certificates/"):
		fingerprint := strings.TrimPrefix(r.URL.Path, "/1.0/certificates/")
		m.mu.Lock()
		cert, ok := m.trusted[fingerprint]
		if ok && r.Method == http.MethodDelete {
			delete(m.trusted, fingerprint)
			m.deleted = append(m.deleted, fingerprint)
		}
		m.mu.Unlock()
		if !ok {
			writeIncusError(w, http.StatusNotFound, "not found")
			return
		}
		if r.Method == http.MethodDelete {
			writeIncusResponse(m.t, w, nil)
			return
		}
		writeIncusResponse(m.t, w, cert)
	case r.URL.Path == "/1.0/projects/containers":
		writeIncusResponse(m.t, w, api.Project{Name: "containers"})
	default:
		writeIncusError(w, http.StatusNotFound, "not found")
	}
}

func writeIncusResponse(t *testing.T, w http.ResponseWriter, metadata any) {
	t.Helper()
	b, err := json.Marshal(metadata)
	if err != nil {
		t.Errorf("marshal Incus test response: %v", err)
		return
	}
	_, _ = fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, b)
}

func writeIncusError(w http.ResponseWriter, code int, message string) {
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"type":"error","error":%q,"error_code":%d}`, message, code)
}

func generateDNSOnlyCertificate(t *testing.T, dnsNames []string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func makeTrustToken(t *testing.T, fingerprint string) string {
	t.Helper()
	data, err := json.Marshal(api.CertificateAddToken{
		ClientName:  "webkvm",
		Fingerprint: fingerprint,
		Addresses:   []string{"incus.test:8443"},
		Secret:      "single-use-secret",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data)
}
