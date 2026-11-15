package ha

import (
	"context"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"webkvm/internal/sysutil"
)

// Eligibility reason codes.
const (
	ReasonDiskNotReplicated = "disk_not_on_replicated_storage"
	ReasonXMLNotReplicated  = "xml_not_on_replicated_storage"
	ReasonDRBDNotUpToDate   = "drbd_not_uptodate"
	ReasonPoolNotShared     = "pool_not_shared"
	ReasonLocalDevices      = "local_devices"
	ReasonBootAutostart     = "boot_autostart_enabled"
	ReasonVMRunning         = "vm_running"
	ReasonBridgeMissing     = "bridge_missing_on_nodes"
	ReasonNameCollision     = "name_collision"
	ReasonMemberOffline     = "incus_member_not_online"
	// ReasonNICNetworkNotOnAllMembers: a NIC points at a network or bridge
	// that does not exist on every cluster member, so the instance cannot
	// start after a move ("Parent device ... doesn't exist").
	ReasonNICNetworkNotOnAllMembers = "nic_network_not_on_all_members"
	// WarnDefaultProfileNetworkNotPortable is the readiness warning for the
	// default profile NIC with the same problem.
	WarnDefaultProfileNetworkNotPortable = "incus_default_profile_network_not_portable"
	// PortableNetworkName is the suggested name of a cluster-wide network.
	PortableNetworkName = "ha-net"
)

func (e *Eligibility) add(code, detail, fix string) {
	e.Reasons = append(e.Reasons, Reason{Code: code, Detail: detail, Fix: fix})
	e.Eligible = false
}

// KVMInputs are the facts the caller (compute backend) gathers about a VM.
type KVMInputs struct {
	Name         string
	DiskPaths    []string
	XMLPath      string
	Running      bool
	ClusterNodes []string
	// Bridges are the host bridges the VM NICs attach to; NodeBridges maps a
	// cluster node to the bridges it has.
	Bridges     []string
	NodeBridges map[string][]string
	// Existing are the workloads already under Pacemaker (collision check).
	Existing []Workload
}

// KVMFacts are the storage facts derived while evaluating a VM, reused to
// build the enable plan.
type KVMFacts struct {
	DRBDResource string `json:"drbd_resource"`
	Device       string `json:"device"`
	MountDir     string `json:"mount_dir"`
	FSType       string `json:"fstype"`
}

// DRBDVolume is one volume of `drbdsetup status`.
type DRBDVolume struct {
	Resource string
	Minor    string
	Disk     string
	// PeerDisks are the peer-disk states of the connected peers.
	PeerDisks []string
}

// UpToDate counts the UpToDate replicas (local plus peers).
func (v DRBDVolume) UpToDate() int {
	n := 0
	if v.Disk == "UpToDate" {
		n++
	}
	for _, d := range v.PeerDisks {
		if d == "UpToDate" {
			n++
		}
	}
	return n
}

var tokenRE = regexp.MustCompile(`([A-Za-z-]+):(\S+)`)

// ParseDRBDStatus parses `drbdsetup status --verbose` (also the plain
// `drbdadm status` shape; minors are only known with --verbose). Format
// assumed from DRBD 9 documentation, not verified on the lab:
//
//	kvm_data node-id:0 role:Primary
//	  volume:0 minor:1001 disk:UpToDate
//	  ha2 node-id:1 connection:Connected role:Secondary
//	    volume:0 replication:Established peer-disk:UpToDate
func ParseDRBDStatus(out string) map[string]*DRBDVolume {
	vols := map[string]*DRBDVolume{}
	var cur *DRBDVolume
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		fields := strings.Fields(line)
		tokens := map[string]string{}
		for _, m := range tokenRE.FindAllStringSubmatch(line, -1) {
			tokens[m[1]] = m[2]
		}
		switch {
		case indent == 0:
			cur = &DRBDVolume{Resource: fields[0]}
			vols[fields[0]] = cur
		case cur == nil:
		case indent <= 2:
			if v, ok := tokens["disk"]; ok {
				cur.Disk = v
			}
			if v, ok := tokens["minor"]; ok {
				cur.Minor = v
			}
		default:
			if v, ok := tokens["peer-disk"]; ok {
				cur.PeerDisks = append(cur.PeerDisks, v)
			}
		}
	}
	return vols
}

type mountInfo struct{ Target, Source, FSType string }

