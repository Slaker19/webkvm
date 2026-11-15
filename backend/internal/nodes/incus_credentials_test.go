package nodes

import (
	"os"
	"path/filepath"
	"testing"

	sharedtls "github.com/lxc/incus/v7/shared/tls"
)

func TestIncusCredentialStoreRoundTripUsesPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	store := NewIncusCredentialStore(dir)
	clientCert, clientKey, err := sharedtls.GenerateMemCert(true, false)
	if err != nil {
		t.Fatal(err)
	}
	serverCert, _, err := sharedtls.GenerateMemCert(false, false)
	if err != nil {
		t.Fatal(err)
	}
	want := IncusCredentials{
		ClientCertificate: string(clientCert),
		ClientKey:         string(clientKey),
		ServerCertificate: string(serverCert),
	}
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := store.Save(id, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Load() = %+v, want stored credentials", got)
	}

	for _, path := range []string{
		filepath.Join(dir, "incus-credentials"),
		filepath.Join(dir, "incus-credentials", id),
		filepath.Join(dir, "incus-credentials", id, "client.key"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has permissive mode %o", path, info.Mode().Perm())
		}
	}
	if err := store.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(id); !os.IsNotExist(err) {
		t.Fatalf("Load after Delete error = %v, want not-exist", err)
	}
}

func TestIncusCredentialStoreRejectsUnsafeIDsAndInvalidKeys(t *testing.T) {
	store := NewIncusCredentialStore(t.TempDir())
	if err := store.Save("../outside", IncusCredentials{}); err == nil {
		t.Fatal("unsafe credential ID was accepted")
	}
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := store.Save(id, IncusCredentials{ClientCertificate: "cert", ClientKey: "not a private key", ServerCertificate: "server"}); err == nil {
		t.Fatal("invalid TLS keypair was accepted")
	}
}
