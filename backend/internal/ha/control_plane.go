package ha

import (
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
	"webkvm/internal/sysutil"
)

// Control-plane check codes.
const (
	CPGroupMissing      = "cp_group_missing"
	CPFsNotDataDir      = "cp_fs_not_data_dir"
	CPNoVIP             = "cp_no_vip"
	CPAppAgentMissing   = "cp_app_agent_missing"
	CPColocationMissing = "cp_colocation_missing"
	CPOrderMissing      = "cp_order_missing"
)

// Check is one failed control-plane verification.
type Check struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// VerifyControlPlane inspects the CIB for an existing control plane group and
// returns only the failing checks (empty = healthy, adopt it as is). dataDir
// is optional: when given, the Filesystem must mount exactly there.
func VerifyControlPlane(cib []byte, cpGroup string, dataDir ...string) []Check {
	cpGroup = normCPGroup(cpGroup)
	var root xmlElement
	if err := xml.Unmarshal(cib, &root); err != nil {
		return []Check{{Code: CPGroupMissing, Detail: "the CIB is not readable", Fix: "Check `pcs cluster cib`."}}
	}
	prims := cibPrimitives(root)
	members := []cibPrimitive{}
	for _, p := range prims {
		if p.Group == cpGroup {
			members = append(members, p)
		}
	}
	if len(members) == 0 {
		return []Check{{Code: CPGroupMissing, Detail: "group " + cpGroup + " does not exist or is empty",
			Fix: "Create it (see ControlPlaneCommands) or set ha.cp_group to the existing group."}}
	}
	out := []Check{}
	var hasFS, fsOK, hasVIP, hasApp bool
	for _, p := range members {
		switch {
		case p.Agent == "ocf:heartbeat:Filesystem":
			hasFS = true
			if len(dataDir) == 0 || dataDir[0] == "" || strings.TrimRight(p.Params["directory"], "/") == strings.TrimRight(dataDir[0], "/") {
				fsOK = true
			}
		case p.Agent == "ocf:heartbeat:IPaddr2":
			hasVIP = true
		case p.Agent == "ocf:webkvm:webkvm":
			hasApp = true
		}
	}
	if !hasFS || !fsOK {
		out = append(out, Check{Code: CPFsNotDataDir, Detail: "no Filesystem in " + cpGroup + " mounts DATA_DIR",
			Fix: "Add an ocf:heartbeat:Filesystem on the DRBD device with directory=<DATA_DIR>."})
	}
	if !hasVIP {
		out = append(out, Check{Code: CPNoVIP, Detail: "no IPaddr2 resource in " + cpGroup,
			Fix: "Add an ocf:heartbeat:IPaddr2 virtual IP to the group."})
	}
	if !hasApp {
		out = append(out, Check{Code: CPAppAgentMissing, Detail: "no ocf:webkvm:webkvm resource in " + cpGroup,
			Fix: "Install the webkvm OCF agent and add the resource to the group."})
	}
	clones := map[string]bool{}
	var collect func(e xmlElement)
	collect = func(e xmlElement) {
		for _, c := range e.Children {
			if c.XMLName.Local == "clone" || c.XMLName.Local == "master" {
				clones[c.attr("id")] = true
			}
			collect(c)
		}
	}
	collect(root)
	colocated, ordered := false, false
	if cs := root.find("constraints"); cs != nil {
		for _, c := range cs.Children {
			switch c.XMLName.Local {
			case "rsc_colocation":
				role := c.attr("with-rsc-role")
				if inGroup(c.attr("rsc"), cpGroup, members) && clones[c.attr("with-rsc")] && (role == "Promoted" || role == "Master") {
					colocated = true
				}
			case "rsc_order":
				action := c.attr("first-action")
				if clones[c.attr("first")] && (action == "promote") && inGroup(c.attr("then"), cpGroup, members) {
					ordered = true
				}
			}
		}
	}
	if !colocated {
		out = append(out, Check{Code: CPColocationMissing, Detail: "no colocation of " + cpGroup + " with the Promoted DRBD clone",
			Fix: "pcs constraint colocation add " + cpGroup + " with Promoted <drbd-clone> score=INFINITY"})
	}
	if !ordered {
		out = append(out, Check{Code: CPOrderMissing, Detail: "no promote-then-start order from the DRBD clone to " + cpGroup,
			Fix: "pcs constraint order promote <drbd-clone> then start " + cpGroup})
	}
	return out
}

func inGroup(id, group string, members []cibPrimitive) bool {
	if id == group {
		return true
	}
	for _, m := range members {
		if m.ID == id {
			return true
		}
	}
	return false
}

// CPSpec describes a control plane for a NEW cluster.
type CPSpec struct {
	Group        string
	DRBDResource string
	Device       string
	DataDir      string
	VIP          string
	VIPCIDR      int
	VIPNIC       string
}

// ControlPlaneCommands returns copyable pcs commands for a NEW cluster. It
// executes nothing; existing control planes are adopted, never recreated.
func ControlPlaneCommands(spec CPSpec) ([]string, error) {
	spec.Group = normCPGroup(spec.Group)
	if !WorkloadNameRE.MatchString(spec.Group) || !drbdResourceRE.MatchString(spec.DRBDResource) ||
		!drbdDeviceRE.MatchString(spec.Device) || !absPathRE.MatchString(spec.DataDir) || strings.Contains(spec.DataDir, "..") {
		return nil, fmt.Errorf("invalid control plane specification")
	}
	if !ipRE.MatchString(spec.VIP) || spec.VIPCIDR < 8 || spec.VIPCIDR > 32 {
		return nil, fmt.Errorf("invalid virtual IP")
	}
	if spec.VIPNIC != "" && !WorkloadNameRE.MatchString(spec.VIPNIC) {
		return nil, fmt.Errorf("invalid NIC name")
	}
	nic := ""
	if spec.VIPNIC != "" {
		nic = " nic=" + spec.VIPNIC
	}
	clone := "webkvm-data-clone"
	return []string{
		"pcs resource create webkvm-data ocf:linbit:drbd drbd_resource=" + spec.DRBDResource + " promotable promoted-max=1 promoted-node-max=1 clone-max=3 clone-node-max=1 notify=true",
		"pcs resource create webkvm-fs ocf:heartbeat:Filesystem device=" + spec.Device + " directory=" + spec.DataDir + " fstype=ext4 --group " + spec.Group,
		fmt.Sprintf("pcs resource create webkvm-vip ocf:heartbeat:IPaddr2 ip=%s cidr_netmask=%d%s --group %s", spec.VIP, spec.VIPCIDR, nic, spec.Group),
		"pcs resource create webkvm-app ocf:webkvm:webkvm --group " + spec.Group,
		"pcs resource meta " + spec.Group + " resource-stickiness=100",
		"pcs constraint colocation add " + spec.Group + " with Promoted " + clone + " score=INFINITY",
		"pcs constraint order promote " + clone + " then start " + spec.Group,
	}, nil
}

var ipRE = regexp.MustCompile(`^[0-9a-fA-F:.]{2,45}$`)

// ControlPlaneChecks reads the live CIB and verifies the control plane.
func ControlPlaneChecks(ctx context.Context, runner sysutil.CommandRunner, cpGroup string, dataDir ...string) ([]Check, error) {
	if runner == nil {
		return nil, ErrPacemakerUnavailable
	}
	cib, err := runReadOnly(ctx, runner, "cluster", "cib")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPacemakerUnavailable, err)
	}
	return VerifyControlPlane(cib, cpGroup, dataDir...), nil
}
