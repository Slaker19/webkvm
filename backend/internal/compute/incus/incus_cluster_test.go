package incus

import (
	"context"
	"errors"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

func TestMapClusterMembersKeepsStatusRolesAndNoCredentials(t *testing.T) {
	got := mapClusterMembers([]api.ClusterMember{
		{
			ServerName: "node-a", URL: "https://10.20.30.10:8443", Status: "Online",
			Message: "Fully operational", Database: true, Architecture: "x86_64",
			ClusterMemberPut: api.ClusterMemberPut{
				FailureDomain: "default",
				Roles:         []string{"database", "database-leader"},
			},
		},
		{
			ServerName: "node-b", URL: "https://10.20.30.11:8443", Status: "Offline",
			Message: "No heartbeat",
		},
	})
	if len(got) != 2 {
		t.Fatalf("expected two members, got %+v", got)
	}
	if got[0].Name != "node-a" || got[0].Status != "Online" || !got[0].Database {
		t.Fatalf("member fields not mapped: %+v", got[0])
	}
	if len(got[0].Roles) != 2 || got[0].Roles[1] != "database-leader" {
		t.Fatalf("roles not mapped: %+v", got[0])
	}
	if got[1].Roles == nil {
		t.Fatalf("empty roles must be an empty slice, got %+v", got[1])
	}
}

func TestClusterMembersWithoutClientIsEmpty(t *testing.T) {
	members, err := (&IncusBackend{}).ClusterMembers()
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Fatalf("expected no members, got %+v", members)
	}
}

func TestHealingFromConfig(t *testing.T) {
	cases := []struct {
		config   api.ConfigMap
		enabled  bool
		seconds  int64
		scenario string
	}{
		{api.ConfigMap{}, false, 0, "unset"},
		{api.ConfigMap{clusterHealingKey: "300"}, true, 300, "enabled"},
		{api.ConfigMap{clusterHealingKey: " 600 "}, true, 600, "padded"},
		{api.ConfigMap{clusterHealingKey: "0"}, false, 0, "zero"},
		{api.ConfigMap{clusterHealingKey: "bogus"}, false, 0, "invalid"},
		{api.ConfigMap{clusterHealingKey: "-5"}, false, 0, "negative"},
	}
	for _, tc := range cases {
		got := healingFromConfig(tc.config)
		if got.Enabled != tc.enabled || got.ThresholdSeconds != tc.seconds {
			t.Fatalf("%s: got %+v", tc.scenario, got)
		}
	}
}

func TestHealingConfigPreservesOtherKeys(t *testing.T) {
	base := api.ConfigMap{"core.https_address": ":8443", clusterHealingKey: "120"}
	enabled := healingConfig(base, 300)
	if enabled[clusterHealingKey] != "300" || enabled["core.https_address"] != ":8443" {
		t.Fatalf("enable did not preserve config: %+v", enabled)
	}
	if base[clusterHealingKey] != "120" {
		t.Fatalf("input config was mutated: %+v", base)
	}
	disabled := healingConfig(base, 0)
	if _, ok := disabled[clusterHealingKey]; ok {
		t.Fatalf("disable left the key in place: %+v", disabled)
	}
	if disabled["core.https_address"] != ":8443" {
		t.Fatalf("disable dropped unrelated config: %+v", disabled)
	}
}

// envClientStub fakes the Incus client for ClusterMemberEnvironments: it
// answers GetServer per target and can block one target to force a timeout.
type envClientStub struct {
	incus.InstanceServer
	clustered bool
	members   []api.ClusterMember
	listErr   error
	envs      map[string]api.ServerEnvironment
	errs      map[string]error
	block     map[string]chan struct{}
	target    string
}

func (c *envClientStub) IsClustered() bool { return c.clustered }

func (c *envClientStub) GetClusterMembers() ([]api.ClusterMember, error) {
	return c.members, c.listErr
}

func (c *envClientStub) UseTarget(name string) incus.InstanceServer {
	cp := *c
	cp.target = name
	return &cp
}

func (c *envClientStub) GetServer() (*api.Server, string, error) {
	if ch, ok := c.block[c.target]; ok {
		<-ch
	}
	if err := c.errs[c.target]; err != nil {
		return nil, "", err
	}
	return &api.Server{Environment: c.envs[c.target]}, "etag", nil
}

