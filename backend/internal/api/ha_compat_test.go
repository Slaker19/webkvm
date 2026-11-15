package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/auth"
	"webkvm/internal/compute/incus"
	"webkvm/internal/config"
)

func compatMember(name, osName, osVersion, kernel, incusVersion string) haCompatMember {
	return haCompatMember{Name: name, OSName: osName, OSVersion: osVersion, KernelVersion: kernel, IncusVersion: incusVersion}
}

func TestHACompatWarnings(t *testing.T) {
	cases := []struct {
		name        string
		members     []haCompatMember
		want        []string
		recommended bool
	}{
		{"no members", nil, []string{}, false},
		{"single member", []haCompatMember{compatMember("a", "Ubuntu", "26.04", "6.8.0", "6.18")}, []string{}, false},
		{"all same", []haCompatMember{
			compatMember("a", "Ubuntu", "26.04", "6.8.0-40-generic", "6.18"),
			compatMember("b", "ubuntu", "26.04", "6.8.0-41-generic", "6.18"),
		}, []string{}, true},
		{"mixed distro", []haCompatMember{
			compatMember("a", "Ubuntu", "26.04", "6.8.0-40-generic", "6.18"),
			compatMember("b", "Debian GNU/Linux", "13", "6.8.0-1-amd64", "6.18"),
		}, []string{"mixed_distro"}, false},
		{"mixed distro version", []haCompatMember{
			compatMember("a", "Ubuntu", "26.04", "6.8.0-40-generic", "6.18"),
			compatMember("b", "Ubuntu", "24.04", "6.8.0-40-generic", "6.18"),
		}, []string{"mixed_distro_version"}, false},
		{"mixed incus", []haCompatMember{
			compatMember("a", "Ubuntu", "26.04", "6.8.0-40-generic", "6.18"),
			compatMember("b", "Ubuntu", "26.04", "6.8.0-40-generic", "6.17"),
		}, []string{"mixed_incus_version"}, false},
		{"mixed kernel major", []haCompatMember{
			compatMember("a", "Ubuntu", "26.04", "6.8.0-40-generic", "6.18"),
			compatMember("b", "Ubuntu", "26.04", "6.12.3", "6.18"),
		}, []string{"mixed_kernel_major"}, false},
		{"unknown member", []haCompatMember{
			compatMember("a", "Ubuntu", "26.04", "6.8.0-40-generic", "6.18"),
			{Name: "b", Error: "timeout"},
			{Name: "c"},
		}, []string{"members_unknown"}, false},
		{"everything mixed sorted", []haCompatMember{
			compatMember("a", "Ubuntu", "26.04", "6.8.0", "6.18"),
			compatMember("b", "Ubuntu", "24.04", "6.12.0", "6.17"),
			compatMember("c", "Debian GNU/Linux", "13", "6.12.0", "6.18"),
			{Name: "d", Error: "boom"},
		}, []string{"members_unknown", "mixed_distro", "mixed_distro_version", "mixed_incus_version", "mixed_kernel_major"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, recommended := haCompatWarnings(tc.members)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("warnings = %#v, want %#v", got, tc.want)
			}
			if recommended != tc.recommended {
				t.Fatalf("recommended = %v, want %v", recommended, tc.recommended)
			}
		})
	}
}

