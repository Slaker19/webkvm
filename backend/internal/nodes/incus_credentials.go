package nodes

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var incusCredentialIDRE = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// IncusCredentials are private client TLS material plus its pinned server cert.
type IncusCredentials struct {
	ClientCertificate string
	ClientKey         string
	ServerCertificate string
}

// IncusCredentialStore keeps trusted-client private keys outside nodes.json.
type IncusCredentialStore struct {
	dir string
}

func NewIncusCredentialStore(dataDir string) *IncusCredentialStore {
	return &IncusCredentialStore{dir: filepath.Join(dataDir, "incus-credentials")}
}

func (s *IncusCredentialStore) Save(id string, credentials IncusCredentials) error {
	if !incusCredentialIDRE.MatchString(id) {
		return errors.New("invalid Incus credential ID")
	}
	if _, err := tls.X509KeyPair([]byte(credentials.ClientCertificate), []byte(credentials.ClientKey)); err != nil {
		return fmt.Errorf("invalid Incus client certificate/key pair: %w", err)
	}
	if block, _ := pem.Decode([]byte(credentials.ServerCertificate)); block == nil {
		return errors.New("invalid Incus server certificate")
	} else if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return fmt.Errorf("invalid Incus server certificate: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return err
	}
	dest := filepath.Join(s.dir, id)
	if _, err := os.Lstat(dest); err == nil {
		return errors.New("incus credentials already exist")
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.MkdirTemp(s.dir, ".incus-credential-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for name, content := range map[string]string{
		"client.crt": credentials.ClientCertificate,
		"client.key": credentials.ClientKey,
		"server.crt": credentials.ServerCertificate,
	} {
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dest)
}

func (s *IncusCredentialStore) Load(id string) (IncusCredentials, error) {
	if !incusCredentialIDRE.MatchString(id) {
		return IncusCredentials{}, errors.New("invalid Incus credential ID")
	}
	dir := filepath.Join(s.dir, id)
	info, err := os.Lstat(dir)
	if err != nil {
		return IncusCredentials{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return IncusCredentials{}, errors.New("incus credential directory permissions are unsafe")
	}
	read := func(name string) (string, error) {
		path := filepath.Join(dir, name)
		fileInfo, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("incus credential file %q has unsafe type or permissions", name)
		}
		b, err := os.ReadFile(path)
		return string(b), err
	}
	clientCert, err := read("client.crt")
	if err != nil {
		return IncusCredentials{}, err
	}
	clientKey, err := read("client.key")
	if err != nil {
		return IncusCredentials{}, err
	}
	serverCert, err := read("server.crt")
	if err != nil {
		return IncusCredentials{}, err
	}
	return IncusCredentials{ClientCertificate: clientCert, ClientKey: clientKey, ServerCertificate: serverCert}, nil
}

func (s *IncusCredentialStore) Delete(id string) error {
	if !incusCredentialIDRE.MatchString(id) {
		return errors.New("invalid Incus credential ID")
	}
	path := filepath.Join(s.dir, id)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("incus credential entry is not a directory")
	}
	return os.RemoveAll(path)
}
