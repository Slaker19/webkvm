package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"webkvm/internal/config"
	"webkvm/internal/models"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

// WebAuthnUser wraps models.User to satisfy webauthn.User interface.
type WebAuthnUser struct {
	User *models.User
}

func (u *WebAuthnUser) WebAuthnID() []byte {
	h := sha256.Sum256([]byte(u.User.Username))
	return h[:]
}

func (u *WebAuthnUser) WebAuthnName() string {
	return u.User.Username
}

func (u *WebAuthnUser) WebAuthnDisplayName() string {
	if u.User.Email != "" {
		return u.User.Email
	}
	return u.User.Username
}

func (u *WebAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	var creds []webauthn.Credential
	for _, c := range u.User.WebAuthnCredentials {
		if c.RawJSON != "" {
			var wc webauthn.Credential
			if err := json.Unmarshal([]byte(c.RawJSON), &wc); err == nil {
				creds = append(creds, wc)
				continue
			}
		}
		rawID, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(c.ID, "="))
		if err != nil {
			rawID, _ = base64.URLEncoding.DecodeString(c.ID)
		}
		creds = append(creds, webauthn.Credential{
			ID:              rawID,
			AttestationType: c.AttestationType,
			Authenticator: webauthn.Authenticator{
				SignCount: c.SignCount,
			},
		})
	}
	return creds
}

type WebAuthnSession struct {
	ID          string
	Username    string
	SessionData *webauthn.SessionData
	CreatedAt   time.Time
}

// WebAuthnManager coordinates WebAuthn registration and authentication ceremonies.
type WebAuthnManager struct {
	cfg       *config.Config
	mu        sync.RWMutex
	instances map[string]*webauthn.WebAuthn
	sessions  map[string]*WebAuthnSession
}

func NewWebAuthnManager(cfg *config.Config) *WebAuthnManager {
	return &WebAuthnManager{
		cfg:       cfg,
		instances: make(map[string]*webauthn.WebAuthn),
		sessions:  make(map[string]*WebAuthnSession),
	}
}

// reapStaleSessions removes expired sessions. Caller must hold m.mu Lock.
func (m *WebAuthnManager) reapStaleSessions() {
	cutoff := time.Now().Add(-10 * time.Minute)
	for id, s := range m.sessions {
		if s.CreatedAt.Before(cutoff) {
			delete(m.sessions, id)
		}
	}
}

// ResolveRPIDAndOrigin determines the Relying Party ID and Origins from config or request.
func (m *WebAuthnManager) ResolveRPIDAndOrigin(r *http.Request) (string, []string, error) {
	if m.cfg.WebAuthnRPID != "" {
		origins := m.cfg.WebAuthnRPOrigins
		if len(origins) == 0 {
			origins = []string{originFromReq(r)}
		}
		return m.cfg.WebAuthnRPID, origins, nil
	}

	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}

	// W3C WebAuthn spec §5.4.2: RP ID cannot be an IP address.
	// Loopback IPs (127.0.0.1, ::1) are mapped to "localhost".
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			host = "localhost"
		} else {
			return "", nil, fmt.Errorf("WebAuthn requires a domain name (e.g. webkvm.local) or localhost per W3C specification (§5.4.2); host %q is an IP address. Configure WEBAUTHN_RP_ID in your configuration", host)
		}
	}

	reqOrigin := originFromReq(r)
	origins := []string{reqOrigin}
	if host == "localhost" {
		origins = append(origins, "http://localhost:8080", "https://localhost:8080", "https://localhost:8081", "http://localhost:8081", "http://localhost:5173", "https://localhost:5173")
	}

	return host, origins, nil
}

