package api

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"webkvm/internal/compute"
	"webkvm/internal/ha"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
	"webkvm/internal/sysutil"
)

func init() {
	newWorkloadFacts = func(h *Handler) workloadFacts { return &backendWorkloadFacts{h: h} }
}

// libvirtDefinitionDir holds the persistent domain definitions
// (`<name>.xml`). Variable so tests do not touch /etc.
var libvirtDefinitionDir = "/etc/libvirt/qemu"

// errNotOnThisNode marks a VM that is not defined in this node's libvirt.
// v1 only takes local VMs as HA candidates.
var errNotOnThisNode = errors.New("vm_not_on_this_node")

// incusHAProvider is implemented by the Incus adapter (compute/incus).
type incusHAProvider interface {
	HAInspector(ctx context.Context, project, name string) (ha.IncusInspector, []string, error)
}

// secondaryProvider is implemented by compute.Combined.
type secondaryProvider interface {
	Secondary() compute.Backend
}

// kvmBackendOf returns the libvirt (primary) backend behind h.compute. The
// Combined wrapper routes by name and would answer from Incus when both
// hypervisors have an instance with that name.
func kvmBackendOf(b compute.Backend) compute.Backend {
	if c, ok := b.(*compute.Combined); ok {
		return c.Backend
	}
	return b
}

func incusProviderOf(b compute.Backend) (incusHAProvider, bool) {
	if sp, ok := b.(secondaryProvider); ok {
		if sec := sp.Secondary(); sec != nil {
			p, ok := sec.(incusHAProvider)
			return p, ok
		}
		return nil, false
	}
	p, ok := b.(incusHAProvider)
	return p, ok
}

// backendWorkloadFacts reads the facts from the compute backends. It never
// reads the local filesystem for a remote node: a handler cloned for a
// remote libvirt node answers errNotOnThisNode.
type backendWorkloadFacts struct{ h *Handler }

type domainXML struct {
	Devices struct {
		Disks []struct {
			Type   string `xml:"type,attr"`
			Device string `xml:"device,attr"`
			Source struct {
				File     string `xml:"file,attr"`
				Dev      string `xml:"dev,attr"`
				Name     string `xml:"name,attr"`
				Protocol string `xml:"protocol,attr"`
			} `xml:"source"`
		} `xml:"disk"`
		Interfaces []struct {
			Type   string `xml:"type,attr"`
			Source struct {
				Bridge  string `xml:"bridge,attr"`
				Network string `xml:"network,attr"`
			} `xml:"source"`
		} `xml:"interface"`
	} `xml:"devices"`
}