func ubuntuEnv(kernel string) api.ServerEnvironment {
	return api.ServerEnvironment{
		OSName: "Ubuntu", OSVersion: "26.04", KernelVersion: kernel,
		ServerVersion: "6.18", KernelArchitecture: "x86_64", Architectures: []string{"x86_64", "i686"},
	}
}

func TestClusterMemberEnvironmentsClustered(t *testing.T) {
	client := &envClientStub{
		clustered: true,
		members: []api.ClusterMember{
			{ServerName: "a", URL: "https://a:8443", Status: "Online"},
			{ServerName: "b", URL: "https://b:8443", Status: "Online"},
		},
		envs: map[string]api.ServerEnvironment{
			"a": ubuntuEnv("6.8.0-40-generic"),
			"b": {OSName: "Debian GNU/Linux", OSVersion: "13", KernelVersion: "6.12.0-1-amd64", ServerVersion: "6.17"},
		},
	}
	got, err := (&IncusBackend{client: client}).ClusterMemberEnvironments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two members, got %+v", got)
	}
	a, b := got[0], got[1]
	if a.Name != "a" || a.URL != "https://a:8443" || a.Status != "Online" || a.OSName != "Ubuntu" ||
		a.OSVersion != "26.04" || a.KernelVersion != "6.8.0-40-generic" || a.IncusVersion != "6.18" ||
		a.KernelArchitecture != "x86_64" || len(a.Architectures) != 2 || a.Error != "" {
		t.Fatalf("member a not mapped: %+v", a)
	}
	if b.Name != "b" || b.OSName != "Debian GNU/Linux" || b.IncusVersion != "6.17" {
		t.Fatalf("member b not mapped: %+v", b)
	}
}

func TestClusterMemberEnvironmentsNotClusteredReturnsLocal(t *testing.T) {
	env := ubuntuEnv("6.8.0-40-generic")
	env.ServerName = "solo"
	client := &envClientStub{envs: map[string]api.ServerEnvironment{"": env}}
	b := &IncusBackend{client: client}
	if b.IsClustered() {
		t.Fatal("stub is not clustered")
	}
	got, err := b.ClusterMemberEnvironments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "solo" || got[0].OSName != "Ubuntu" || got[0].Error != "" {
		t.Fatalf("expected one local entry, got %+v", got)
	}
}

func TestClusterMemberEnvironmentsMemberErrorDoesNotFailCall(t *testing.T) {
	client := &envClientStub{
		clustered: true,
		members: []api.ClusterMember{
			{ServerName: "a", Status: "Online"},
			{ServerName: "b", Status: "Offline"},
		},
		envs: map[string]api.ServerEnvironment{"a": ubuntuEnv("6.8.0")},
		errs: map[string]error{"b": errors.New("member unreachable")},
	}
	got, err := (&IncusBackend{client: client}).ClusterMemberEnvironments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Error != "" || got[1].Error != "member unreachable" || got[1].Status != "Offline" {
		t.Fatalf("per-member error not recorded: %+v", got)
	}
}

func TestClusterMemberEnvironmentsSlowMemberTimesOut(t *testing.T) {
	old := memberEnvironmentTimeout
	memberEnvironmentTimeout = 50 * time.Millisecond
	defer func() { memberEnvironmentTimeout = old }()
	release := make(chan struct{})
	defer close(release)
	client := &envClientStub{
		clustered: true,
		members:   []api.ClusterMember{{ServerName: "a"}, {ServerName: "slow"}},
		envs:      map[string]api.ServerEnvironment{"a": ubuntuEnv("6.8.0")},
		block:     map[string]chan struct{}{"slow": release},
	}
	start := time.Now()
	got, err := (&IncusBackend{client: client}).ClusterMemberEnvironments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("slow member was not bounded")
	}
	if len(got) != 2 || got[0].Error != "" || got[1].Name != "slow" || got[1].Error != "timeout" {
		t.Fatalf("slow member not marked timeout: %+v", got)
	}
}

func TestClusterMemberEnvironmentsListErrorFails(t *testing.T) {
	client := &envClientStub{clustered: true, listErr: errors.New("boom")}
	if _, err := (&IncusBackend{client: client}).ClusterMemberEnvironments(context.Background()); err == nil {
		t.Fatal("expected list error")
	}
}

func TestClusterMemberEnvironmentsWithoutClientIsEmpty(t *testing.T) {
	got, err := (&IncusBackend{}).ClusterMemberEnvironments(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("expected empty, got %+v %v", got, err)
	}
}
