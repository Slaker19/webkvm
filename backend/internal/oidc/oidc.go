// Package oidc implements the OpenID Connect authorization-code flow used by
// the optional SSO login. Configuration is environment-driven so the client
// secret never enters the settings JSON or the API responses.
package oidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/zitadel/oidc/v3/pkg/client/rp"
	"github.com/zitadel/oidc/v3/pkg/oidc"
)

type Config struct {
	Enabled           bool
	Issuer            string
	ClientID          string
	ClientSecret      string
	RedirectURL       string
	Scopes            []string
	UsernameClaim     string
	RoleClaim         string
	RoleMap           map[string]string
	DefaultRole       string
	AutoProvision     bool
	DisableLocalLogin bool
	ButtonLabel       string

	// HTTPClient is for tests (a TLS test server). Production leaves it nil.
	HTTPClient *http.Client
}

func FromEnv() (Config, error) {
	cfg := Config{
		Enabled:       envBool("WEBKVM_OIDC_ENABLED"),
		Issuer:        strings.TrimSpace(os.Getenv("WEBKVM_OIDC_ISSUER")),
		ClientID:      strings.TrimSpace(os.Getenv("WEBKVM_OIDC_CLIENT_ID")),
		ClientSecret:  os.Getenv("WEBKVM_OIDC_CLIENT_SECRET"),
		RedirectURL:   strings.TrimSpace(os.Getenv("WEBKVM_OIDC_REDIRECT_URL")),
		UsernameClaim: envOrDefault("WEBKVM_OIDC_USERNAME_CLAIM", "preferred_username"),
		RoleClaim:     envOrDefault("WEBKVM_OIDC_ROLE_CLAIM", "roles"),
		DefaultRole:   envOrDefault("WEBKVM_OIDC_DEFAULT_ROLE", "viewer"),
		AutoProvision: envBool("WEBKVM_OIDC_AUTO_PROVISION"),
		ButtonLabel:   envOrDefault("WEBKVM_OIDC_BUTTON_LABEL", "SSO"),
	}
	cfg.DisableLocalLogin = envBool("WEBKVM_OIDC_DISABLE_LOCAL_LOGIN")
	scopes := strings.TrimSpace(os.Getenv("WEBKVM_OIDC_SCOPES"))
	if scopes == "" {
		cfg.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	} else {
		for _, s := range strings.Split(scopes, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.Scopes = append(cfg.Scopes, s)
			}
		}
	}
	hasOpenID := false
	for _, s := range cfg.Scopes {
		if s == oidc.ScopeOpenID {
			hasOpenID = true
		}
	}
	if !hasOpenID {
		cfg.Scopes = append([]string{oidc.ScopeOpenID}, cfg.Scopes...)
	}
	if raw := strings.TrimSpace(os.Getenv("WEBKVM_OIDC_ROLE_MAP")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.RoleMap); err != nil {
			return Config{}, fmt.Errorf("WEBKVM_OIDC_ROLE_MAP: %w", err)
		}
	}
	switch cfg.DefaultRole {
	case "admin", "operator", "viewer":
	default:
		return Config{}, fmt.Errorf("WEBKVM_OIDC_DEFAULT_ROLE must be admin, operator or viewer")
	}
	if !cfg.Enabled {
		return cfg, nil
	}
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" {
		return Config{}, errors.New("OIDC is enabled but issuer, client ID, client secret or redirect URL is missing")
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Issuer == "" || c.ClientID == "" || c.ClientSecret == "" || c.RedirectURL == "" {
		return errors.New("issuer, client ID, client secret and redirect URL are required")
	}
	return nil
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return v
}

type nonceContextKey struct{}

func nonceFromContext(ctx context.Context) string {
	v, _ := ctx.Value(nonceContextKey{}).(string)
	return v
}

type stateClaims struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	jwt.RegisteredClaims
}

type Client struct {
	cfg      Config
	rp       rp.RelyingParty
	stateKey []byte
}

func New(ctx context.Context, cfg Config, stateKey []byte) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(stateKey) < 16 {
		return nil, errors.New("OIDC state signing key is too short")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	opts := []rp.Option{
		rp.WithVerifierOpts(rp.WithNonce(nonceFromContext)),
		rp.WithHTTPClient(httpClient),
	}
	r, err := rp.NewRelyingPartyOIDC(ctx, cfg.Issuer, cfg.ClientID, cfg.ClientSecret, cfg.RedirectURL, cfg.Scopes, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, rp: r, stateKey: append([]byte(nil), stateKey...)}, nil
}

func (c *Client) Enabled() bool { return c != nil && c.cfg.Enabled }
func (c *Client) Config() Config {
	if c == nil {
		return Config{}
	}
	return c.cfg
}

func (c *Client) AuthURL(state, verifier string) string {
	return rp.AuthURL(state, c.rp, rp.WithCodeChallenge(oidc.NewSHACodeChallenge(verifier)))
}

func (c *Client) Exchange(ctx context.Context, code, verifier, nonce string) (*oidc.IDTokenClaims, error) {
	ctx = context.WithValue(ctx, nonceContextKey{}, nonce)
	tokens, err := rp.CodeExchange[*oidc.IDTokenClaims](ctx, code, c.rp, rp.WithCodeVerifier(verifier))
	if err != nil {
		return nil, err
	}
	if tokens.IDTokenClaims == nil {
		return nil, errors.New("OIDC token response has no ID token claims")
	}
	return tokens.IDTokenClaims, nil
}

func (c *Client) SignState(state, nonce, verifier string) (string, error) {
	now := time.Now()
	claims := stateClaims{
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(c.stateKey)
}

func (c *Client) VerifyState(raw string) (state, nonce, verifier string, err error) {
	var claims stateClaims
	tok, err := jwt.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) {
		return c.stateKey, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !tok.Valid {
		return "", "", "", errors.New("invalid OIDC state")
	}
	if claims.State == "" || claims.Nonce == "" || claims.Verifier == "" {
		return "", "", "", errors.New("incomplete OIDC state")
	}
	return claims.State, claims.Nonce, claims.Verifier, nil
}

// NewState returns the random values used by one authorization request.
func NewState() (state, nonce, verifier string, err error) {
	state, err = randomHex(24)
	if err != nil {
		return "", "", "", err
	}
	nonce, err = randomHex(24)
	if err != nil {
		return "", "", "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	return state, nonce, verifier, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