// parseDomainFacts extracts disk paths and the libvirt networks/bridges used
// by a domain XML. Disks without a local path (network disks) are reported as
// "<protocol>:<name>" so eligibility rejects them with a clear reason.
func parseDomainFacts(raw string) (disks, bridges, networks []string, err error) {
	var d domainXML
	if err := xml.Unmarshal([]byte(raw), &d); err != nil {
		return nil, nil, nil, err
	}
	seen := map[string]bool{}
	for _, disk := range d.Devices.Disks {
		p := disk.Source.File
		if p == "" {
			p = disk.Source.Dev
		}
		if p == "" && disk.Source.Name != "" {
			p = strings.TrimSuffix(disk.Source.Protocol+":"+disk.Source.Name, ":")
		}
		if p != "" && !seen[p] {
			seen[p] = true
			disks = append(disks, p)
		}
	}
	for _, nic := range d.Devices.Interfaces {
		switch {
		case nic.Type == "bridge" && nic.Source.Bridge != "":
			bridges = appendUnique(bridges, nic.Source.Bridge)
		case nic.Type == "network" && nic.Source.Network != "":
			networks = appendUnique(networks, nic.Source.Network)
		}
	}
	return disks, bridges, networks, nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func (f *backendWorkloadFacts) KVM(_ context.Context, name string) (kvmVMFacts, error) {
	if f.h.isRemote() {
		return kvmVMFacts{}, errNotOnThisNode
	}
	if !ha.WorkloadNameRE.MatchString(name) {
		return kvmVMFacts{}, nil
	}
	kvm := kvmBackendOf(f.h.compute)
	if kvm == nil {
		return kvmVMFacts{}, nil
	}
	vm, err := kvm.GetDomain(name)
	if err != nil {
		if errors.Is(err, compute.ErrNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return kvmVMFacts{}, nil
		}
		return kvmVMFacts{}, err
	}
	if vm.Hypervisor == "incus" {
		return kvmVMFacts{}, nil
	}
	out := kvmVMFacts{Exists: true, Running: vm.State == models.VMStateRunning || vm.State == models.VMStatePaused}
	raw, err := kvm.GetDomainXML(name)
	if err != nil {
		return kvmVMFacts{}, err
	}
	disks, bridges, networks, err := parseDomainFacts(raw)
	if err != nil {
		return kvmVMFacts{}, fmt.Errorf("parse domain XML: %w", err)
	}
	out.DiskPaths = disks
	out.Bridges = bridges
	if len(networks) > 0 {
		if nets, nerr := kvm.ListNetworks(); nerr == nil {
			byName := map[string]string{}
			for _, n := range nets {
				byName[n.Name] = n.Bridge
			}
			for _, n := range networks {
				if br := byName[n]; br != "" {
					out.Bridges = appendUnique(out.Bridges, br)
				}
			}
		}
	}
	// XML of record: the persistent definition. Following symlinks lets a
	// definition that lives on the replicated filesystem be recognised there.
	def := filepath.Join(libvirtDefinitionDir, name+".xml")
	if real, rerr := filepath.EvalSymlinks(def); rerr == nil {
		out.XMLPath = real
	}
	return out, nil
}

func (f *backendWorkloadFacts) Incus(ctx context.Context, project, name string) (ha.IncusInspector, []string, error) {
	p, ok := incusProviderOf(f.h.compute)
	if !ok {
		return nil, nil, compute.ErrNotImplemented
	}
	return p.HAInspector(ctx, project, name)
}

// NodeBridges reports the bridges of this node only: peers cannot be read
// without contacting them, so the cross-node bridge check covers the local
// node (see ha_workloads evaluate).
func (f *backendWorkloadFacts) NodeBridges(_ context.Context) (map[string][]string, error) {
	if f.h.isRemote() {
		return nil, errNotOnThisNode
	}
	nets, err := kvmBackendOf(f.h.compute).ListNetworks()
	if err != nil {
		return nil, err
	}
	var bridges []string
	for _, n := range nets {
		if n.Bridge != "" {
			bridges = appendUnique(bridges, n.Bridge)
		}
	}
	sort.Strings(bridges)
	return map[string][]string{localNodeName(): bridges}, nil
}

// --- Pacemaker ownership probe shared by the VM routes ---------------------

// haProbeTTL bounds how long one probe result is reused so a burst of
// requests (a listing plus lifecycle calls) costs a single pcs exec.
var haProbeTTL = 3 * time.Second

type haProbeResult struct {
	Workloads []ha.Workload
	// Fallback lists KVM domain names when the CIB could not be read but
	// Pacemaker is installed: they are treated as owned (fail closed).
	Fallback []ha.ManagedDomain
	// Nodes are the Pacemaker node names of the CIB (empty on fallback).
	Nodes []string
	At    time.Time
}

// empty reports whether nothing at all is owned by Pacemaker.
func (r haProbeResult) empty() bool { return len(r.Workloads) == 0 && len(r.Fallback) == 0 }

// isMember reports whether a registered node is a member of this Pacemaker
// cluster: its name, or the host of its URI, is a Pacemaker node name.
func (r haProbeResult) isMember(n nodes.Node) bool {
	host := ""
	if u, err := url.Parse(n.URI); err == nil {
		host = u.Hostname()
	}
	for _, name := range r.Nodes {
		if name == "" {
			continue
		}
		if strings.EqualFold(name, n.Name) || (host != "" && strings.EqualFold(name, host)) {
			return true
		}
	}
	return false
}

var haProbeCache struct {
	sync.Mutex
	runner sysutil.CommandRunner
	group  string
	res    haProbeResult
	valid  bool
}

func sameRunner(a, b sysutil.CommandRunner) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	return ta == tb && ta.Comparable() && a == b
}

