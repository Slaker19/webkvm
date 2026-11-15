package incus

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/compute"
)

// ClusterMember is the read-only view of one Incus cluster member exposed by
// the Nodes page. It is the neutral compute.MemberInfo.
type ClusterMember = compute.MemberInfo

// clusterMembersLister is implemented by the concrete Incus client but is not
// part of the InstanceServer interface the backend stores.
type clusterMembersLister interface {
	GetClusterMembers() ([]api.ClusterMember, error)
}

// ClusterMembers returns the members of the local Incus cluster, or an empty
// slice when this daemon is not clustered.
func (b *IncusBackend) ClusterMembers() ([]ClusterMember, error) {
	if b == nil || b.client == nil {
		return []ClusterMember{}, nil
	}
	if !b.client.IsClustered() {
		return []ClusterMember{}, nil
	}
	lister, ok := b.client.(clusterMembersLister)
	if !ok {
		return nil, fmt.Errorf("incus client does not expose cluster members")
	}
	raw, err := lister.GetClusterMembers()
	if err != nil {
		return nil, err
	}
	return mapClusterMembers(raw), nil
}

// ClusterHealing is the read-only view of Incus native healing. A
// non-zero threshold means a member that stays offline is evacuated
// automatically after that many seconds.
type ClusterHealing struct {
	Enabled          bool  `json:"enabled"`
	ThresholdSeconds int64 `json:"threshold_seconds"`
}

const clusterHealingKey = "cluster.healing_threshold"

// ClusterHealing reads cluster.healing_threshold from the local daemon.
// It is cluster-wide, so a clustered daemon must be behind it.
func (b *IncusBackend) ClusterHealing() (ClusterHealing, error) {
	if b == nil || b.client == nil {
		return ClusterHealing{}, fmt.Errorf("incus client is not available")
	}
	if !b.client.IsClustered() {
		return ClusterHealing{}, fmt.Errorf("incus is not clustered")
	}
	server, _, err := b.client.GetServer()
	if err != nil {
		return ClusterHealing{}, err
	}
	return healingFromConfig(server.Config), nil
}

// SetClusterHealing writes cluster.healing_threshold, or removes it for
// seconds <= 0. The whole config map is sent back because the server PUT
// replaces it; dropping the other keys would reset unrelated settings.
func (b *IncusBackend) SetClusterHealing(seconds int64) (ClusterHealing, error) {
	if b == nil || b.client == nil {
		return ClusterHealing{}, fmt.Errorf("incus client is not available")
	}
	if !b.client.IsClustered() {
		return ClusterHealing{}, fmt.Errorf("incus is not clustered")
	}
	server, etag, err := b.client.GetServer()
	if err != nil {
		return ClusterHealing{}, err
	}
	config := healingConfig(server.Config, seconds)
	if err := b.client.UpdateServer(api.ServerPut{Config: config}, etag); err != nil {
		return ClusterHealing{}, err
	}
	return healingFromConfig(config), nil
}

func healingFromConfig(config api.ConfigMap) ClusterHealing {
	seconds, _ := strconv.ParseInt(strings.TrimSpace(config[clusterHealingKey]), 10, 64)
	if seconds < 0 {
		seconds = 0
	}
	return ClusterHealing{Enabled: seconds > 0, ThresholdSeconds: seconds}
}

func healingConfig(config api.ConfigMap, seconds int64) api.ConfigMap {
	updated := make(api.ConfigMap, len(config)+1)
	for key, value := range config {
		updated[key] = value
	}
	if seconds > 0 {
		updated[clusterHealingKey] = strconv.FormatInt(seconds, 10)
	} else {
		delete(updated, clusterHealingKey)
	}
	return updated
}

func mapClusterMembers(raw []api.ClusterMember) []ClusterMember {
	members := make([]ClusterMember, 0, len(raw))
	for _, member := range raw {
		roles := append([]string{}, member.Roles...)
		members = append(members, ClusterMember{
			Name:          member.ServerName,
			URL:           member.URL,
			Status:        member.Status,
			Message:       member.Message,
			Roles:         roles,
			Database:      member.Database,
			Architecture:  member.Architecture,
			FailureDomain: member.FailureDomain,
			Description:   member.Description,
		})
	}
	return members
}

