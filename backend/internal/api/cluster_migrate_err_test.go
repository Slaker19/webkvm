package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestMigrateErrorResponseIsCleanAndCoded(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantStatus int
		wantCode   string
	}{
		{"dest disk missing", "virError(Code=49, Domain=18, Message='Storage pool not found: no storage pool with matching target path '/tmp'')", http.StatusConflict, "migration_dest_storage_missing"},
		{"dest disk missing (qemu)", "internal error: unable to execute QEMU command 'blockdev-add': Could not open '/var/lib/x.qcow2': No such file or directory", http.StatusConflict, "migration_dest_storage_missing"},
		{"vm busy", "virError(Code=55, Domain=10, Message='Timed out during operation: cannot acquire state change lock (held by monitor=remoteDispatchDomainMigratePerform3)')", http.StatusConflict, "vm_busy"},
		{"dest unreachable", "virError(Code=38, Domain=7, Message='unable to connect to server at 'kvm2:49152': Connection timed out')", http.StatusBadGateway, "migration_dest_unreachable"},
		{"unknown", "virError(Code=1, Domain=10, Message='internal error: something /secret/path odd')", http.StatusInternalServerError, "migration_failed"},
	}
	for _, c := range cases {
		st, code, msg := migrateErrorResponse(errors.New(c.raw))
		if st != c.wantStatus || code != c.wantCode {
			t.Errorf("%s: got %d %s, want %d %s", c.name, st, code, c.wantStatus, c.wantCode)
		}
		if strings.Contains(msg, "virError") || strings.Contains(msg, "Domain=") || strings.Contains(msg, "/secret/path") {
			t.Errorf("%s: raw libvirt text leaked: %q", c.name, msg)
		}
		if msg == "" {
			t.Errorf("%s: empty message", c.name)
		}
	}
}