func findMount(ctx context.Context, runner sysutil.CommandRunner, p string) (mountInfo, bool) {
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := runner.Run(c, "findmnt", "-n", "-o", "TARGET,SOURCE,FSTYPE", "--target", p)
	if err != nil {
		return mountInfo{}, false
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	if len(f) < 3 {
		return mountInfo{}, false
	}
	src := f[1]
	if i := strings.Index(src, "["); i > 0 {
		src = src[:i] // bind-mount subpath
	}
	return mountInfo{Target: f[0], Source: src, FSType: f[2]}, true
}

// drbdResourceOf maps a mount source to a DRBD resource name.
func drbdResourceOf(source string, status map[string]*DRBDVolume) (string, bool) {
	const prefix = "/dev/drbd/by-res/"
	if strings.HasPrefix(source, prefix) {
		parts := strings.Split(strings.TrimPrefix(source, prefix), "/")
		if len(parts) == 2 && drbdResourceRE.MatchString(parts[0]) {
			return parts[0], true
		}
		return "", false
	}
	if m := regexp.MustCompile(`^/dev/drbd([0-9]+)$`).FindStringSubmatch(source); m != nil {
		for name, v := range status {
			if v.Minor == m[1] {
				return name, true
			}
		}
		return "", false
	}
	return "", false
}

// EvalKVM decides whether a KVM VM can be put under HA. All disks and the
// libvirt XML must live on the same DRBD-backed filesystem with at least two
// UpToDate replicas, and the VM must be powered off (v1).
func EvalKVM(ctx context.Context, runner sysutil.CommandRunner, in KVMInputs) (Eligibility, KVMFacts) {
	e := Eligibility{Eligible: true, Reasons: []Reason{}}
	var facts KVMFacts
	var status map[string]*DRBDVolume
	if out, err := runner.Run(ctx, "drbdsetup", "status", "--verbose"); err == nil {
		status = ParseDRBDStatus(string(out))
	} else {
		status = map[string]*DRBDVolume{}
	}

	check := func(p, code, what string) (string, bool) {
		m, ok := findMount(ctx, runner, p)
		if !ok {
			e.add(code, what+" "+p+": mount could not be determined",
				"Move it onto a DRBD-backed filesystem mounted on this node.")
			return "", false
		}
		res, ok := drbdResourceOf(m.Source, status)
		if !ok {
			e.add(code, what+" "+p+" is on "+m.Source+", which is not a DRBD device",
				"Place it on a DRBD filesystem (/dev/drbd/by-res/<resource>/0).")
			return "", false
		}
		if m.FSType == "" || m.FSType == "-" {
			e.add(code, what+" "+p+": unknown filesystem type", "Mount the volume with a known filesystem.")
			return "", false
		}
		if facts.Device == "" || p == in.XMLPath {
			if p == in.XMLPath {
				facts = KVMFacts{DRBDResource: res, Device: m.Source, MountDir: m.Target, FSType: m.FSType}
			}
		}
		return res, true
	}

	xmlRes := ""
	if in.XMLPath == "" || !path.IsAbs(in.XMLPath) {
		e.add(ReasonXMLNotReplicated, "the VM XML path is unknown", "Define the VM from a persistent XML file on the DRBD volume.")
	} else if r, ok := check(in.XMLPath, ReasonXMLNotReplicated, "XML"); ok {
		xmlRes = r
	}
	resources := map[string]bool{}
	if xmlRes != "" {
		resources[xmlRes] = true
	}
	for _, d := range in.DiskPaths {
		if !path.IsAbs(d) {
			e.add(ReasonDiskNotReplicated, "disk "+d+" is not a local file path", "Use a file-backed disk on the DRBD volume.")
			continue
		}
		r, ok := check(d, ReasonDiskNotReplicated, "disk")
		if !ok {
			continue
		}
		resources[r] = true
		if xmlRes != "" && r != xmlRes {
			e.add(ReasonDiskNotReplicated, "disk "+d+" is on DRBD resource "+r+" but the XML is on "+xmlRes,
				"Keep XML and all disks on the same DRBD resource.")
		}
	}
	for r := range resources {
		v, ok := status[r]
		if !ok {
			e.add(ReasonDRBDNotUpToDate, "DRBD resource "+r+" is not reported by drbdsetup",
				"Check `drbdadm status "+r+"` on this node.")
			continue
		}
		if n := v.UpToDate(); v.Disk != "UpToDate" || n < 2 {
			e.add(ReasonDRBDNotUpToDate, "DRBD resource "+r+" has fewer than 2 UpToDate replicas",
				"Wait for the resync to finish and reconnect every peer.")
		}
	}
	if in.Running {
		e.add(ReasonVMRunning, "the VM is running", "Power the VM off before enabling HA (v1); Pacemaker will start it.")
	}
	if len(in.Bridges) > 0 {
		missing := []string{}
		for _, n := range in.ClusterNodes {
			have := in.NodeBridges[n]
			for _, b := range in.Bridges {
				if !containsStr(have, b) {
					missing = append(missing, n+":"+b)
				}
			}
		}
		if len(missing) > 0 {
			e.add(ReasonBridgeMissing, "bridge missing on: "+strings.Join(missing, ", "),
				"Create the same bridge name on every cluster node.")
		}
	}
	collisionCheck(&e, in.Existing, WorkloadKey{Kind: KindKVM, Name: in.Name}, KVMResourceIDsVM(in.Name))
	return e, facts
}

// KVMResourceIDsVM is the VM primitive id for a name.
func KVMResourceIDsVM(name string) string { _, _, _, vm, _ := KVMResourceIDs(name); return vm }

func collisionCheck(e *Eligibility, existing []Workload, key WorkloadKey, id string) {
	for _, w := range existing {
		if w.ResourceID == id && w.Key != key.String() {
			e.add(ReasonNameCollision, "resource id "+id+" is already used by "+w.Key,
				"Rename the workload so its sanitized id is unique.")
			return
		}
	}
}

// DeviceInfo is one expanded Incus device.
type DeviceInfo struct {
	Name   string
	Type   string
	Pool   string
	Source string
}

// NICInfo is one expanded NIC device. Network is the managed network name,
// Parent the host interface/bridge for unmanaged NICs; Type is nictype.
type NICInfo struct {
	Name    string
	Network string
	Parent  string
	Type    string
}

// IncusInspector is implemented by compute/incus.
type IncusInspector interface {
	RootPoolDriver(inst string) string
	Devices(inst string) []DeviceInfo
	Config(inst string) map[string]string
	MemberOnline(member string) bool
	// NICs lists the expanded NIC devices of the instance.
	NICs(inst string) []NICInfo
	// NetworkMembers reports on which cluster members the network (managed
	// network or host interface/bridge) exists and is usable. Only members
	// whose state could be determined appear in the map; managed tells
	// whether Incus manages it. An error means the answer is unknown.
	NetworkMembers(name string) (present map[string]bool, managed bool, err error)
}

// NICPortability checks every NIC of inst against the cluster members and
// returns one Reason per NIC whose network is missing on some member. A
// network that could not be probed is not reported (unknown is not a
// failure). Fewer than two members means nothing can move.
func NICPortability(insp IncusInspector, inst string, members []string) []Reason {
	if len(members) < 2 {
		return nil
	}
	var out []Reason
	for _, nic := range insp.NICs(inst) {
		name, kind := nic.Network, "network"
		if name == "" {
			name, kind = nic.Parent, "parent bridge/interface"
		}
		if name == "" {
			continue
		}
		present, managed, err := insp.NetworkMembers(name)
		if err != nil {
			continue
		}
		missing := []string{}
		for _, m := range members {
			if ok, known := present[m]; known && !ok {
				missing = append(missing, m)
			}
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		net, targets := name, missing
		if !managed {
			net, targets = PortableNetworkName, append([]string(nil), members...)
			sort.Strings(targets)
		}
		cmds := make([]string, 0, len(targets)+1)
		for _, m := range targets {
			cmds = append(cmds, "incus network create "+net+" --target "+m)
		}
		cmds = append(cmds, "incus network create "+net)
		fix := "Use a cluster-wide managed network: run `incus network create <name> --target <member>` on each member, then `incus network create <name>`, or change the NIC to a network that exists on every member."
		out = append(out, Reason{
			Code:     ReasonNICNetworkNotOnAllMembers,
			Detail:   "NIC " + nic.Name + " uses " + kind + " " + name + ", which is missing on: " + strings.Join(missing, ", "),
			Fix:      fix,
			Commands: cmds,
		})
	}
	return out
}

// IncusInputs are the facts for an Incus instance.
type IncusInputs struct {
	Name     string
	Project  string
	Members  []string
	Existing []Workload
}

// EvalIncus decides whether an Incus instance can be put under HA.
func EvalIncus(insp IncusInspector, in IncusInputs) Eligibility {
	e := Eligibility{Eligible: true, Reasons: []Reason{}}
	if driver := insp.RootPoolDriver(in.Name); driver != "linstor" && driver != "ceph" && driver != "cephfs" {
		e.add(ReasonPoolNotShared, "root pool driver is "+quoteOrNone(driver),
			"Move the instance to a pool with driver linstor, ceph or cephfs.")
	}
	local := []string{}
	for _, d := range insp.Devices(in.Name) {
		switch d.Type {
		case "usb", "pci", "gpu", "unix-char", "unix-block":
			local = append(local, d.Name+" ("+d.Type+")")
		case "disk":
			if d.Pool == "" && d.Source != "" {
				local = append(local, d.Name+" (host path "+d.Source+")")
			}
		}
	}
	if len(local) > 0 {
		e.add(ReasonLocalDevices, "host-bound devices: "+strings.Join(local, ", "),
			"Remove the devices; they cannot follow the instance to another node.")
	}
	if insp.Config(in.Name)["boot.autostart"] == "true" {
		e.add(ReasonBootAutostart, "boot.autostart is true; Incus and Pacemaker would both start it",
			"Set boot.autostart=false (the HA enable path does this).")
	}
	for _, m := range in.Members {
		if !insp.MemberOnline(m) {
			e.add(ReasonMemberOffline, "Incus cluster member "+m+" is not online", "Bring every member online first.")
		}
	}
	for _, r := range NICPortability(insp, in.Name, in.Members) {
		e.add(r.Code, r.Detail, r.Fix)
		e.Reasons[len(e.Reasons)-1].Commands = r.Commands
	}
	collisionCheck(&e, in.Existing, WorkloadKey{Kind: KindIncus, Project: in.Project, Name: in.Name}, IncusResourceID(in.Project, in.Name))
	return e
}

func quoteOrNone(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
