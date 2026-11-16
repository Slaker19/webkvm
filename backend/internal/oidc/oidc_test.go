package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestStateRoundTrip(t *testing.T) {
	c := &Client{stateKey: []byte("0123456789abcdef0123456789abcdef")}
	state, nonce, verifier, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.SignState(state, nonce, verifier)
	if err != nil {
		t.Fatal(err)
	}
	gotState, gotNonce, gotVerifier, err := c.VerifyState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if gotState != state || gotNonce != nonce || gotVerifier != verifier {
		t.Fatalf("round trip = %q %q %q", gotState, gotNonce, gotVerifier)
	}
	if _, _, _, err := c.VerifyState(raw + "x"); err == nil {
		t.Fatal("tampered state accepted")
	}
}

func TestClientDiscoveryAuthURLAndCodeExchange(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/auth",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
			"scopes_supported":                      []string{"openid", "profile", "email"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": "test-key",
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "good" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		if r.Form.Get("code_verifier") == "" {
			http.Error(w, "missing code_verifier", http.StatusBadRequest)
			return
		}
		claims := jwt.MapClaims{
			"iss":   issuer,
			"aud":   "client",
			"sub":   "user-1",
			"nonce": "nonce-1",
			"iat":   time.Now().Unix(),
			"exp":   time.Now().Add(time.Hour).Unix(),
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "test-key"
		signed, err := tok.SignedString(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-1", "token_type": "Bearer", "expires_in": 3600, "id_token": signed,
		})
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	issuer = srv.URL

	c, err := New(context.Background(), Config{
		Enabled: true, Issuer: issuer, ClientID: "client", ClientSecret: "secret",
		RedirectURL: issuer + "/callback", Scopes: []string{"openid", "profile", "email"},
		HTTPClient: srv.Client(),
	}, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	authURL := c.AuthURL("state-1", "verifier-1")
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != "client" || q.Get("state") != "state-1" || q.Get("code_challenge") == "" {
		t.Fatalf("auth URL = %s", authURL)
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
	claims, err := c.Exchange(context.Background(), "good", "verifier-1", "nonce-1")
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-1" || claims.Nonce != "nonce-1" || claims.Issuer != issuer {
		t.Fatalf("claims = %+v", claims.TokenClaims)
	}
}
