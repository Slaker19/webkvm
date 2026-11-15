package nodes

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRemoteLibvirtURI(t *testing.T) {
	ok := []string{"qemu+ssh://root@10.0.0.2/system", "qemu+ssh://host.example/system", "qemu+ssh://u@[::1]:2222/system", " qemu+ssh://u@h/system "}
	for _, u := range ok {
		if err := ValidateRemoteLibvirtURI(u); err != nil {
			t.Errorf("%q rejected: %v", u, err)
		}
	}
	bad := []string{
		"qemu+tcp://h:16509/system", "qemu+ssh://u:pw@h/system", "qemu+ssh://u@h/system?command=/bin/sh",
		"qemu+ssh://u@h/system?netcat=nc", "qemu+ssh://u@h/system?keyfile=/etc/shadow", "qemu+ssh://u@h/system?no_verify=1",
		"qemu+ext://u@h/system?command=/bin/x", "qemu:///system", "https://h:8080", "qemu+ssh://u@h/session",
		"qemu+ssh:///system", "qemu+ssh://-oProxyCommand=x@h/system", "qemu+ssh://u@-h/system", "qemu+ssh://u@h/system#f", "garbage",
	}
	for _, u := range bad {
		err := ValidateRemoteLibvirtURI(u)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Code != CodeInvalidURI {
			t.Errorf("%q should be invalid_uri, got %v", u, err)
		}
	}
	if err := ValidateRemoteLibvirtURI("qemu+tcp://h/system"); err == nil || !strings.Contains(err.Error(), "qemu+ssh") {
		t.Fatalf("qemu+tcp message must point to qemu+ssh/Incus: %v", err)
	}
}

func TestRedactURI(t *testing.T) {
	for in, want := range map[string]string{
		"qemu+ssh://user:s3cr3t@host/system": "qemu+ssh://***@host/system",
		"qemu+ssh://user@host/system":        "qemu+ssh://***@host/system",
		"qemu+ssh://host/system":             "qemu+ssh://host/system",
	} {
		if got := RedactURI(in); got != want {
			t.Errorf("RedactURI(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(RedactURI(in), "s3cr3t") {
			t.Error("secret leaked")
		}
	}
}