func originFromReq(r *http.Request) string {
	if orig := r.Header.Get("Origin"); orig != "" {
		return strings.TrimRight(orig, "/")
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}

// GetWebAuthn returns or creates a WebAuthn relying party instance for the request.
func (m *WebAuthnManager) GetWebAuthn(r *http.Request) (*webauthn.WebAuthn, error) {
	rpid, origins, err := m.ResolveRPIDAndOrigin(r)
	if err != nil {
		return nil, err
	}

	key := fmt.Sprintf("%s|%s", rpid, strings.Join(origins, ","))
	m.mu.RLock()
	inst, ok := m.instances[key]
	m.mu.RUnlock()
	if ok {
		return inst, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok = m.instances[key]; ok {
		return inst, nil
	}

	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "WebKVM",
		RPID:          rpid,
		RPOrigins:     origins,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize webauthn: %w", err)
	}

	m.instances[key] = wa
	return wa, nil
}

// BeginRegistration starts a WebAuthn credential registration ceremony.
func (m *WebAuthnManager) BeginRegistration(u *models.User, r *http.Request) (*protocol.CredentialCreation, string, error) {
	wa, err := m.GetWebAuthn(r)
	if err != nil {
		return nil, "", err
	}

	webUser := &WebAuthnUser{User: u}
	creation, sessionData, err := wa.BeginRegistration(webUser)
	if err != nil {
		return nil, "", fmt.Errorf("begin registration: %w", err)
	}

	sessionID := uuid.NewString()
	m.mu.Lock()
	m.reapStaleSessions()
	m.sessions[sessionID] = &WebAuthnSession{
		ID:          sessionID,
		Username:    u.Username,
		SessionData: sessionData,
		CreatedAt:   time.Now(),
	}
	m.mu.Unlock()

	return creation, sessionID, nil
}

// FinishRegistration verifies the client's credential creation response.
func (m *WebAuthnManager) FinishRegistration(u *models.User, sessionID, credName string, bodyBytes []byte, r *http.Request) (*models.WebAuthnCredential, error) {
	m.mu.Lock()
	session, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	if !ok || session.SessionData == nil {
		return nil, fmt.Errorf("invalid or expired webauthn registration session")
	}
	if session.Username != u.Username {
		return nil, fmt.Errorf("webauthn registration session user mismatch")
	}

	wa, err := m.GetWebAuthn(r)
	if err != nil {
		return nil, err
	}

	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("parse credential creation: %w", err)
	}

	webUser := &WebAuthnUser{User: u}
	cred, err := wa.CreateCredential(webUser, *session.SessionData, parsed)
	if err != nil {
		return nil, fmt.Errorf("verify credential creation: %w", err)
	}

	rawJSON, _ := json.Marshal(cred)
	idStr := base64.RawURLEncoding.EncodeToString(cred.ID)
	if credName == "" {
		credName = "Passkey " + idStr[:min(6, len(idStr))]
	}

	var transports []string
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}

	return &models.WebAuthnCredential{
		ID:              idStr,
		Name:            credName,
		AttestationType: cred.AttestationType,
		AAGUID:          fmt.Sprintf("%x", cred.Authenticator.AAGUID),
		SignCount:       cred.Authenticator.SignCount,
		CreatedAt:       time.Now().UTC(),
		Transport:       transports,
		RawJSON:         string(rawJSON),
	}, nil
}

// BeginLogin starts a WebAuthn authentication ceremony (discoverable or user-targeted).
func (m *WebAuthnManager) BeginLogin(u *models.User, r *http.Request) (*protocol.CredentialAssertion, string, error) {
	wa, err := m.GetWebAuthn(r)
	if err != nil {
		return nil, "", err
	}

	var assertion *protocol.CredentialAssertion
	var sessionData *webauthn.SessionData

	if u != nil {
		webUser := &WebAuthnUser{User: u}
		assertion, sessionData, err = wa.BeginLogin(webUser)
	} else {
		assertion, sessionData, err = wa.BeginDiscoverableLogin()
	}
	if err != nil {
		return nil, "", fmt.Errorf("begin webauthn login: %w", err)
	}

	sessionID := uuid.NewString()
	username := ""
	if u != nil {
		username = u.Username
	}

	m.mu.Lock()
	m.reapStaleSessions()
	m.sessions[sessionID] = &WebAuthnSession{
		ID:          sessionID,
		Username:    username,
		SessionData: sessionData,
		CreatedAt:   time.Now(),
	}
	m.mu.Unlock()

	return assertion, sessionID, nil
}

// FinishLogin verifies the client's assertion response.
func (m *WebAuthnManager) FinishLogin(sessionID string, findUser func(rawID, userHandle []byte) (*models.User, error), bodyBytes []byte, r *http.Request) (*models.User, *models.WebAuthnCredential, error) {
	m.mu.Lock()
	session, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	if !ok || session.SessionData == nil {
		return nil, nil, fmt.Errorf("invalid or expired webauthn login session")
	}

	wa, err := m.GetWebAuthn(r)
	if err != nil {
		return nil, nil, err
	}

	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("parse credential request: %w", err)
	}

	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		u, err := findUser(rawID, userHandle)
		if err != nil {
			return nil, err
		}
		return &WebAuthnUser{User: u}, nil
	}

	resUser, cred, err := wa.ValidatePasskeyLogin(handler, *session.SessionData, parsed)
	if err != nil {
		return nil, nil, fmt.Errorf("validate passkey login: %w", err)
	}

	wUser, ok := resUser.(*WebAuthnUser)
	if !ok || wUser.User == nil {
		return nil, nil, fmt.Errorf("user resolution failed")
	}

	idStr := base64.RawURLEncoding.EncodeToString(cred.ID)
	var matchedCred *models.WebAuthnCredential
	for i := range wUser.User.WebAuthnCredentials {
		if strings.TrimRight(wUser.User.WebAuthnCredentials[i].ID, "=") == strings.TrimRight(idStr, "=") {
			matchedCred = &wUser.User.WebAuthnCredentials[i]
			break
		}
	}

	return wUser.User, matchedCred, nil
}
