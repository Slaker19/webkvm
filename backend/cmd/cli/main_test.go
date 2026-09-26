package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webkvm/internal/auth"
)

func TestParseFlags_BothFormats(t *testing.T) {
	args := []string{
		"--name", "vm1",
		"--ram=2048",
		"--disk=50",
		"--enabled",
		"extra-pos1",
		"extra-pos2",
	}

	flags, pos := parseFlags(args)
	if flags["name"] != "vm1" {
		t.Errorf("got name %q, want %q", flags["name"], "vm1")
	}
	if flags["ram"] != "2048" {
		t.Errorf("got ram %q, want %q", flags["ram"], "2048")
	}
	if flags["disk"] != "50" {
		t.Errorf("got disk %q, want %q", flags["disk"], "50")
	}
	if flags["enabled"] != "true" {
		t.Errorf("got enabled %q, want %q", flags["enabled"], "true")
	}
	if len(pos) != 2 || pos[0] != "extra-pos1" || pos[1] != "extra-pos2" {
		t.Errorf("got pos %+v, want [extra-pos1 extra-pos2]", pos)
	}
}

func TestParseFlags_BoolFlagBeforePositional(t *testing.T) {
	// Case 1: disks wipe --yes /dev/sdb
	flags, pos := parseFlags([]string{"--yes", "/dev/sdb"})
	if flags["yes"] != "true" {
		t.Errorf("got yes %q, want true", flags["yes"])
	}
	if len(pos) != 1 || pos[0] != "/dev/sdb" {
		t.Errorf("got pos %+v, want [/dev/sdb]", pos)
	}

	// Case 2: disks probe --deep /mnt/image.qcow2
	flags, pos = parseFlags([]string{"--deep", "/mnt/image.qcow2"})
	if flags["deep"] != "true" {
		t.Errorf("got deep %q, want true", flags["deep"])
	}
	if len(pos) != 1 || pos[0] != "/mnt/image.qcow2" {
		t.Errorf("got pos %+v, want [/mnt/image.qcow2]", pos)
	}

	// Case 3: disks init --nofail false /dev/sdb
	flags, pos = parseFlags([]string{"--nofail", "false", "/dev/sdb"})
	if flags["nofail"] != "false" {
		t.Errorf("got nofail %q, want false", flags["nofail"])
	}
	if len(pos) != 1 || pos[0] != "/dev/sdb" {
		t.Errorf("got pos %+v, want [/dev/sdb]", pos)
	}
}

func TestResolveToken_WithSessionEpochAndMCP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)

	jwtSecret := "super-secure-secret-key-32-chars-long!"
	if err := os.WriteFile(filepath.Join(dir, "jwt.key"), []byte(jwtSecret), 0600); err != nil {
		t.Fatal(err)
	}

	// Create users.json with admin session_epoch = 5
	users := []map[string]any{
		{
			"username":      "admin",
			"session_epoch": 5,
		},
	}
	uBytes, err := json.Marshal(users)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "users.json"), uBytes, 0600); err != nil {
		t.Fatal(err)
	}

	tokenStr := resolveToken("https://127.0.0.1:8080", "")
	if tokenStr == "" {
		t.Fatal("expected resolveToken to generate a local token, got empty string")
	}

	// Validate the minted token with the auth package
	authMgr := auth.NewManager(jwtSecret, nil)
	claims, err := authMgr.ValidateToken(tokenStr)
	if err != nil {
		t.Fatalf("ValidateToken failed on minted token: %v", err)
	}

	if claims.Username != "admin" {
		t.Errorf("got username %q, want admin", claims.Username)
	}
	if claims.Role != "admin" {
		t.Errorf("got role %q, want admin", claims.Role)
	}
	if claims.SessionEpoch != 5 {
		t.Errorf("got SessionEpoch %d, want 5 (must match admin's epoch in users.json)", claims.SessionEpoch)
	}
	if claims.MustChangePassword {
		t.Errorf("expected MustChangePassword to be false")
	}
}

