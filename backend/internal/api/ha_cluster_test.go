package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/auth"
	"webkvm/internal/config"
	"webkvm/internal/ha"
	"webkvm/internal/hainstall"
	"webkvm/internal/models"
	"webkvm/internal/sysutil"
)

type clusterFake struct {
	mu     sync.Mutex
	calls  []string
	nodes  string // <node .../> xml
	quorum string
	// stonith adds a fence_pve primitive to the CIB (device mode); history is
	// raw <fence_history> xml for the status.
	stonith bool
	history string
}

func (f *clusterFake) handler() func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.mu.Lock()
		f.calls = append(f.calls, name+" "+strings.Join(args, " "))
		f.mu.Unlock()
		switch name {
		case "dpkg-query":
			return []byte("install ok installed"), nil
		case "systemctl":
			return []byte("inactive\n"), errors.New("exit 3")
		case "lsmod":
			return []byte("Module\niTCO_wdt 1 0\n"), nil
		case "systemd-detect-virt":
			return []byte("none\n"), errors.New("exit 1")
		case "pcs":
			switch strings.Join(args, " ") {
			case "status xml":
				q := f.quorum
				if q == "" {
					q = "true"
				}
				return []byte(`<pacemaker-result><status code="0"/><crm_mon><summary><cluster_name>c</cluster_name><quorum>` + q + `</quorum></summary><nodes>` + f.nodes + `</nodes><resources><group id="webkvm-cp"><resource id="webkvm-ip" resource_agent="ocf:heartbeat:IPaddr2" role="Started" active="true"><node name="n1"/></resource></group></resources>` + f.history + `</crm_mon></pacemaker-result>`), nil
			case "cluster cib":
				res := ""
				if f.stonith {
					res = `<resources><primitive class="stonith" type="fence_pve" id="fence-pve"/></resources>`
				}
				return []byte(`<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-enabled" value="true"/><nvpair name="stonith-watchdog-timeout" value="10s"/></cluster_property_set></crm_config>` + res + `</configuration></cib>`), nil
			}
		}
		return nil, nil
	}
}

func (f *clusterFake) joined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "\n")
}

const threeNodes = `<node name="n1" online="true" type="member"/><node name="n2" online="true" type="member"/><node name="n3" online="true" type="member"/>`
const twoNodes = `<node name="n1" online="true" type="member"/><node name="n2" online="true" type="member"/>`

func clusterTestHandler(t *testing.T, f *clusterFake) (*Handler, string) {
	t.Helper()
	h, _, dir := haTestHandler(t)
	runner := &sysutil.MockCommandRunner{Handler: f.handler()}
	prevRunner, prevTweak := sysutil.DefaultRunner, clusterEnvTweak
	sysutil.DefaultRunner = runner
	t.Cleanup(func() { sysutil.DefaultRunner, clusterEnvTweak = prevRunner, prevTweak })
	h.cfg = &config.Config{DataDir: dir}
	h.haInstall = hainstall.New(dir, runner)
	hostDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(hostDir, "watchdog"), nil, 0o600)
	_ = os.WriteFile(filepath.Join(hostDir, "known-hosts"), []byte(`{"known_hosts":{"n1":{},"n2":{},"n3":{}}}`), 0o600)
	clusterEnvTweak = func(e ha.ClusterEnv) ha.ClusterEnv {
		e.CorosyncConf = filepath.Join(hostDir, "corosync.conf")
		e.CIBPath = filepath.Join(hostDir, "cib.xml")
		e.PcsKnownHosts = filepath.Join(hostDir, "known-hosts")
		e.WatchdogGlob = filepath.Join(hostDir, "watchdog*")
		e.SBDPaths = []string{filepath.Join(hostDir, "sbd")}
		e.Hostname = func() (string, error) { return "n1", nil }
		return e
	}
	return h, hostDir
}

func clusterMux(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/api/modules/ha/cluster", func(r chi.Router) {
		r.Use(h.requireHAPackages)
		r.Post("/plan", h.PlanHACluster)
		r.Post("/setup", h.SetupHACluster)
		r.Get("/status", h.GetHAClusterStatus)
	})
	r.Route("/api/ha", func(r chi.Router) {
		r.Use(h.requireClusterHA)
		r.Use(auth.RequireRole(models.RoleAdmin))
		r.Post("/fencing/test", h.TestHAFencing)
		r.Get("/fencing/evidence", h.GetHAFencingEvidence)
		r.Post("/nodes/{name}/standby", h.StandbyHANode)
		r.Post("/nodes/{name}/unstandby", h.UnstandbyHANode)
	})
	return r
}

