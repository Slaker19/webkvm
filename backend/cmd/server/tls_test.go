package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"webkvm/internal/configstore"
)

func newTestStore(t *testing.T, vals configstore.Set) (*configstore.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := configstore.New(dir, configstore.DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) > 0 {
		_, failed, err := s.SetMany(vals)
		if err != nil || len(failed) > 0 {
			t.Fatalf("SetMany failed: %v %v", err, failed)
		}
	}
	return s, dir
}

func writeSelfSigned(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "webkvm"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"webkvm", "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestConfigureTLS_Off(t *testing.T) {
	s, _ := newTestStore(t, nil)
	conf, mode := configureTLS(s, t.TempDir(), slog.Default())
	if conf != nil || mode != tlsModeOff {
		t.Fatalf("expected off, got mode=%s conf=%v", mode, conf)
	}
}

func TestConfigureTLS_SelfSigned(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir)
	s, _ := newTestStore(t, configstore.Set{
		"server.tls_cert": cert,
		"server.tls_key":  key,
	})
	conf, mode := configureTLS(s, dir, slog.Default())
	if mode != tlsModeSelfSigned || conf == nil {
		t.Fatalf("expected selfsigned, got mode=%s conf=%v", mode, conf)
	}
	if len(conf.Certificates) != 1 {
		t.Fatalf("expected 1 loaded certificate, got %d", len(conf.Certificates))
	}
	if conf.MinVersion == 0 {
		t.Fatal("expected MinVersion set")
	}
}

func TestConfigureTLS_LetsEncryptWithFallback(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir)
	s, _ := newTestStore(t, configstore.Set{
		"server.tls_cert":   cert,
		"server.tls_key":    key,
		"server.tls_domain": "webkvm.example.com",
	})
	conf, mode := configureTLS(s, dir, slog.Default())
	if mode != tlsModeLetsEncrypt || conf == nil {
		t.Fatalf("expected letsencrypt, got mode=%s conf=%v", mode, conf)
	}
	if conf.GetCertificate == nil {
		t.Fatal("expected GetCertificate to be set")
	}
}

func TestConfigureTLS_KeyOnly(t *testing.T) {
	dir := t.TempDir()
	cert, _ := writeSelfSigned(t, dir)
	s, _ := newTestStore(t, configstore.Set{
		"server.tls_cert": cert,
	})
	conf, mode := configureTLS(s, dir, slog.Default())
	if mode != tlsModeOff || conf != nil {
		t.Fatalf("expected off when only cert set, got mode=%s", mode)
	}
}