func TestKernelMajorMinor(t *testing.T) {
	for in, want := range map[string]string{
		"6.8.0-40-generic": "6.8", "6.12": "6.12", "7.0.14-28-pve": "7.0", "6": "6", "": "",
	} {
		if got := kernelMajorMinor(in); got != want {
			t.Fatalf("kernelMajorMinor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseOSRelease(t *testing.T) {
	got := parseOSRelease(strings.NewReader("# comment\nNAME=\"Ubuntu\"\nVERSION_ID='26.04'\nID=ubuntu\n\nbogus\n"))
	if got["NAME"] != "Ubuntu" || got["VERSION_ID"] != "26.04" || got["ID"] != "ubuntu" {
		t.Fatalf("unexpected os-release map: %+v", got)
	}
}

type fakeEnvSource struct {
	clustered bool
	members   []incus.MemberEnvironment
	err       error
}

func (f fakeEnvSource) IsClustered() bool { return f.clustered }

func (f fakeEnvSource) ClusterMemberEnvironments(context.Context) ([]incus.MemberEnvironment, error) {
	return f.members, f.err
}

// stubHACompatHost points the local probes at test data and reports whether
// the handler tried to open Incus. Restored on cleanup.
func stubHACompatHost(t *testing.T, src haCompatEnvSource, openErr error) *bool {
	t.Helper()
	dir := t.TempDir()
	osr := filepath.Join(dir, "os-release")
	if err := os.WriteFile(osr, []byte("NAME=\"Ubuntu\"\nVERSION_ID=\"26.04\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldHost, oldKernel, oldOpen := haCompatOSReleasePath, haCompatHostname, haCompatKernel, haCompatOpenIncus
	haCompatOSReleasePath = osr
	haCompatHostname = func() (string, error) { return "node-a", nil }
	haCompatKernel = func() string { return "6.8.0-40-generic" }
	opened := new(bool)
	haCompatOpenIncus = func(*Handler) (haCompatEnvSource, func(), error) {
		*opened = true
		if openErr != nil {
			return nil, func() {}, openErr
		}
		return src, func() {}, nil
	}
	t.Cleanup(func() {
		haCompatOSReleasePath, haCompatHostname, haCompatKernel, haCompatOpenIncus = oldPath, oldHost, oldKernel, oldOpen
	})
	return opened
}

func getHACompat(t *testing.T, h *Handler) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.GetHACompat(rec, httptest.NewRequest(http.MethodGet, "/api/modules/ha/compat", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestGetHACompatIncusDisabled(t *testing.T) {
	opened := stubHACompatHost(t, nil, errors.New("must not open Incus when disabled"))
	for _, h := range []*Handler{{}, {cfg: &config.Config{IncusEnabled: false}}} {
		code, body := getHACompat(t, h)
		if *opened {
			t.Fatal("Incus must not be opened when disabled")
		}
		if code != http.StatusOK {
			t.Fatalf("status = %d body=%v", code, body)
		}
		if body["incus_available"] != false || body["clustered"] != false || body["recommended"] != false {
			t.Fatalf("unexpected flags: %v", body)
		}
		if members, ok := body["members"].([]any); !ok || len(members) != 0 {
			t.Fatalf("members must be an empty array: %v", body["members"])
		}
		if warnings, ok := body["warnings"].([]any); !ok || len(warnings) != 0 {
			t.Fatalf("warnings must be an empty array: %v", body["warnings"])
		}
		local := body["local"].(map[string]any)
		if local["hostname"] != "node-a" || local["os_name"] != "Ubuntu" || local["os_version"] != "26.04" ||
			local["kernel_version"] != "6.8.0-40-generic" || local["incus_version"] != "" {
			t.Fatalf("unexpected local: %v", local)
		}
	}
}

func TestGetHACompatIncusUnavailable(t *testing.T) {
	stubHACompatHost(t, nil, errors.New("no socket"))
	code, body := getHACompat(t, &Handler{cfg: &config.Config{IncusEnabled: true}})
	if code != http.StatusOK || body["incus_available"] != false {
		t.Fatalf("status=%d body=%v", code, body)
	}
}

func TestGetHACompatMixedCluster(t *testing.T) {
	stubHACompatHost(t, fakeEnvSource{clustered: true, members: []incus.MemberEnvironment{
		{Name: "node-a", URL: "https://a:8443", Status: "Online", OSName: "Ubuntu", OSVersion: "26.04", KernelVersion: "6.8.0-40-generic", IncusVersion: "6.18"},
		{Name: "node-b", URL: "https://b:8443", Status: "Online", OSName: "Debian GNU/Linux", OSVersion: "13", KernelVersion: "6.12.0-1-amd64", IncusVersion: "6.18"},
		{Name: "node-c", URL: "https://c:8443", Status: "Offline", Error: "timeout"},
	}}, nil)
	code, body := getHACompat(t, &Handler{cfg: &config.Config{IncusEnabled: true}})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body["incus_available"] != true || body["clustered"] != true || body["recommended"] != false {
		t.Fatalf("unexpected flags: %v", body)
	}
	want := []any{"members_unknown", "mixed_distro", "mixed_kernel_major"}
	if !reflect.DeepEqual(body["warnings"], want) {
		t.Fatalf("warnings = %v, want %v", body["warnings"], want)
	}
	members := body["members"].([]any)
	if len(members) != 3 {
		t.Fatalf("members = %v", members)
	}
	c := members[2].(map[string]any)
	if c["name"] != "node-c" || c["status"] != "Offline" || c["error"] != "unreachable" || c["url"] != "https://c:8443" {
		t.Fatalf("member c = %v", c)
	}
	if local := body["local"].(map[string]any); local["incus_version"] != "6.18" {
		t.Fatalf("local incus_version should come from the matching member: %v", local)
	}
}

func TestGetHACompatMemberListErrorIsNot5xx(t *testing.T) {
	stubHACompatHost(t, fakeEnvSource{clustered: true, err: errors.New("cluster db down")}, nil)
	code, body := getHACompat(t, &Handler{cfg: &config.Config{IncusEnabled: true}})
	if code != http.StatusOK || body["incus_available"] != true {
		t.Fatalf("status=%d body=%v", code, body)
	}
	if !reflect.DeepEqual(body["warnings"], []any{"members_unknown"}) {
		t.Fatalf("warnings = %v", body["warnings"])
	}
}

func TestHACompatRouteIsAdminOnly(t *testing.T) {
	stubHACompatHost(t, nil, errors.New("disabled"))
	h := &Handler{}
	r := chi.NewRouter()
	r.Route("/api/modules/ha", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/compat", h.GetHACompat)
	})
	for role, want := range map[string]int{"viewer": http.StatusForbidden, "operator": http.StatusForbidden, "admin": http.StatusOK} {
		req := httptest.NewRequest(http.MethodGet, "/api/modules/ha/compat", nil)
		req.Header.Set("X-User", "u")
		req.Header.Set("X-Role", role)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("role %s: status %d, want %d", role, rec.Code, want)
		}
	}
}

// TestRouterRegistersHACompatInAdminModuleGroup checks the real router.go:
// the compat route must live in the admin-gated /api/modules/ha block and
// not behind requireClusterHA (it is needed before enabling the module).
func TestRouterRegistersHACompatInAdminModuleGroup(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, `r.Route("/api/modules/ha", func(r chi.Router) {`)
	if start < 0 {
		t.Fatal("/api/modules/ha group not found")
	}
	end := strings.Index(text[start:], "\n\t})")
	if end < 0 {
		t.Fatal("end of /api/modules/ha group not found")
	}
	block := text[start : start+end]
	if !strings.Contains(block, `r.Get("/compat", h.GetHACompat)`) {
		t.Fatal("GET /compat not registered in /api/modules/ha")
	}
	if !strings.Contains(block, "auth.RequireRole(modelsRoleAdmin())") {
		t.Fatal("/api/modules/ha group is not admin-gated")
	}
	if strings.Contains(block, "requireClusterHA") {
		t.Fatal("/api/modules/ha must not be gated by modules.cluster_ha")
	}
}

func TestGetHACompatDoesNotExposeRawMemberErrors(t *testing.T) {
	stubHACompatHost(t, fakeEnvSource{clustered: true, members: []incus.MemberEnvironment{
		{Name: "a", OSName: "Ubuntu", OSVersion: "26.04", KernelVersion: "6.8.0", IncusVersion: "6.18"},
		{Name: "b", Error: `Get "https://10.9.8.7:8443/1.0": x509: certificate signed by unknown authority`},
	}}, nil)
	h := &Handler{cfg: &config.Config{IncusEnabled: true}}
	rec := httptest.NewRecorder()
	h.GetHACompat(rec, httptest.NewRequest(http.MethodGet, "/api/modules/ha/compat", nil))
	if strings.Contains(rec.Body.String(), "10.9.8.7") || strings.Contains(rec.Body.String(), "x509") {
		t.Fatalf("raw client error leaked: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"unreachable"`) || !strings.Contains(rec.Body.String(), `"recommended":false`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
