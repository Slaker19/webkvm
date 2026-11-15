package ha

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// VirtualDomainMigration is the live-migration readiness of one Pacemaker
// VirtualDomain primitive. Pacemaker can only migrate a running domain when
// allow-migrate is true and libvirt knows how to move it (shared storage plus
// a reachable transport on both nodes).
type VirtualDomainMigration struct {
	ResourceID         string `json:"resource_id"`
	AllowMigrate       bool   `json:"allow_migrate"`
	MigrationTransport string `json:"migration_transport,omitempty"`
	Ready              bool   `json:"ready"`
}

// ParseVirtualDomainMigrations reads the VirtualDomain primitives from a CIB.
// allow-migrate lives in meta_attributes; migration_transport is an instance
// attribute. Missing values are reported, never assumed.
func ParseVirtualDomainMigrations(cib []byte) []VirtualDomainMigration {
	var root xmlElement
	if err := xml.Unmarshal(cib, &root); err != nil {
		return nil
	}
	var primitives []xmlElement
	root.descendants("primitive", &primitives)
	out := make([]VirtualDomainMigration, 0, len(primitives))
	for _, primitive := range primitives {
		if primitive.attr("type") != "VirtualDomain" {
			continue
		}
		vm := VirtualDomainMigration{ResourceID: primitive.attr("id")}
		if vm.ResourceID == "" {
			continue
		}
		var pairs []xmlElement
		primitive.descendants("nvpair", &pairs)
		for _, pair := range pairs {
			switch pair.attr("name") {
			case "allow-migrate":
				vm.AllowMigrate = strings.EqualFold(strings.TrimSpace(pair.attr("value")), "true")
			case "migration_transport":
				vm.MigrationTransport = strings.TrimSpace(pair.attr("value"))
			}
		}
		vm.Ready = vm.AllowMigrate && vm.MigrationTransport != ""
		out = append(out, vm)
	}
	return out
}

// MigrationCommands returns the pcs commands as text for every VirtualDomain
// that is not migration-ready, plus the manual move/cleanup pair. Nothing is
// executed by WebKVM.
func MigrationCommands(vms []VirtualDomainMigration, targetNode string) (commands, notes []string) {
	notes = []string{
		"Run these commands on the Pacemaker DC and review every line first.",
		"Live migration needs the VM disk on shared storage and libvirt reachable on both nodes; otherwise the resource restarts the VM on the target.",
		"`pcs resource move` creates a temporary constraint. Clear it afterwards so the VM can return.",
	}
	for _, vm := range vms {
		if !vm.AllowMigrate {
			commands = append(commands, fmt.Sprintf("pcs resource meta %s allow-migrate=true", vm.ResourceID))
		}
		if vm.MigrationTransport == "" {
			commands = append(commands, fmt.Sprintf("pcs resource update %s migration_transport=tcp", vm.ResourceID))
		}
	}
	if len(commands) == 0 {
		notes = append(notes, "Every VirtualDomain already has allow-migrate and a migration transport.")
	}
	if strings.TrimSpace(targetNode) != "" {
		for _, vm := range vms {
			commands = append(commands, fmt.Sprintf("pcs resource move %s %s", vm.ResourceID, targetNode))
		}
		if len(vms) > 0 {
			commands = append(commands, "pcs status --full", "pcs resource clear <resource>")
		}
	}
	return commands, notes
}

// ProbeVirtualDomainMigrations reads the CIB (read-only) and returns the
// migration readiness of every VirtualDomain resource.
func ProbeVirtualDomainMigrations(ctx context.Context, runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}) ([]VirtualDomainMigration, error) {
	if runner == nil {
		return nil, fmt.Errorf("command runner unavailable")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cib, err := runner.Run(commandCtx, "pcs", "cluster", "cib")
	if err != nil {
		return nil, err
	}
	return ParseVirtualDomainMigrations(cib), nil
}
