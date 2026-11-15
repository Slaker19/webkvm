package ha

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/sysutil"
)

func basePVEConfig(endpoint string) FencePVEConfig {
	return FencePVEConfig{
		Enabled:     true,
		Endpoint:    endpoint,
		TokenID:     "root@pam!webkvm",
		TokenSecret: "supersecret",
		PVENode:     "pve1",
		HostMap:     "webkvm-1:100,webkvm-2:101",
	}
}

func TestIsHypervisorFenceAgent(t *testing.T) {
	for _, agent := range []string{"fence_pve", "fence_ipmilan", "fence_ilo4", "fence_redfish"} {
		if !IsHypervisorFenceAgent(agent) {
			t.Fatalf("%s must count as hypervisor/hardware fencing", agent)
		}
	}
	for _, agent := range []string{"fence_sbd", "fence_mpath", ""} {
		if IsHypervisorFenceAgent(agent) {
			t.Fatalf("%q must not count as hypervisor fencing", agent)
		}
	}
}

func TestParseConfigDetectsFenceAgents(t *testing.T) {
	cib := []byte(`<cib><configuration><resources>
		<primitive id="fence-a" class="stonith" type="fence_pve"/>
		<primitive id="fence-b" class="stonith" type="fence_ipmilan"/>
	</resources></configuration></cib>`)
	config, err := parsePacemakerConfig(cib)
	if err != nil {
		t.Fatal(err)
	}
	if config.FenceDevices != 2 || len(config.FenceAgents) != 2 {
		t.Fatalf("agents = %v, devices = %d", config.FenceAgents, config.FenceDevices)
	}
	if !HasHypervisorFence(config.FenceAgents) {
		t.Fatal("fence_pve must be detected as hypervisor fencing")
	}
	if HasHypervisorFence([]string{"fence_sbd"}) {
		t.Fatal("fence_sbd alone is not hypervisor fencing")
	}
}

