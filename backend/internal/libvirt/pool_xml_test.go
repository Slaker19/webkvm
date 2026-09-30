package libvirt

import (
	"strings"
	"testing"

	"webkvm/internal/models"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreCurrent(),
		// ensureEventLoop (connect.go) starts libvirt's default event
		// loop on a permanently OS-thread-locked goroutine exactly
		// once per process (sync.Once) — by design it never exits.
		// Any test that calls Open() (connect_test.go) triggers this
		// for the first time, which goleak would otherwise always
		// flag as a leak.
		goleak.IgnoreTopFunction("libvirt.org/go/libvirt._Cfunc_virEventRunDefaultImplWrapper"),
	)
}

func TestBuildPoolXMLDir(t *testing.T) {
	got, err := buildPoolXML("dir", models.CreatePoolRequest{
		Name: "data1", Path: "/mnt/data1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "type='dir'") || !strings.Contains(got, "<name>data1</name>") ||
		!strings.Contains(got, "<path>/mnt/data1</path>") {
		t.Fatalf("dir XML missing pieces: %s", got)
	}
}

func TestBuildPoolXMLNFS(t *testing.T) {
	got, err := buildPoolXML("netfs", models.CreatePoolRequest{
		Name: "nfs1", Path: "/mnt/nfs1",
		SourceHost: "10.0.0.5", SourceDir: "/export/vms", SourceFormat: "nfs",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"type='netfs'",
		"<name>nfs1</name>",
		"host name='10.0.0.5'",
		"dir path='/export/vms'",
		"format type='nfs'",
		"<path>/mnt/nfs1</path>",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in: %s", w, got)
		}
	}
}

func TestBuildPoolXMLCIFS(t *testing.T) {
	got, err := buildPoolXML("netfs", models.CreatePoolRequest{
		Name: "smb1", Path: "/mnt/smb1",
		SourceHost: "files.example.com", SourceDir: "/share", SourceFormat: "cifs",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "format type='cifs'") {
		t.Fatalf("expected cifs format: %s", got)
	}
}

func TestBuildPoolXMLBadName(t *testing.T) {
	_, err := buildPoolXML("dir", models.CreatePoolRequest{Name: "bad name with spaces", Path: "/x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildPoolXMLBadPath(t *testing.T) {
	_, err := buildPoolXML("dir", models.CreatePoolRequest{Name: "ok", Path: "relative"})
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestBuildPoolXMLNFSMissingSource(t *testing.T) {
	_, err := buildPoolXML("netfs", models.CreatePoolRequest{Name: "nfs1", Path: "/mnt/nfs1"})
	if err == nil {
		t.Fatal("expected error for missing source_host")
	}
	_, err = buildPoolXML("netfs", models.CreatePoolRequest{Name: "nfs1", Path: "/mnt/nfs1", SourceHost: "h"})
	if err == nil {
		t.Fatal("expected error for missing source_dir")
	}
}

func TestBuildPoolXMLUnsupportedType(t *testing.T) {
	_, err := buildPoolXML("glusterfs", models.CreatePoolRequest{Name: "i", Path: "/x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildPoolXMLISCSI(t *testing.T) {
	got, err := buildPoolXML("iscsi", models.CreatePoolRequest{
		Name:         "iscsi1",
		Path:         "/dev/disk/by-path",
		SourceHost:   "192.168.1.50",
		SourceDevice: "iqn.2003-01.org.linux-iscsi.target:sn.12345",
	})
	if err != nil {
		t.Fatal(err)
	}
	wants := []string{
		"<pool type='iscsi'>",
		"<name>iscsi1</name>",
		"<host name='192.168.1.50'/>",
		"<device path='iqn.2003-01.org.linux-iscsi.target:sn.12345'/>",
		"<path>/dev/disk/by-path</path>",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "<auth") {
		t.Errorf("unexpected <auth> block in:\n%s", got)
	}
	if extractSourceHost(got) != "192.168.1.50" {
		t.Errorf("extractSourceHost got %q, want %q", extractSourceHost(got), "192.168.1.50")
	}
	if extractSourceDevice(got) != "iqn.2003-01.org.linux-iscsi.target:sn.12345" {
		t.Errorf("extractSourceDevice got %q, want %q", extractSourceDevice(got), "iqn.2003-01.org.linux-iscsi.target:sn.12345")
	}
	if extractSourcePort(got) != 0 {
		t.Errorf("extractSourcePort got %d, want 0", extractSourcePort(got))
	}
}

func TestBuildPoolXMLISCSIWithCustomPort(t *testing.T) {
	got, err := buildPoolXML("iscsi", models.CreatePoolRequest{
		Name:         "iscsi-custom",
		Path:         "/dev/disk/by-path",
		SourceHost:   "san.local",
		SourcePort:   3261,
		SourceDevice: "iqn.2003-01.org.linux-iscsi.target:sn.99999",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "<host name='san.local' port='3261'/>") {
		t.Errorf("missing custom port in:\n%s", got)
	}
	if extractSourcePort(got) != 3261 {
		t.Errorf("extractSourcePort got %d, want 3261", extractSourcePort(got))
	}
}

func TestBuildPoolXMLISCSIWithCHAP(t *testing.T) {
	got, err := buildPoolXML("iscsi", models.CreatePoolRequest{
		Name:           "iscsi-chap",
		Path:           "/dev/disk/by-path",
		SourceHost:     "192.168.1.50",
		SourceDevice:   "iqn.2003-01.org.linux-iscsi.target:sn.12345",
		SourceUsername: "admin_user",
		SecretUUID:     "12345678-abcd-ef01-2345-6789abcdef01",
	})
	if err != nil {
		t.Fatal(err)
	}
	wants := []string{
		"<auth type='chap' username='admin_user'>",
		"<secret uuid='12345678-abcd-ef01-2345-6789abcdef01'/>",
		"</auth>",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestBuildPoolXMLISCSIMissingFields(t *testing.T) {
	_, err := buildPoolXML("iscsi", models.CreatePoolRequest{
		Name: "iscsi-bad",
		Path: "/dev/disk/by-path",
		// missing SourceHost and SourceDevice
	})
	if err == nil {
		t.Fatal("expected error for missing SourceHost")
	}

	_, err = buildPoolXML("iscsi", models.CreatePoolRequest{
		Name:       "iscsi-bad",
		Path:       "/dev/disk/by-path",
		SourceHost: "192.168.1.50",
		// missing SourceDevice
	})
	if err == nil {
		t.Fatal("expected error for missing SourceDevice")
	}

	_, err = buildPoolXML("iscsi", models.CreatePoolRequest{
		Name:           "iscsi-bad",
		Path:           "/dev/disk/by-path",
		SourceHost:     "192.168.1.50",
		SourceDevice:   "iqn.123",
		SourceUsername: "user",
		// missing SecretUUID
	})
	if err == nil {
		t.Fatal("expected error for missing SecretUUID when username is provided")
	}
}

func TestBuildPoolXMLInvalidFormat(t *testing.T) {
	_, err := buildPoolXML("netfs", models.CreatePoolRequest{
		Name: "nfs1", Path: "/mnt/nfs1",
		SourceHost: "h", SourceDir: "/e", SourceFormat: "ext4",
	})
	if err == nil {
		t.Fatal("expected error for bad format")
	}
}

func TestBuildPoolXMLXSSInName(t *testing.T) {
	got, err := buildPoolXML("dir", models.CreatePoolRequest{
		Name: "x' onload='alert(1)", // attempt injection
		Path: "/x",
	})
	// The name RE should reject it. If it didn't, the escape would
	// at least contain &apos; so the rendered XML stays inert.
	if err == nil && !strings.Contains(got, "&apos;") {
		t.Fatalf("name not escaped: %s", got)
	}
}

func TestBuildPoolXMLCIFSWithAuth(t *testing.T) {
	got, err := buildPoolXML("netfs", models.CreatePoolRequest{
		Name: "smb1", Path: "/mnt/smb1",
		SourceHost: "files.example.com", SourceDir: "/share",
		SourceFormat:   "cifs",
		SourceUsername: "alice",
		SecretUUID:     "abc-123-def",
	})
	if err != nil {
		t.Fatal(err)
	}
	wants := []string{
		"format type='cifs'",
		"<auth type='cifs' username='alice'",
		"<secret uuid='abc-123-def'/>",
		"</auth>",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in: %s", w, got)
		}
	}
}

func TestBuildPoolXMLCIFSWithUsernameButNoSecret(t *testing.T) {
	_, err := buildPoolXML("netfs", models.CreatePoolRequest{
		Name: "smb1", Path: "/mnt/smb1",
		SourceHost: "h", SourceDir: "/e",
		SourceFormat:   "cifs",
		SourceUsername: "alice",
		// SecretUUID deliberately empty.
	})
	if err == nil {
		t.Fatal("expected error when SourceUsername set but SecretUUID missing")
	}
}

func TestBuildPoolXMLNFSDoesNotEmitAuth(t *testing.T) {
	got, err := buildPoolXML("netfs", models.CreatePoolRequest{
		Name: "nfs1", Path: "/mnt/nfs1",
		SourceHost: "h", SourceDir: "/e",
		SourceFormat:   "nfs",
		SourceUsername: "should-be-ignored",
		SecretUUID:     "u-ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<auth") {
		t.Fatalf("NFS pool should not have <auth>: %s", got)
	}
}

func TestBuildPoolXMLCIFSNoUsernameNoAuth(t *testing.T) {
	got, err := buildPoolXML("netfs", models.CreatePoolRequest{
		Name: "anon", Path: "/mnt/anon",
		SourceHost: "h", SourceDir: "/e",
		SourceFormat: "cifs",
		// No SourceUsername, no SecretUUID — anonymous CIFS.
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<auth") {
		t.Fatalf("anonymous CIFS should not have <auth>: %s", got)
	}
}