// invalidateHAProbe drops the cached ownership view; write paths call it so
// the next request sees the change at once.
func invalidateHAProbe() {
	haProbeCache.Lock()
	haProbeCache.valid = false
	haProbeCache.Unlock()
}

// probeHAOwnership returns the Pacemaker view, cached for haProbeTTL. It
// reads the CIB through ha.ManagedWorkloads (KVM and Incus, legacy
// resources adopted) and, when the CIB is unreadable, falls back to the
// fail-closed transient-domain list.
func probeHAOwnership(ctx context.Context, cpGroup string) haProbeResult {
	runner := sysutil.DefaultRunner
	haProbeCache.Lock()
	if haProbeCache.valid && haProbeCache.group == cpGroup && sameRunner(haProbeCache.runner, runner) &&
		time.Since(haProbeCache.res.At) < haProbeTTL {
		res := haProbeCache.res
		haProbeCache.Unlock()
		return res
	}
	haProbeCache.Unlock()

	res := haProbeResult{At: time.Now()}
	list, members, err := ha.ManagedWorkloadsAndNodes(ctx, runner, cpGroup)
	if err != nil {
		res.Fallback = ha.ManagedVirtualDomainsDetailed(ctx, runner)
	} else {
		res.Workloads, res.Nodes = list, members
	}
	haProbeCache.Lock()
	haProbeCache.runner, haProbeCache.group, haProbeCache.res, haProbeCache.valid = runner, cpGroup, res, true
	haProbeCache.Unlock()
	return res
}

// maintenanceActive reports whether the workload is unmanaged inside an
// unexpired maintenance window (the safe-edit flow).
func maintenanceActive(w ha.Workload, now time.Time) bool {
	return !w.Managed && w.MaintUntil != "" && !w.MaintenanceExpired(now)
}

// haOwnerOf finds the managed workload for an instance. hv is "kvm", "incus"
// or "" (unknown: both kinds are considered). Incus instances are matched in
// the default project, the one the local daemon client works in. ambiguous is
// true when hv is unknown and both kinds claim the name.
func (r haProbeResult) ownerOf(name, hv string) (w ha.Workload, ok, ambiguous bool) {
	var kvm, incus *ha.Workload
	for i := range r.Workloads {
		wl := &r.Workloads[i]
		if wl.Name != name {
			continue
		}
		switch {
		case wl.Kind == ha.KindKVM && kvm == nil:
			kvm = wl
		case wl.Kind == ha.KindIncus && wl.Project == "default" && incus == nil:
			incus = wl
		}
	}
	switch hv {
	case ha.KindKVM:
		incus = nil
	case ha.KindIncus:
		kvm = nil
	}
	switch {
	case kvm != nil && incus != nil:
		return *kvm, true, true
	case kvm != nil:
		return *kvm, true, false
	case incus != nil:
		return *incus, true, false
	}
	if hv != ha.KindIncus {
		for _, d := range r.Fallback {
			if d.Name == name {
				return ha.Workload{Kind: ha.KindKVM, Name: name, Node: d.Node, Managed: true}, true, false
			}
		}
	}
	return ha.Workload{}, false, false
}

// requestHV returns the hypervisor selector of a /api/vms request: ?hv= or
// the dispatch context. "" means unspecified.
func requestHV(r *http.Request) string {
	hv := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hv")))
	if hv == "" {
		if d, ok := dispatchFrom(r); ok {
			hv = d.HV
		}
	}
	if hv == ha.KindKVM || hv == ha.KindIncus {
		return hv
	}
	return ""
}
