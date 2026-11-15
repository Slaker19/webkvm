package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"webkvm/internal/nodes"
)

func overviewRow(t *testing.T, f *sshAPIFixture) map[string]any {
	t.Helper()
	r := f.admin(http.MethodGet, "/api/cluster/ssh", "")
	if r.Code != 200 {
		t.Fatalf("overview: %d %s", r.Code, r.Body.String())
	}
	var body struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil || len(body.Nodes) != 1 {
		t.Fatalf("overview body: %v %s", err, r.Body.String())
	}
	return body.Nodes[0]
}

// restart simulates a server restart: a brand-new store and handler over the
// same DATA_DIR (the in-memory verdicts are gone, the node registry reloads).
func (f *sshAPIFixture) restart(t *testing.T) {
	t.Helper()
	keys := nodes.NewSSHKeyStore(f.dir)
	f.h.sshKeys = keys
	f.h.sshTests = nil
}

func TestLastSSHTestSurvivesRestartWithoutSecrets(t *testing.T) {
	f := newSSHAPIFixture(t)
	f.setup(t)
	f.pin(t)
	if r := f.admin(http.MethodPost, f.nodePath("/ssh/test"), ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"ok":true`) {
		t.Fatalf("test: %d %s", r.Code, r.Body.String())
	}
	path := filepath.Join(f.dir, "cluster-ssh", "last_test.json")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("last_test.json: %v %v", err, st)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "ssh-ed25519") {
		t.Fatalf("secrets in last_test.json: %s", raw)
	}

	f.restart(t)
	row := overviewRow(t, f)
	if row["authorized"] != true {
		t.Fatalf("authorized after restart = %v", row["authorized"])
	}
	lt, _ := row["last_test"].(map[string]any)
	if lt == nil || lt["ok"] != true || lt["at"] == nil {
		t.Fatalf("last_test after restart: %v", row["last_test"])
	}
}

func TestLastSSHTestFailureCodeSurvivesRestart(t *testing.T) {
	f := newSSHAPIFixture(t)
	f.setup(t)
	// host key never pinned -> host_key_unpinned is the verdict.
	f.admin(http.MethodPost, f.nodePath("/ssh/test"), "")
	f.restart(t)
	row := overviewRow(t, f)
	lt, _ := row["last_test"].(map[string]any)
	if lt == nil || lt["code"] != "host_key_unpinned" {
		t.Fatalf("last_test = %v", row["last_test"])
	}
}

func TestLastSSHTestDroppedOnEditPinDeleteAndRotation(t *testing.T) {
	f := newSSHAPIFixture(t)
	f.setup(t)
	f.pin(t)
	test := func() {
		f.admin(http.MethodPost, f.nodePath("/ssh/test"), "")
		if overviewRow(t, f)["last_test"] == nil {
			t.Fatal("verdict not stored")
		}
	}
	gone := func(what string) {
		t.Helper()
		f.restart(t) // the drop must also be on disk
		if overviewRow(t, f)["last_test"] != nil {
			t.Fatalf("verdict survived %s", what)
		}
	}
	test()
	if r := f.admin(http.MethodPut, f.nodePath(""), `{"name":"lab1b"}`); r.Code != 200 {
		t.Fatalf("edit: %d", r.Code)
	}
	gone("edit")
	test()
	f.pin(t) // re-pin
	gone("re-pin")
	test()
	if r := f.admin(http.MethodPost, "/api/cluster/ssh/rotate", ""); r.Code != 201 {
		t.Fatalf("rotate: %d", r.Code)
	}
	if overviewRow(t, f)["last_test"] == nil {
		t.Fatal("starting a rotation must not drop the active-key verdict")
	}
	if r := f.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", `{"force":true}`); r.Code != 200 {
		t.Fatalf("commit: %d %s", r.Code, r.Body.String())
	}
	gone("rotation commit")
	test()
	if r := f.admin(http.MethodDelete, f.nodePath(""), ""); r.Code != 200 {
		t.Fatalf("delete: %d", r.Code)
	}
	f.restart(t)
	if got := f.h.sshTestState().get(f.node.ID); got != nil {
		t.Fatalf("verdict survived delete: %v", got)
	}
}

func TestLastSSHTestCorruptOrUnsafeFileIsIgnored(t *testing.T) {
	f := newSSHAPIFixture(t)
	f.setup(t)
	f.pin(t)
	path := filepath.Join(f.dir, "cluster-ssh", "last_test.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	if overviewRow(t, f)["last_test"] != nil {
		t.Fatal("corrupt file must read as empty")
	}
	good, _ := json.Marshal(map[string]nodes.SSHLastTest{f.node.ID: {OK: true, At: time.Now()}})
	if err := os.WriteFile(path, good, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	if overviewRow(t, f)["last_test"] != nil {
		t.Fatal("group/world-readable file must be ignored")
	}
}

func TestOverviewExposesPoolErrorCode(t *testing.T) {
	f := newSSHAPIFixture(t)
	f.setup(t)
	f.pin(t)
	f.connectErr = errors.New("ssh: handshake failed: Permission denied (publickey)")
	if _, err := f.h.libvirtPool.Connector(context.Background(), f.node); err == nil {
		t.Fatal("expected connect failure")
	}
	row := overviewRow(t, f)
	if row["pool_error"] != "ssh_auth_denied" {
		t.Fatalf("pool_error = %v", row["pool_error"])
	}
}
