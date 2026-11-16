package incusremote

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	sharedtls "github.com/lxc/incus/v7/shared/tls"
)

const defaultPort = "8443"

var projectRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,63}$`)

// Credentials contains the server-side TLS material needed for one Incus node.
type Credentials struct {
	ClientCertificate string
	ClientKey         string
	ServerCertificate string
}

// ServerInfo is the small, safe subset used by node preflight and health checks.
type ServerInfo struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	OS           string   `json:"os"`
	OSVersion    string   `json:"os_version"`
	Architecture string   `json:"architecture"`
	Project      string   `json:"project"`
	Clustered    bool     `json:"clustered"`
	Extensions   []string `json:"extensions,omitempty"`
}

// NormalizeEndpoint accepts an Incus HTTPS endpoint, defaulting its port to 8443.
func NormalizeEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("incus endpoint must be an https URL with no credentials, path, query, or fragment")
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	portNum, err := net.LookupPort("tcp", port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return "", errors.New("invalid Incus HTTPS port")
	}
	u.Path = ""
	u.RawPath = ""
	u.Host = net.JoinHostPort(u.Hostname(), fmt.Sprint(portNum))
	return u.String(), nil
}

// DecodeTrustToken verifies the one-time token belongs to the pinned server.
func DecodeTrustToken(raw, serverFingerprint string) (*api.CertificateAddToken, error) {
	if !validFingerprint(serverFingerprint) {
		return nil, errors.New("incus trust token must include a valid SHA-256 server fingerprint")
	}
	token, err := sharedtls.CertificateTokenDecode(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("invalid Incus client-certificate trust token")
	}
	if !strings.EqualFold(token.Fingerprint, serverFingerprint) {
		return nil, errors.New("incus trust token fingerprint does not match the HTTPS server")
	}
	if token.ExpiresAt.IsZero() || !time.Now().Before(token.ExpiresAt) {
		return nil, errors.New("incus client-certificate trust token has expired")
	}
	return token, nil
}

// TestEndpoint verifies an out-of-band fingerprint before making an Incus API call.
//
// The preflight runs as an untrusted client, and Incus only exposes
// api.ServerUntrusted (auth, api_version, api_status, extensions are empty)
// until a certificate is trusted, so only those fields are validated here.
// ServerInfo therefore stays mostly empty until Enroll succeeds.
func TestEndpoint(ctx context.Context, rawEndpoint, rawToken string) (ServerInfo, Credentials, error) {
	endpoint, err := NormalizeEndpoint(rawEndpoint)
	if err != nil {
		return ServerInfo{}, Credentials{}, err
	}
	serverCert, fingerprint, err := fetchServerCertificate(ctx, endpoint)
	if err != nil {
		return ServerInfo{}, Credentials{}, err
	}
	if _, err := DecodeTrustToken(rawToken, fingerprint); err != nil {
		return ServerInfo{}, Credentials{}, err
	}
	client, err := incus.ConnectIncusWithContext(ctx, endpoint, &incus.ConnectionArgs{
		TLSServerCert: string(serverCert),
		SkipGetEvents: true,
		SkipGetServer: true,
	})
	if err != nil {
		return ServerInfo{}, Credentials{}, fmt.Errorf("connect to Incus API: %w", err)
	}
	defer client.Disconnect()
	server, _, err := client.GetServer()
	if err != nil {
		return ServerInfo{}, Credentials{}, fmt.Errorf("read Incus server information: %w", err)
	}
	if err := validateUntrustedServer(server); err != nil {
		return ServerInfo{}, Credentials{}, err
	}
	return serverInfo(server), Credentials{ServerCertificate: string(serverCert)}, nil
}

// validateUntrustedServer checks the fields Incus returns to any client.
// Environment and the extension list are only present for trusted clients.
func validateUntrustedServer(server *api.Server) error {
	if server == nil || strings.TrimSpace(server.APIVersion) == "" {
		return errors.New("HTTPS endpoint is not an Incus server")
	}
	switch server.Auth {
	case "untrusted", "trusted":
	default:
		return errors.New("HTTPS endpoint is not an Incus server")
	}
	return nil
}

// Enroll creates a restricted TLS client certificate using a one-time server token.
func Enroll(ctx context.Context, rawEndpoint, rawToken, project string) (Credentials, ServerInfo, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		project = "default"
	}
	if !projectRE.MatchString(project) {
		return Credentials{}, ServerInfo{}, errors.New("invalid Incus project name")
	}
	_, credentials, err := TestEndpoint(ctx, rawEndpoint, rawToken)
	if err != nil {
		return Credentials{}, ServerInfo{}, err
	}
	endpoint, _ := NormalizeEndpoint(rawEndpoint)
	token, err := DecodeTrustToken(rawToken, certificateFingerprint(credentials.ServerCertificate))
	if err != nil {
		return Credentials{}, ServerInfo{}, err
	}
	clientCert, clientKey, err := sharedtls.GenerateMemCert(true, false)
	if err != nil {
		return Credentials{}, ServerInfo{}, fmt.Errorf("generate Incus client certificate: %w", err)
	}
	credentials.ClientCertificate = string(clientCert)
	credentials.ClientKey = string(clientKey)
	clientFingerprint := certificateFingerprint(credentials.ClientCertificate)
	if clientFingerprint == "" {
		return Credentials{}, ServerInfo{}, errors.New("generated Incus client certificate is invalid")
	}

	bootstrap, err := incus.ConnectIncusWithContext(ctx, endpoint, &incus.ConnectionArgs{
		TLSClientCert: credentials.ClientCertificate,
		TLSClientKey:  credentials.ClientKey,
		TLSServerCert: credentials.ServerCertificate,
		SkipGetEvents: true,
		SkipGetServer: true,
	})
	if err != nil {
		return Credentials{}, ServerInfo{}, fmt.Errorf("prepare Incus certificate enrollment: %w", err)
	}
	defer bootstrap.Disconnect()

	// Incus ignores Restricted/Projects on token-based enrollment and applies
	// whatever the token was issued with, so the request is only a hint; the
	// granted certificate is read back and verified below.
	cert := api.CertificatesPost{
		CertificatePut: api.CertificatePut{
			Name:        "webkvm-" + strings.ToLower(token.ClientName),
			Type:        api.CertificateTypeClient,
			Restricted:  true,
			Projects:    []string{project},
			Certificate: credentials.ClientCertificate,
			Description: "WebKVM remote Incus node",
		},
		TrustToken: token.Secret,
	}
	if err := bootstrap.CreateCertificate(cert); err != nil {
		return Credentials{}, ServerInfo{}, fmt.Errorf("trust WebKVM client certificate on Incus: %w", err)
	}

	// From here on the certificate is in the remote trust store: every
	// failure must revoke it so a half-enrolled node leaves nothing behind.
	revoke := func(cause error) error { return revokeOrphanedCertificate(bootstrap, clientFingerprint, cause) }

	trusted, err := Connect(ctx, endpoint, credentials, project)
	if err != nil {
		return Credentials{}, ServerInfo{}, revoke(fmt.Errorf("verify trusted Incus client certificate: %w", err))
	}
	defer trusted.Disconnect()
	server, _, err := trusted.GetServer()
	if err != nil {
		return Credentials{}, ServerInfo{}, revoke(fmt.Errorf("verify Incus project access: %w", err))
	}
	if server == nil || server.Auth != "trusted" {
		return Credentials{}, ServerInfo{}, revoke(errors.New("remote Incus did not trust the WebKVM client certificate after enrollment"))
	}
	if !trusted.HasExtension("certificate_project") {
		return Credentials{}, ServerInfo{}, revoke(errors.New("remote Incus server lacks project-restricted client certificate support; update Incus before linking it"))
	}
	granted, _, err := trusted.GetCertificate(clientFingerprint)
	if err != nil {
		return Credentials{}, ServerInfo{}, revoke(fmt.Errorf("read back the WebKVM client certificate from Incus: %w", err))
	}
	if granted == nil || !granted.Restricted || len(granted.Projects) != 1 || granted.Projects[0] != project {
		return Credentials{}, ServerInfo{}, revoke(fmt.Errorf("remote Incus granted the WebKVM client certificate without restricting it to project %q; issue the trust token with --restricted --projects %s", project, project))
	}
	if _, _, err := trusted.UseProject(project).GetProject(project); err != nil {
		return Credentials{}, ServerInfo{}, revoke(fmt.Errorf("remote Incus project %q is unavailable to the restricted WebKVM certificate: %w", project, err))
	}
	return credentials, serverInfo(server), nil
}

// revokeOrphanedCertificate best-effort removes a freshly enrolled certificate
// after a verification failure and reports both errors when cleanup fails too.
func revokeOrphanedCertificate(client incus.InstanceServer, fingerprint string, cause error) error {
	if client == nil || fingerprint == "" {
		return cause
	}
	if err := client.DeleteCertificate(fingerprint); err != nil {
		return fmt.Errorf("%w (revoking the orphaned client certificate also failed: %v; remove it with: incus config trust remove %s)", cause, err, fingerprint)
	}
	return cause
}

// Connect opens a pinned TLS connection and scopes it to the selected project.
func Connect(ctx context.Context, rawEndpoint string, credentials Credentials, project string) (incus.InstanceServer, error) {
	endpoint, err := NormalizeEndpoint(rawEndpoint)
	if err != nil {
		return nil, err
	}
	if credentials.ClientCertificate == "" || credentials.ClientKey == "" || credentials.ServerCertificate == "" {
		return nil, errors.New("incus client credentials are incomplete")
	}
	client, err := incus.ConnectIncusWithContext(ctx, endpoint, &incus.ConnectionArgs{
		TLSClientCert: credentials.ClientCertificate,
		TLSClientKey:  credentials.ClientKey,
		TLSServerCert: credentials.ServerCertificate,
		SkipGetEvents: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to Incus API: %w", err)
	}
	if project != "" {
		client = client.UseProject(project)
	}
	return client, nil
}

// Revoke removes the WebKVM client certificate from the remote Incus trust store.
func Revoke(ctx context.Context, rawEndpoint string, credentials Credentials, project string) error {
	client, err := Connect(ctx, rawEndpoint, credentials, project)
	if err != nil {
		return err
	}
	defer client.Disconnect()
	fingerprint := CertificateFingerprint(credentials.ClientCertificate)
	if fingerprint == "" {
		return errors.New("invalid stored Incus client certificate")
	}
	return client.DeleteCertificate(fingerprint)
}

func serverInfo(server *api.Server) ServerInfo {
	if server == nil {
		return ServerInfo{}
	}
	return ServerInfo{
		Name:         server.Environment.ServerName,
		Version:      server.Environment.ServerVersion,
		OS:           server.Environment.OSName,
		OSVersion:    server.Environment.OSVersion,
		Architecture: server.Environment.KernelArchitecture,
		Project:      server.Environment.Project,
		Clustered:    server.Environment.ServerClustered,
		Extensions:   server.APIExtensions,
	}
}

func fetchServerCertificate(ctx context.Context, endpoint string) ([]byte, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, "", err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, "", fmt.Errorf("connect to Incus HTTPS endpoint: %w", err)
	}
	defer conn.Close()

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: u.Hostname(),
		MinVersion: tls.VersionTLS12,
		// codeql[go/disabled-certificate-check] Trust-on-first-use: the
		// one-time Incus trust token pins this certificate before any
		// credentials are sent.
		InsecureSkipVerify: true,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, "", fmt.Errorf("TLS handshake with Incus endpoint: %w", err)
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, "", errors.New("incus endpoint did not present a TLS certificate")
	}
	cert := state.PeerCertificates[0]
	now := time.Now()
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return nil, "", errors.New("incus server certificate is not currently valid")
	}
	// No hostname check: Incus self-signed certificates only carry the
	// hostname plus loopback addresses, so an IP endpoint never matches the
	// SANs. The server is authenticated by the trust-token fingerprint pin
	// instead, and the Incus client pins the same certificate afterwards.
	// Incus trust tokens bind to the full-certificate SHA-256 fingerprint.
	fingerprint := certificateFingerprintFromRaw(cert.Raw)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), fingerprint, nil
}

func certificateFingerprint(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	return certificateFingerprintFromRaw(cert.Raw)
}

// CertificateFingerprint returns the SHA-256 fingerprint of a PEM certificate.
func CertificateFingerprint(certPEM string) string { return certificateFingerprint(certPEM) }

func certificateFingerprintFromRaw(raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func validFingerprint(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(raw, ":", "")))
	_, err := hex.DecodeString(raw)
	return err == nil && len(raw) == 64
}