// MemberEnvironment is the read-only software inventory of one cluster
// member (distro, kernel, Incus version) used to warn about mixed clusters.
// Error is set when that member could not be queried; the other fields are
// then empty.
type MemberEnvironment struct {
	Name               string   `json:"name"`
	URL                string   `json:"url"`
	Status             string   `json:"status"`
	OSName             string   `json:"os_name"`
	OSVersion          string   `json:"os_version"`
	KernelVersion      string   `json:"kernel_version"`
	KernelArchitecture string   `json:"kernel_architecture"`
	Architectures      []string `json:"architectures"`
	IncusVersion       string   `json:"incus_version"`
	Error              string   `json:"error"`
}

// memberEnvironmentTimeout bounds the whole fan-out: GetServer takes no
// context, so members still running when it expires are reported as
// "timeout". Package var so tests can shorten it.
var memberEnvironmentTimeout = 5 * time.Second

// clusterEnvironmentClient is the subset of the Incus client used by
// ClusterMemberEnvironments, kept small so tests can fake it.
type clusterEnvironmentClient interface {
	IsClustered() bool
	GetServer() (*api.Server, string, error)
	UseTarget(name string) incus.InstanceServer
}

// IsClustered reports whether the local Incus daemon is a cluster member.
func (b *IncusBackend) IsClustered() bool {
	return b != nil && b.client != nil && b.client.IsClustered()
}

// ClusterMemberEnvironments queries every cluster member concurrently for
// its server environment. A failing or slow member never fails the call: its
// Error field is set instead. When the daemon is not clustered a single entry
// describes the local server.
func (b *IncusBackend) ClusterMemberEnvironments(ctx context.Context) ([]MemberEnvironment, error) {
	if b == nil || b.client == nil {
		return []MemberEnvironment{}, nil
	}
	client, ok := b.client.(clusterEnvironmentClient)
	if !ok {
		return nil, fmt.Errorf("incus client does not expose server environments")
	}
	if !client.IsClustered() {
		server, _, err := client.GetServer()
		if err != nil {
			return nil, err
		}
		env := environmentFromServer(server)
		env.Name = server.Environment.ServerName
		return []MemberEnvironment{env}, nil
	}
	lister, ok := b.client.(clusterMembersLister)
	if !ok {
		return nil, fmt.Errorf("incus client does not expose cluster members")
	}
	raw, err := lister.GetClusterMembers()
	if err != nil {
		return nil, err
	}
	return collectMemberEnvironments(ctx, client, raw, memberEnvironmentTimeout), nil
}

func collectMemberEnvironments(ctx context.Context, client clusterEnvironmentClient, raw []api.ClusterMember, timeout time.Duration) []MemberEnvironment {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		index int
		env   MemberEnvironment
	}
	// Buffered so goroutines that outlive the deadline never block.
	results := make(chan result, len(raw))
	out := make([]MemberEnvironment, len(raw))
	done := make([]bool, len(raw))
	for i, member := range raw {
		out[i] = MemberEnvironment{Name: member.ServerName, URL: member.URL, Status: member.Status, Architectures: []string{}}
		go func(i int, name string) {
			server, _, err := client.UseTarget(name).GetServer()
			if err != nil {
				results <- result{i, MemberEnvironment{Error: err.Error()}}
				return
			}
			results <- result{i, environmentFromServer(server)}
		}(i, member.ServerName)
	}
	for pending := len(raw); pending > 0; pending-- {
		select {
		case r := <-results:
			r.env.Name, r.env.URL, r.env.Status = out[r.index].Name, out[r.index].URL, out[r.index].Status
			if r.env.Architectures == nil {
				r.env.Architectures = []string{}
			}
			out[r.index] = r.env
			done[r.index] = true
		case <-ctx.Done():
			for i := range out {
				if !done[i] {
					out[i].Error = "timeout"
				}
			}
			return out
		}
	}
	return out
}

func environmentFromServer(server *api.Server) MemberEnvironment {
	if server == nil {
		return MemberEnvironment{Architectures: []string{}, Error: "empty server response"}
	}
	env := server.Environment
	return MemberEnvironment{
		OSName:             env.OSName,
		OSVersion:          env.OSVersion,
		KernelVersion:      env.KernelVersion,
		KernelArchitecture: env.KernelArchitecture,
		Architectures:      append([]string{}, env.Architectures...),
		IncusVersion:       env.ServerVersion,
	}
}