const planBody = `{"cluster_name":"webkvm-ha","nodes":[{"name":"n1","address":"10.0.0.1"},{"name":"n2","address":"10.0.0.2"},{"name":"n3","address":"10.0.0.3"}]}`

func TestClusterRoutesRequirePackages(t *testing.T) {
	h, _, _ := haTestHandler(t) // empty mock runner: dpkg-query says nothing
	mux := clusterMux(h)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/modules/ha/cluster/plan"}, {"POST", "/api/modules/ha/cluster/setup"}, {"GET", "/api/modules/ha/cluster/status"},
	} {
		rec := doRequest(t, mux, c.method, c.path, planBody)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusConflict || body["code"] != "module_not_installed" {
			t.Fatalf("%s %s = %d %v", c.method, c.path, rec.Code, body)
		}
	}
}

func TestPlanEndpointShape(t *testing.T) {
	f := &clusterFake{nodes: threeNodes}
	h, _ := clusterTestHandler(t, f)
	rec := doRequest(t, clusterMux(h), "POST", "/api/modules/ha/cluster/plan", planBody)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var plan map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &plan)
	for _, k := range []string{"ok", "blockers", "warnings", "checks", "watchdog", "virt", "fencing_hint", "commands", "manual_steps", "nodes_needed"} {
		if _, ok := plan[k]; !ok {
			t.Fatalf("missing %s in %s", k, rec.Body.String())
		}
	}
	if plan["ok"] != true {
		t.Fatalf("plan not ok: %s", rec.Body.String())
	}
	if bad := doRequest(t, clusterMux(h), "POST", "/api/modules/ha/cluster/plan", "{nope"); bad.Code != 400 {
		t.Fatalf("bad body = %d", bad.Code)
	}
}

func TestSetupRefusalsAndRun(t *testing.T) {
	f := &clusterFake{nodes: threeNodes}
	h, hostDir := clusterTestHandler(t, f)
	mux := clusterMux(h)

	if rec := doRequest(t, mux, "POST", "/api/modules/ha/cluster/setup", planBody); rec.Code != 400 {
		t.Fatalf("no confirm = %d", rec.Code)
	}
	confirmed := strings.Replace(planBody, "{", `{"confirm":true,`, 1)

	_ = os.WriteFile(filepath.Join(hostDir, "corosync.conf"), []byte("x"), 0o600)
	rec := doRequest(t, mux, "POST", "/api/modules/ha/cluster/setup", confirmed)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 409 || body["code"] != "cluster_exists" {
		t.Fatalf("existing cluster: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(f.joined(), "cluster setup") {
		t.Fatal("pcs cluster setup must not run")
	}

	_ = os.Remove(filepath.Join(hostDir, "corosync.conf"))
	rec = doRequest(t, mux, "POST", "/api/modules/ha/cluster/setup", confirmed)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body.String())
	}
	h.clusterSetup().Wait()
	st := doRequest(t, mux, "GET", "/api/modules/ha/cluster/status", "")
	var status ha.SetupStatus
	_ = json.Unmarshal(st.Body.Bytes(), &status)
	if status.State != ha.SetupDone || len(status.Steps) != 6 {
		t.Fatalf("status %s", st.Body.String())
	}
	if !strings.Contains(f.joined(), "pcs cluster setup webkvm-ha n1 addr=10.0.0.1") {
		t.Fatalf("calls:\n%s", f.joined())
	}
}

func fencingFixture(t *testing.T, nodes string) (*clusterFake, *Handler, http.Handler, string) {
	return fencingFixtureWith(t, &clusterFake{nodes: nodes, stonith: true})
}

func fencingFixtureWith(t *testing.T, f *clusterFake) (*clusterFake, *Handler, http.Handler, string) {
	h, _ := clusterTestHandler(t, f)
	_, _, _ = h.settings.SetMany(map[string]any{"modules.cluster_ha": true})
	mux := clusterMux(h)
	return f, h, withAdmin(mux), h.cfg.DataDir
}

// withAdmin injects the identity headers RequireRole reads in this repo's tests.
func withAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.Header.Set("X-User", "root")
		req.Header.Set("X-Role", "admin")
		next.ServeHTTP(w, req)
	})
}

