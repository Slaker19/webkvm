package api

import (
	"errors"
	"testing"

	"webkvm/internal/libvirt"
	"webkvm/internal/nodes"
)

func TestSSHSetupPendingCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
		ok   bool
	}{
		{libvirt.ErrNoSSHKey, "no_key", true},
		{nodes.ErrHostKeyUnpinned, "host_key_unpinned", true},
		{libvirt.ErrSSHAuthDenied, "ssh_auth_denied", true},
		{libvirt.ErrHostKeyChanged, "", false},
		{libvirt.ErrNodeUnreachable, "", false},
		{errors.New("remote libvirt connection failed"), "", false},
	} {
		code, ok := sshSetupPendingCode(tc.err)
		if code != tc.code || ok != tc.ok {
			t.Errorf("%v: got (%q,%v), want (%q,%v)", tc.err, code, ok, tc.code, tc.ok)
		}
	}
}
