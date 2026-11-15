package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"webkvm/internal/models"
	"webkvm/internal/sysutil"
)

func TestNodeDependenciesScriptSyntax(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-n")
	cmd.Stdin = strings.NewReader(nodeDependenciesScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v: %s", err, out)
	}
}

// The script is piped over `sh -s`, so a command that reads stdin would
// swallow the rest of the script and the install would silently stop.
// slowReader hands the script over in small chunks, like a network pipe, so
// the shell cannot slurp it in one read.
type slowReader struct {
	data []byte
	off  int
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), 16)], r.data[r.off:])
	r.off += n
	time.Sleep(time.Millisecond)
	return n, nil
}

func TestNodeDependenciesScriptSurvivesCommandsReadingStdin(t *testing.T) {
	dir := t.TempDir()
	drain := "#!/bin/sh\nwhile read -r _l; do :; done\nexit 0\n"
	for name, body := range map[string]string{
		"id":      "#!/bin/sh\nprintf '0\\n'\n",
		"apt-get": drain, "systemctl": drain, "virsh": drain, "usermod": drain, "getent": drain,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// bash reads a piped script byte by byte (as /bin/sh does on several
	// distros), which is where a stdin-consuming command bites.
	for _, shell := range []string{"/bin/sh", "/bin/bash"} {
		if _, err := os.Stat(shell); err != nil {
			continue
		}
		cmd := exec.Command(shell, "-s")
		cmd.Stdin = &slowReader{data: []byte(nodeDependenciesScript)}
		cmd.Env = []string{"PATH=" + dir, "WEBKVM_SSH_USER=root"}
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "node dependencies are installed for root") {
			t.Fatalf("%s: script did not run to completion over stdin: err=%v out=%q", shell, err, out)
		}
	}
}

func installRequest(ctx context.Context, body string) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/install-dependencies", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("X-Role", models.RoleAdmin)
	return httptest.NewRecorder(), req
}

func withInstallRunner(t *testing.T, mock *sysutil.MockCommandRunner) {
	t.Helper()
	prev := sysutil.DefaultRunner
	sysutil.DefaultRunner = mock
	t.Cleanup(func() { sysutil.DefaultRunner = prev })
}

func TestInstallNodeDependenciesValidationIs400WithRealMessage(t *testing.T) {
	var calls int32
	withInstallRunner(t, &sysutil.MockCommandRunner{
		InputHandler: func(context.Context, string, []byte, ...string) ([]byte, error) {
			atomic.AddInt32(&calls, 1)
			return nil, nil
		},
	})
	h := &Handler{}
	for body, want := range map[string]string{
		`{"uri":"qemu+tcp://h.example/system"}`:       "qemu+ssh",
		`{"uri":"qemu+ssh://u:pw@h.example/system"}`:  "passwords",
		`{"uri":"qemu+ssh://-bad@h.example/system"}`:  "invalid SSH username",
		`{"uri":"qemu+ssh://u@h.example/system?x=1"}`: "qemu+ssh",
	} {
		res, req := installRequest(context.Background(), body)
		h.InstallNodeDependencies(res, req)
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), want) {
			t.Errorf("%s: status=%d body=%s", body, res.Code, res.Body.String())
		}
	}
	if calls != 0 {
		t.Fatal("ssh must not run for an invalid URI")
	}
}

func TestInstallNodeDependenciesIgnoresClientDisconnectAndSerializesPerHost(t *testing.T) {
	started := make(chan context.Context, 2)
	release := make(chan struct{})
	withInstallRunner(t, &sysutil.MockCommandRunner{
		InputHandler: func(ctx context.Context, _ string, _ []byte, _ ...string) ([]byte, error) {
			started <- ctx
			<-release
			return []byte("done"), ctx.Err()
		},
	})
	h := &Handler{}
	body := `{"uri":"qemu+ssh://u@lockhost.example/system"}`

	clientCtx, disconnect := context.WithCancel(context.Background())
	res1, req1 := installRequest(clientCtx, body)
	done := make(chan struct{})
	go func() { h.InstallNodeDependencies(res1, req1); close(done) }()
	sshCtx := <-started

	disconnect()
	time.Sleep(50 * time.Millisecond)
	if sshCtx.Err() != nil {
		t.Fatal("client disconnect cancelled the running install")
	}
	if _, ok := sshCtx.Deadline(); !ok {
		t.Fatal("install context needs a cap")
	}

	res2, req2 := installRequest(context.Background(), body)
	h.InstallNodeDependencies(res2, req2)
	if res2.Code != http.StatusConflict {
		t.Fatalf("second install on the same host: %d %s", res2.Code, res2.Body.String())
	}
	// A different host is not blocked.
	res3, req3 := installRequest(context.Background(), `{"uri":"qemu+ssh://u@otherhost.example/system"}`)
	go func() { h.InstallNodeDependencies(res3, req3) }()
	<-started

	close(release)
	<-done
	// Lock released: the same host can install again.
	res4, req4 := installRequest(context.Background(), body)
	go func() { h.InstallNodeDependencies(res4, req4) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("host lock was not released")
	}
}