func TestFencingTestRefusalsAndSuccess(t *testing.T) {
	f, _, mux, dir := fencingFixture(t, threeNodes)
	post := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		rec := doRequest(t, mux, "POST", "/api/ha/fencing/test", body)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec, m
	}
	for _, tc := range []struct{ name, body, code string }{
		{"wrong confirm", `{"node":"n2","confirm":"fence n2"}`, "confirm_required"},
		{"no confirm", `{"node":"n2"}`, "confirm_required"},
		{"control plane", `{"node":"n1","confirm":"FENCE n1"}`, "target_is_control_plane"},
		{"unknown node", `{"node":"n9","confirm":"FENCE n9"}`, "node_unknown"},
	} {
		rec, m := post(tc.body)
		if rec.Code != 409 || m["code"] != tc.code || m["blockers"] == nil {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if rec, _ := post(`{"node":"n2;rm","confirm":"x"}`); rec.Code != 400 {
		t.Fatalf("injection = %d", rec.Code)
	}
	if strings.Contains(f.joined(), "stonith fence") {
		t.Fatal("refused requests must never fence")
	}
	if _, err := os.Stat(ha.FencingRecordPath(dir)); err == nil {
		t.Fatal("no record may exist yet")
	}

	rec, m := post(`{"node":"n2","confirm":"FENCE n2"}`)
	if rec.Code != 200 || m["ok"] != true || m["node"] != "n2" || m["fenced_at"] == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(f.joined(), "pcs stonith fence n2") {
		t.Fatalf("calls:\n%s", f.joined())
	}
	if _, ok := ha.LoadFencingRecord(dir, "c"); !ok {
		t.Fatal("success must be recorded")
	}
	// Control plane target allowed explicitly.
	if rec, _ := post(`{"node":"n1","confirm":"FENCE n1","allow_control_plane":true}`); rec.Code != 200 {
		t.Fatalf("allow_control_plane = %d %s", rec.Code, rec.Body.String())
	}
}

func TestFencingRefusedWithoutQuorumOrThreeNodes(t *testing.T) {
	_, _, mux, _ := fencingFixture(t, twoNodes)
	rec := doRequest(t, mux, "POST", "/api/ha/fencing/test", `{"node":"n2","confirm":"FENCE n2"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "not_enough_nodes") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	f := &clusterFake{nodes: threeNodes, quorum: "false"}
	h, _ := clusterTestHandler(t, f)
	_, _, _ = h.settings.SetMany(map[string]any{"modules.cluster_ha": true})
	rec = doRequest(t, withAdmin(clusterMux(h)), "POST", "/api/ha/fencing/test", `{"node":"n2","confirm":"FENCE n2"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "quorum_missing") || strings.Contains(f.joined(), "stonith fence") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestStandbyEndpoints(t *testing.T) {
	f, _, mux, _ := fencingFixture(t, threeNodes)
	rec := doRequest(t, mux, "POST", "/api/ha/nodes/n2/standby", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"standby"`) || !strings.Contains(f.joined(), "pcs node standby n2") {
		t.Fatalf("%d %s\n%s", rec.Code, rec.Body.String(), f.joined())
	}
	rec = doRequest(t, mux, "POST", "/api/ha/nodes/n2/unstandby", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"online"`) || !strings.Contains(f.joined(), "pcs node unstandby n2") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"-force", "n2%3Brm", "n9"} {
		rec = doRequest(t, mux, "POST", "/api/ha/nodes/"+bad+"/standby", "")
		if rec.Code != 400 && rec.Code != 404 {
			t.Fatalf("%s = %d", bad, rec.Code)
		}
	}
	if strings.Contains(f.joined(), "force") || strings.Contains(f.joined(), "n9") {
		t.Fatalf("unsafe argv:\n%s", f.joined())
	}

	f2, _, mux2, _ := fencingFixture(t, twoNodes)
	rec = doRequest(t, mux2, "POST", "/api/ha/nodes/n2/standby", "")
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if rec.Code != 409 || m["code"] != "would_lose_quorum" || strings.Contains(f2.joined(), "node standby") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestRolesAndModuleGatingOnHARoutes(t *testing.T) {
	h, _, _ := haTestHandler(t)
	mux := clusterMux(h)
	do := func(role string, path string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		if role != "" {
			req.Header.Set("X-User", "u")
			req.Header.Set("X-Role", role)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	// flag off: 404 module_not_installed before anything runs
	if got := do("admin", "/api/ha/fencing/test"); got != 404 {
		t.Fatalf("flag off = %d", got)
	}
	_, _, _ = h.settings.SetMany(map[string]any{"modules.cluster_ha": true})
	if got := do("", "/api/ha/nodes/n1/standby"); got != 401 && got != 403 {
		t.Fatalf("anonymous = %d", got)
	}
	if got := do("operator", "/api/ha/fencing/test"); got != 403 {
		t.Fatalf("operator = %d", got)
	}
}

// The wiring in router.go is replicated above (as the repo does for other
// groups); this canary fails if the real routes disappear or move out of the
// admin/gated groups.
func TestRouterSourceWiresClusterRoutes(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, want := range []string{
		`r.Route("/cluster"`, `r.Use(h.requireHAPackages)`, `r.Post("/plan", h.PlanHACluster)`, `r.Post("/setup", h.SetupHACluster)`,
		`r.Get("/status", h.GetHAClusterStatus)`, `r.Post("/fencing/test", h.TestHAFencing)`,
		`r.Post("/nodes/{name}/standby", h.StandbyHANode)`, `r.Post("/nodes/{name}/unstandby", h.UnstandbyHANode)`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("router.go missing %s", want)
		}
	}
	mod := text[strings.Index(text, `r.Route("/api/modules/ha"`):]
	mod = mod[:strings.Index(mod, `r.Route("/api/ha"`)]
	if !strings.Contains(mod, "auth.RequireRole(modelsRoleAdmin())") || strings.Contains(mod, "requireClusterHA") {
		t.Fatal("/api/modules/ha must be admin-only and not gated by cluster_ha")
	}
	api := text[strings.Index(text, `r.Route("/api/ha"`):]
	api = api[:strings.Index(api, "})")]
	if !strings.Contains(api, "h.requireClusterHA") || !strings.Contains(api, "RequireRole(modelsRoleAdmin())") || !strings.Contains(api, "/fencing/test") {
		t.Fatal("/api/ha group lost its gates")
	}
}

func TestReadinessExposesFencingTestedAt(t *testing.T) {
	f := &clusterFake{nodes: threeNodes, stonith: true}
	h, _ := clusterTestHandler(t, f)
	rec := httptest.NewRecorder()
	h.GetHAReadiness(rec, httptest.NewRequest("GET", "/api/ha/readiness", nil))
	var got map[string]map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if v, ok := got["fencing"]["tested_at"]; !ok || v != nil {
		t.Fatalf("tested_at must be present and null: %s", rec.Body.String())
	}
	if err := ha.SaveFencingRecord(h.cfg.DataDir, ha.FencingRecord{TestedAt: time.Now(), Node: "n2", ClusterName: "c"}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.GetHAReadiness(rec, httptest.NewRequest("GET", "/api/ha/readiness", nil))
	got = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["fencing"]["tested_at"] == nil || got["control_plane"]["fencing_tested"] != true {
		t.Fatalf("record not reflected: %s", rec.Body.String())
	}
}

func TestFencingWatchdogOnlyRefusesAndNeverRecordsProof(t *testing.T) {
	f, _, mux, dir := fencingFixtureWith(t, &clusterFake{nodes: threeNodes})
	post := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		rec := doRequest(t, mux, "POST", "/api/ha/fencing/test", body)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec, m
	}
	rec, m := post(`{"node":"n2","confirm":"FENCE n2"}`)
	sim, _ := m["simulate"].(map[string]any)
	if rec.Code != 409 || m["code"] != "watchdog_only_unreliable" || m["mode"] != "watchdog_only" ||
		m["explanation"] != "self_fence_assumed" || sim["node"] != "n2" || sim["command"] != ha.SimulateFailureCommand {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// A node that runs resources is refused even with the acknowledgement.
	rec, m = post(`{"node":"n1","confirm":"FENCE n1","allow_control_plane":true,"acknowledge_unreliable":true}`)
	if rec.Code != 409 || m["code"] != "watchdog_only_unreliable" || m["explanation"] != "node_has_resources" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(f.joined(), "stonith fence") {
		t.Fatal("refused requests must never fence")
	}
	// Acknowledged + idle node: runs, but is never recorded as proof.
	rec, m = post(`{"node":"n2","confirm":"FENCE n2","acknowledge_unreliable":true}`)
	if rec.Code != 200 || m["mode"] != "watchdog_only" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(ha.FencingRecordPath(dir)); err == nil {
		t.Fatal("watchdog_only result must never be recorded as proof")
	}
}

const evidenceHistory = `<fence_history><fence_event target="n2" action="reboot" delegate="n1" status="failed" completed="2026-11-12 09:00:00 +00:00"/><fence_event target="n2" action="reboot" delegate="n1" status="success" completed="2026-11-12 10:00:00 +00:00"/></fence_history>`

func TestFencingEvidenceEndpointAndReadinessFlip(t *testing.T) {
	f := &clusterFake{nodes: threeNodes}
	_, h, mux, dir := fencingFixtureWith(t, f)
	get := func() map[string]any {
		rec := doRequest(t, mux, "GET", "/api/ha/fencing/evidence", "")
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	readiness := func() map[string]map[string]any {
		rec := httptest.NewRecorder()
		h.GetHAReadiness(rec, httptest.NewRequest("GET", "/api/ha/readiness", nil))
		var got map[string]map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return got
	}
	m := get()
	if m["mode"] != "watchdog_only" || m["tested"] != false || m["tested_at"] != nil || m["recorded_by"] != nil {
		t.Fatalf("%v", m)
	}
	if s, _ := m["simulate"].(map[string]any); s["command_template"] != ha.SimulateFailureCommand {
		t.Fatalf("%v", m)
	}
	// The legacy record is not evidence in watchdog_only mode.
	_ = ha.SaveFencingRecord(dir, ha.FencingRecord{TestedAt: time.Now(), Node: "n2", ClusterName: "c"})
	if r := readiness(); r["control_plane"]["fencing_tested"] != false || r["fencing"]["mode"] != "watchdog_only" {
		t.Fatalf("record counted as proof: %v", r["fencing"])
	}
	f.mu.Lock()
	f.history = evidenceHistory
	f.mu.Unlock()
	m = get()
	events, _ := m["events"].([]any)
	if m["tested"] != true || m["recorded_by"] != "event" || m["tested_at"] != "2026-11-12T10:00:00Z" || len(events) != 2 {
		t.Fatalf("%v", m)
	}
	if first, _ := events[0].(map[string]any); first["status"] != "success" {
		t.Fatalf("most recent first: %v", events)
	}
	r := readiness()
	if r["control_plane"]["fencing_tested"] != true || r["fencing"]["tested_at"] != "2026-11-12T10:00:00Z" || r["fencing"]["evidence_count"] != float64(2) {
		t.Fatalf("%v", r["fencing"])
	}
}

func TestFencingEvidenceRouteIsGated(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	api := text[strings.Index(text, `r.Route("/api/ha"`):]
	api = api[:strings.Index(api, "})")]
	if !strings.Contains(api, `"/fencing/evidence"`) || !strings.Contains(api, "h.requireClusterHA") {
		t.Fatal("evidence route must live inside the gated /api/ha group")
	}
}

func TestFencingLedgerSurvivesClusterRestart(t *testing.T) {
	f := &clusterFake{nodes: threeNodes, history: evidenceHistory}
	_, h, mux, dir := fencingFixtureWith(t, f)
	get := func() map[string]any {
		rec := doRequest(t, mux, "GET", "/api/ha/fencing/evidence", "")
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	m := get()
	evs, _ := m["events"].([]any)
	if first, _ := evs[0].(map[string]any); m["tested"] != true || first["source"] != "history" {
		t.Fatalf("%v", m)
	}
	if fi, err := os.Stat(ha.FencingLedgerPath(dir)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("ledger not persisted 0600: %v %v", fi, err)
	}
	// Whole cluster restarted: Pacemaker's in-memory history is gone.
	f.mu.Lock()
	f.history = ""
	f.mu.Unlock()
	m = get()
	evs, _ = m["events"].([]any)
	if m["tested"] != true || m["recorded_by"] != "ledger" || m["tested_at"] != "2026-11-12T10:00:00Z" || len(evs) != 1 {
		t.Fatalf("%v", m)
	}
	if e, _ := evs[0].(map[string]any); e["source"] != "ledger" {
		t.Fatalf("%v", evs)
	}
	rec := httptest.NewRecorder()
	h.GetHAReadiness(rec, httptest.NewRequest("GET", "/api/ha/readiness", nil))
	var r map[string]map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	if r["control_plane"]["fencing_tested"] != true || r["fencing"]["evidence_count"] != float64(1) || r["fencing"]["tested_at"] != "2026-11-12T10:00:00Z" {
		t.Fatalf("%v", r["fencing"])
	}
}

func TestFencingLedgerNotFedByDirectFenceAPI(t *testing.T) {
	f, _, mux, dir := fencingFixtureWith(t, &clusterFake{nodes: threeNodes})
	rec := doRequest(t, mux, "POST", "/api/ha/fencing/test", `{"node":"n2","confirm":"FENCE n2","acknowledge_unreliable":true}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	_ = f
	if _, err := os.Stat(ha.FencingLedgerPath(dir)); err == nil {
		t.Fatal("direct fence in watchdog_only must not feed the ledger")
	}
	if m := doRequest(t, mux, "GET", "/api/ha/fencing/evidence", ""); !strings.Contains(m.Body.String(), `"tested":false`) {
		t.Fatal(m.Body.String())
	}
}