func TestToInt(t *testing.T) {
	if toInt(nil) != 0 {
		t.Error("expected 0 for nil")
	}
	if toInt(float64(42)) != 42 {
		t.Error("expected 42 for float64")
	}
	if toInt(int64(100)) != 100 {
		t.Error("expected 100 for int64")
	}
	if toInt(int(5)) != 5 {
		t.Error("expected 5 for int")
	}
	if toInt("12345") != 12345 {
		t.Error("expected 12345 for string")
	}
	if toInt("invalid") != 0 {
		t.Error("expected 0 for invalid string")
	}
}

func TestTokenConfigPath(t *testing.T) {
	p := tokenConfigPath()
	if !strings.HasSuffix(p, filepath.Join(".config", "webkvm", "token")) {
		t.Errorf("unexpected token config path: %s", p)
	}
}

func TestNewClient(t *testing.T) {
	c := newClient("https://127.0.0.1:8080", "test-token", true)
	if c.server != "https://127.0.0.1:8080" {
		t.Errorf("got server %q, want https://127.0.0.1:8080", c.server)
	}
	if c.token != "test-token" {
		t.Errorf("got token %q, want test-token", c.token)
	}
	if !c.insecure {
		t.Errorf("expected insecure to be true")
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	data, _ := io.ReadAll(r)
	return string(data)
}

// TestRunImages_Cloud_ReportsCachedStatus is a regression test for a bug
// found during a live audit: runImages("cloud") read img["is_local"] /
// img["size"], but GET /api/images/cloud-base (image_hub.go
// CloudBaseImage) actually serializes is_cached / size_bytes. The
// mismatched keys always evaluated to zero-value ("No" / "-"), silently
// hiding the cached status and size of every image regardless of
// whether it was actually downloaded to disk.
func TestRunImages_Cloud_ReportsCachedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/images/cloud-base" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":"alpine-3.24","name":"Alpine Linux 3.24","is_cached":true,"size_bytes":183697408},
			{"id":"debian-13","name":"Debian 13","is_cached":false,"size_bytes":0}
		]`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "", false)
	out := captureStdout(t, func() {
		if err := runImages(c, []string{"images", "cloud"}); err != nil {
			t.Fatalf("runImages returned error: %v", err)
		}
	})

	if !strings.Contains(out, "alpine-3.24") || !strings.Contains(out, "Yes (Cached)") {
		t.Errorf("expected cached alpine-3.24 to show 'Yes (Cached)', got:\n%s", out)
	}
	if !strings.Contains(out, "175.2 MB") {
		t.Errorf("expected size 175.2 MB for the cached image, got:\n%s", out)
	}
	if !strings.Contains(out, "debian-13") {
		t.Errorf("expected debian-13 row present, got:\n%s", out)
	}
	// The uncached row must NOT accidentally read "Yes (Cached)".
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "debian-13") && strings.Contains(line, "Yes (Cached)") {
			t.Errorf("debian-13 (is_cached=false) incorrectly reported as cached: %q", line)
		}
	}
}

// TestRunVMs_Show_DiskAndNetworkFields is a regression test for a bug
// found during a live audit: runVMs("show") read dm["dev"] (models.DiskInfo
// has no such key — it's "target") and nm["network"] unconditionally
// (models.NetIface leaves Network empty for bridge NICs, the normal KVM
// case, putting the bridge name in Source instead). Both always printed
// <nil>/blank for real VMs regardless of the actual disk/network data.
func TestRunVMs_Show_DiskAndNetworkFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/vms/audit-poc-1" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"audit-poc-1","name":"audit-poc-1","type":"vm","state":"shutoff",
			"vcpus":1,"ram_mb":1024,"disk_gb":5,"autostart":false,
			"disks":[
				{"device":"disk","bus":"virtio","target":"vda","name":"audit-poc-1.qcow2","pool":"webkvm-disks","size_gb":5,"type":"file"},
				{"device":"cdrom","bus":"sata","target":"sda","name":"seed-audit-poc-1.iso","type":"file"}
			],
			"networks":[
				{"mac":"52:54:00:fb:97:1a","network":"","model":"virtio","type":"bridge","source":"vmbr0"}
			]
		}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "", false)
	out := captureStdout(t, func() {
		if err := runVMs(c, []string{"vms", "show", "audit-poc-1"}); err != nil {
			t.Fatalf("runVMs returned error: %v", err)
		}
	})

	if !strings.Contains(out, "vda") || !strings.Contains(out, "audit-poc-1.qcow2") {
		t.Errorf("expected disk target/name to be rendered, got:\n%s", out)
	}
	if strings.Contains(out, "<nil>") {
		t.Errorf("output must never leak Go's <nil> formatting, got:\n%s", out)
	}
	if !strings.Contains(out, "vmbr0") {
		t.Errorf("expected bridge source vmbr0 to be rendered for the network line, got:\n%s", out)
	}
}

// TestRunVMs_Create_NoStateInResponse is a regression test for a bug
// found during a live audit: POST /api/vms (CreateVM) only ever answers
// {id, name[, password, password_warning]} — there is no "state" key in
// the response. runVMs("create") unconditionally printed
// created["state"], so every single successful create — VM or container
// — reported "state: <nil>" instead of anything useful.
func TestRunVMs_Create_NoStateInResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"audit-poc-ct1","name":"audit-poc-ct1"}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "", false)
	out := captureStdout(t, func() {
		if err := runVMs(c, []string{"vms", "create", "--name", "audit-poc-ct1", "--type", "container"}); err != nil {
			t.Fatalf("runVMs returned error: %v", err)
		}
	})

	if strings.Contains(out, "<nil>") {
		t.Errorf("output must never leak Go's <nil> formatting, got:\n%s", out)
	}
	if !strings.Contains(out, "audit-poc-ct1") {
		t.Errorf("expected the created id to be reported, got:\n%s", out)
	}
}

// TestRunVMs_Delete_DisksFlag is a regression test for a gap found during
// a live audit: "vms delete" never sent ?disks=true, so it always left
// the VM's qcow2/cloud-init-seed ISO orphaned on the storage pool — the
// only way to reach full cleanup (matching the web UI's "also delete its
// disks" checkbox) was to call the API directly, bypassing the CLI.
func TestRunVMs_Delete_DisksFlag(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/vms/audit-poc-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"disks_deleted":["audit-poc-1.qcow2"],"disks_kept":[]}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "", false)
	out := captureStdout(t, func() {
		if err := runVMs(c, []string{"vms", "delete", "audit-poc-1", "--disks"}); err != nil {
			t.Fatalf("runVMs returned error: %v", err)
		}
	})

	if gotQuery != "disks=true" {
		t.Errorf("expected DELETE request with ?disks=true, got query %q", gotQuery)
	}
	if !strings.Contains(out, "disks deleted") || !strings.Contains(out, "audit-poc-1.qcow2") {
		t.Errorf("expected disks_deleted to be reported, got:\n%s", out)
	}
}

// TestRunVMs_Delete_WithoutDisksFlag confirms the historical behavior
// (disks kept by default) is unchanged when --disks is not passed.
func TestRunVMs_Delete_WithoutDisksFlag(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "", false)
	if err := runVMs(c, []string{"vms", "delete", "audit-poc-1"}); err != nil {
		t.Fatalf("runVMs returned error: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("expected no query string without --disks, got %q", gotQuery)
	}
}

// TestRunImages_Containers_ParsesEnvelope is a regression test for a bug
// found during a live audit: GET /api/vms/incus-images returns an
// envelope object ({images, host_arch, incus_enabled} —
// models.IncusImagesResponse), not a bare array. runImages("containers")
// used to unmarshal the response directly into []map[string]any, which
// silently failed (leaving the slice nil) and printed an empty table
// even when locally cached container images existed.
func TestRunImages_Containers_ParsesEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/vms/incus-images" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"images": [
				{"ref":"9fe03de4c231","label":"Debian 13 (Trixie)","is_local":true,"arch":"x86_64"},
				{"ref":"images:alpine/3.24","label":"Alpine Linux 3.24","is_local":false,"arch":"x86_64 / arm64"}
			],
			"host_arch": "x86_64",
			"incus_enabled": true
		}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "", false)
	out := captureStdout(t, func() {
		if err := runImages(c, []string{"images", "containers"}); err != nil {
			t.Fatalf("runImages returned error: %v", err)
		}
	})

	if !strings.Contains(out, "9fe03de4c231") || !strings.Contains(out, "Local Cache") {
		t.Errorf("expected locally cached image row, got:\n%s", out)
	}
	if !strings.Contains(out, "images:alpine/3.24") || !strings.Contains(out, "Remote") {
		t.Errorf("expected remote image row, got:\n%s", out)
	}
}