func TestValidateFencePVEConfig(t *testing.T) {
	good := basePVEConfig("https://10.10.10.1:8006")
	if err := ValidateFencePVEConfig(good); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := map[string]func(*FencePVEConfig){
		"http endpoint":  func(c *FencePVEConfig) { c.Endpoint = "http://10.10.10.1:8006" },
		"no token id":    func(c *FencePVEConfig) { c.TokenID = "root@pam" },
		"empty secret":   func(c *FencePVEConfig) { c.TokenSecret = " " },
		"bad pve node":   func(c *FencePVEConfig) { c.PVENode = "pve1;rm" },
		"bad host map":   func(c *FencePVEConfig) { c.HostMap = "webkvm-1=100" },
		"empty endpoint": func(c *FencePVEConfig) { c.Endpoint = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := good
			mutate(&cfg)
			if err := ValidateFencePVEConfig(cfg); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

func TestFencePVECommandsNeverContainSecret(t *testing.T) {
	cfg := basePVEConfig("https://10.10.10.1:8006")
	commands, notes, err := FencePVECommands(cfg, []string{"webkvm-1", "webkvm-2"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{
		"pcs property set stonith-enabled=true",
		"pcs stonith create fence-pve fence_pve",
		"pve_node='pve1'",
		"token_id='root@pam!webkvm'",
		"pcmk_host_map='webkvm-1:100,webkvm-2:101'",
		"ip='10.10.10.1'",
		"ipport='8006'",
		"pcs stonith config fence-pve",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("commands missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "ip='10.10.10.1:8006'") || strings.Contains(joined, "stonith show") || strings.Contains(joined, "--full") {
		t.Fatalf("M13: ip must not carry the port and pcs stonith show is gone:\n%s", joined)
	}
	if strings.Contains(joined, "supersecret") {
		t.Fatalf("commands leaked the token secret:\n%s", joined)
	}
	if len(notes) == 0 {
		t.Fatal("expected operator notes")
	}
}

func TestFencePVECommandsBuildsHostMapPlaceholders(t *testing.T) {
	cfg := basePVEConfig("https://10.10.10.1:8006")
	cfg.HostMap = ""
	commands, _, err := FencePVECommands(cfg, []string{"webkvm-1", "webkvm-2"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "webkvm-1:<VMID_webkvm-1>") || !strings.Contains(joined, "webkvm-2:<VMID_webkvm-2>") {
		t.Fatalf("placeholder host map missing:\n%s", joined)
	}
}

func TestCheckPVEToken(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/access/permissions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "PVEAPIToken=root@pam!webkvm=supersecret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"/vms/100":{"VM.PowerMgmt":1,"VM.Audit":1}}}`)
	}))
	defer server.Close()

	cfg := basePVEConfig(server.URL)
	cfg.InsecureTLS = true
	check, err := CheckPVEToken(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Valid || !check.PowerMgmt || check.Message != "" {
		t.Fatalf("check = %+v, want valid with VM.PowerMgmt", check)
	}

	cfg.TokenSecret = "wrong"
	check, err = CheckPVEToken(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if check.Valid || check.PowerMgmt {
		t.Fatalf("rejected token reported as valid: %+v", check)
	}
	if !strings.Contains(check.Message, "401") {
		t.Fatalf("message = %q, want the HTTP status", check.Message)
	}
}

func TestCheckPVETokenReportsMissingPermission(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"/":{"VM.Audit":1}}}`)
	}))
	defer server.Close()

	cfg := basePVEConfig(server.URL)
	cfg.InsecureTLS = true
	check, err := CheckPVEToken(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Valid || check.PowerMgmt {
		t.Fatalf("check = %+v, want valid without VM.PowerMgmt", check)
	}
	if check.Message == "" {
		t.Fatal("missing permission must be explained")
	}
}

func TestCheckPVETokenRejectsInvalidConfigBeforeDialing(t *testing.T) {
	cfg := basePVEConfig("http://10.10.10.1:8006")
	if _, err := CheckPVEToken(context.Background(), cfg); err == nil {
		t.Fatal("http endpoint must be rejected before any request")
	}
}

func TestProbeFencePVEAgent(t *testing.T) {
	runner := &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "pcs" || strings.Join(args, " ") != "stonith list" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return []byte("fence_pve\nfence_ipmilan\n"), nil
	}}
	if !ProbeFencePVEAgent(context.Background(), runner) {
		t.Fatal("fence_pve must be detected in pcs stonith list")
	}
	empty := &sysutil.MockCommandRunner{Handler: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("fence_ipmilan\n"), nil
	}}
	if ProbeFencePVEAgent(context.Background(), empty) {
		t.Fatal("fence_pve reported present when it is not listed")
	}
}

func TestPacemakerOwnsWebKVM(t *testing.T) {
	status := PacemakerStatus{Resources: []PacemakerResource{{ID: "webkvm-vip", Group: "webkvm-cp", Agent: "ocf:heartbeat:IPaddr2"}}}
	if !PacemakerOwnsWebKVM(status) {
		t.Fatal("webkvm-cp must be recognized as a WebKVM resource")
	}
	if PacemakerOwnsWebKVM(PacemakerStatus{Resources: []PacemakerResource{{ID: "kvm-ha"}}}) {
		t.Fatal("an unrelated resource must not match")
	}
	if PacemakerOwnsWebKVM(PacemakerStatus{Resources: []PacemakerResource{{ID: "webkvm-x", Group: "ha-kvm-webkvm-x-grp"}}}) {
		t.Fatal("a VM named webkvm-x must not be mistaken for the control plane")
	}
}

func TestFencePVECommandsCustomPortAndHostname(t *testing.T) {
	cfg := basePVEConfig("https://pve.example.org:8443")
	commands, _, err := FencePVECommands(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "ip='pve.example.org' ipport='8443'") {
		t.Fatalf("custom port/host not split:\n%s", joined)
	}
}
