package incus

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"webkvm/internal/compute"
	"webkvm/internal/ha"
)

// haInspector is the read-only snapshot of one instance that ha.EvalIncus
// needs. Everything is fetched once by HAInspector so the eligibility check
// itself never touches the daemon.
type haInspector struct {
	name    string
	driver  string
	devices []ha.DeviceInfo
	config  map[string]string
	online  map[string]bool
	nics    []ha.NICInfo
	// status/location are the live Incus status ("Running") and cluster
	// member of the instance, read when the inspector was built.
	status, location string
	// nets holds the per-network probe result, filled once by HAInspector.
	nets map[string]netProbe
}

// netProbe is the answer of networkMembers for one network name.
type netProbe struct {
	present map[string]bool
	managed bool
	err     error
}

var _ ha.IncusInspector = (*haInspector)(nil)

// RootPoolDriver returns the storage driver of the instance root pool, or ""
// when it could not be resolved.
func (i *haInspector) RootPoolDriver(inst string) string {
	if inst != i.name {
		return ""
	}
	return i.driver
}

// Devices lists the expanded devices (instance plus profiles).
func (i *haInspector) Devices(inst string) []ha.DeviceInfo {
	if inst != i.name {
		return nil
	}
	return append([]ha.DeviceInfo(nil), i.devices...)
}

// Config returns the expanded config (instance plus profiles).
func (i *haInspector) Config(inst string) map[string]string {
	out := map[string]string{}
	if inst != i.name {
		return out
	}
	for k, v := range i.config {
		out[k] = v
	}
	return out
}

// NICs lists the expanded NIC devices.
// InstanceState returns the live status and member of the instance.
func (i *haInspector) InstanceState(string) (string, string) {
	loc := i.location
	if loc == "none" { // standalone server: no cluster member
		loc = ""
	}
	return i.status, loc
}

func (i *haInspector) NICs(inst string) []ha.NICInfo {
	if inst != i.name {
		return nil
	}
	return append([]ha.NICInfo(nil), i.nics...)
}

// NetworkMembers returns the probe made when the inspector was built.
func (i *haInspector) NetworkMembers(name string) (map[string]bool, bool, error) {
	p, ok := i.nets[name]
	if !ok {
		return nil, false, fmt.Errorf("network %q was not probed", name)
	}
	return p.present, p.managed, p.err
}

// MemberOnline reports whether the named cluster member is Online. An
// unknown member is not online.
func (i *haInspector) MemberOnline(member string) bool { return i.online[member] }

// HAInspector reads the facts ha.EvalIncus needs for an instance in project
// ("" means default) and returns them with the names of every cluster member
// (empty when the daemon is not clustered). A missing instance wraps
// compute.ErrNotFound.
func (b *IncusBackend) HAInspector(_ context.Context, project, name string) (ha.IncusInspector, []string, error) {
	if b == nil || b.client == nil {
		return nil, nil, compute.ErrNotImplemented
	}
	client := b.client
	if project != "" && project != "default" {
		client = client.UseProject(project)
	}
	inst, _, err := client.GetInstance(name)
	if err != nil {
		if isNotFound(err) {
			return nil, nil, fmt.Errorf("%w: %v", compute.ErrNotFound, err)
		}
		return nil, nil, mapLXErr(err)
	}
	insp := &haInspector{name: name, config: map[string]string{}, online: map[string]bool{}, status: inst.Status, location: inst.Location}
	for k, v := range inst.ExpandedConfig {
		insp.config[k] = v
	}
	rootPool := ""
	for devName, dev := range inst.ExpandedDevices {
		info := ha.DeviceInfo{Name: devName, Type: dev["type"], Pool: dev["pool"], Source: dev["source"]}
		insp.devices = append(insp.devices, info)
		if dev["type"] == "disk" && dev["path"] == "/" {
			rootPool = dev["pool"]
		}
		if dev["type"] == "nic" {
			insp.nics = append(insp.nics, ha.NICInfo{Name: devName, Network: dev["network"], Parent: dev["parent"], Type: dev["nictype"]})
		}
	}
	sort.Slice(insp.nics, func(a, c int) bool { return insp.nics[a].Name < insp.nics[c].Name })
	sort.Slice(insp.devices, func(a, c int) bool { return insp.devices[a].Name < insp.devices[c].Name })
	if rootPool != "" {
		if pool, _, perr := b.client.GetStoragePool(rootPool); perr == nil && pool != nil {
			insp.driver = pool.Driver
		}
	}
	members, err := b.ClusterMembers()
	if err != nil && !errors.Is(err, compute.ErrNotImplemented) {
		return nil, nil, err
	}
	names := make([]string, 0, len(members))
	for _, m := range members {
		names = append(names, m.Name)
		insp.online[m.Name] = strings.EqualFold(m.Status, memberStatusOnline)
	}
	insp.probeNetworks(b, names)
	return insp, names, nil
}

// probeNetworks asks every member whether each NIC network/bridge exists.
func (i *haInspector) probeNetworks(b *IncusBackend, members []string) {
	i.nets = map[string]netProbe{}
	if len(members) < 2 {
		return
	}
	for _, nic := range i.nics {
		name := nic.Network
		if name == "" {
			name = nic.Parent
		}
		if name == "" {
			continue
		}
		if _, done := i.nets[name]; !done {
			i.nets[name] = b.networkMembers(name, members)
		}
	}
}

// networkMembers queries GET /1.0/networks/<name>?target=<member> on every
// member. The Incus networks API lists managed networks and also the host
// interfaces of that member (unmanaged bridges such as a Linux vmbr0), so
// one call covers both. 404 means missing; a managed network counts only
// when its status is Created. Any other error (member unreachable) leaves
// that member out of the answer: unknown is never reported as missing.
// Limit: the daemon only sees interfaces of its own network namespace, and
// an unmanaged name that exists but is not a bridge/physical type still
// counts as present.
func (b *IncusBackend) networkMembers(name string, members []string) netProbe {
	out := netProbe{present: map[string]bool{}}
	known := 0
	for _, m := range members {
		net, _, err := b.client.UseTarget(m).GetNetwork(name)
		switch {
		case err == nil && net != nil:
			known++
			if net.Managed {
				out.managed = true
				out.present[m] = net.Status == "" || net.Status == "Created"
			} else {
				out.present[m] = true
			}
		case err != nil && isNotFound(err):
			known++
			out.present[m] = false
		}
	}
	if known == 0 {
		out.err = fmt.Errorf("network %q could not be probed on any member", name)
	}
	return out
}

// DefaultProfileNICIssues checks the NICs of the default profile (project
// default) against the cluster members. It is cheap (one GET per member and
// NIC network) and read-only; an empty result means portable or unknown.
func (b *IncusBackend) DefaultProfileNICIssues(_ context.Context) ([]ha.Reason, error) {
	if b == nil || b.client == nil {
		return nil, compute.ErrNotImplemented
	}
	profile, _, err := b.client.GetProfile("default")
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, mapLXErr(err)
	}
	members, err := b.ClusterMembers()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(members))
	for _, m := range members {
		names = append(names, m.Name)
	}
	insp := &haInspector{name: "default"}
	for devName, dev := range profile.Devices {
		if dev["type"] == "nic" {
			insp.nics = append(insp.nics, ha.NICInfo{Name: devName, Network: dev["network"], Parent: dev["parent"], Type: dev["nictype"]})
		}
	}
	sort.Slice(insp.nics, func(a, c int) bool { return insp.nics[a].Name < insp.nics[c].Name })
	insp.probeNetworks(b, names)
	return ha.NICPortability(insp, "default", names), nil
}
