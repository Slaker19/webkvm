package api

import (
	"net/http"
	"regexp"

	"webkvm/internal/ha"
	"webkvm/internal/sysutil"
)

type haVMMigration struct {
	Available bool                        `json:"available"`
	Error     string                      `json:"error,omitempty"`
	VMs       []ha.VirtualDomainMigration `json:"vms"`
	Commands  []string                    `json:"commands"`
	Notes     []string                    `json:"notes"`
}

var targetNodeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// GetHAVMMigration returns the live-migration readiness of every Pacemaker
// VirtualDomain and the pcs commands to enable it. Read-only: it parses the
// CIB and never updates a resource or migrates a VM.
func (h *Handler) GetHAVMMigration(w http.ResponseWriter, r *http.Request) {
	result := haVMMigration{VMs: []ha.VirtualDomainMigration{}, Commands: []string{}, Notes: []string{}}
	target := r.URL.Query().Get("target")
	if target != "" && !targetNodeRE.MatchString(target) {
		jsonErr(w, http.StatusBadRequest, "invalid target node name")
		return
	}
	vms, err := ha.ProbeVirtualDomainMigrations(r.Context(), sysutil.DefaultRunner)
	if err != nil {
		result.Error = "Pacemaker CIB is not available: " + err.Error()
		notes, _ := ha.MigrationCommands(nil, "")
		result.Notes = notes
		jsonResp(w, http.StatusOK, result)
		return
	}
	commands, notes := ha.MigrationCommands(vms, target)
	result.Available = true
	result.VMs = vms
	result.Commands = commands
	result.Notes = notes
	jsonResp(w, http.StatusOK, result)
}
