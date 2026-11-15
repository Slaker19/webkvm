package ha

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultFencePVEStonithID is the primitive name used unless configured
// otherwise. `pcs stonith create <id> fence_pve ...`.
const DefaultFencePVEStonithID = "fence-pve"

// FencePVEConfig holds the Proxmox token used by the fence_pve stonith
// agent. TokenSecret is persisted in the settings store as a secret and is
// never included in generated commands (the operator pastes it by hand), so
// an API response or proxy log cannot leak it.
type FencePVEConfig struct {
	Enabled     bool
	Endpoint    string // https://pve.example:8006
	TokenID     string // root@pam!webkvm
	TokenSecret string
	PVENode     string // Proxmox node that hosts the Pacemaker VM(s)
	HostMap     string // cluster-node:vmid,cluster-node:vmid
	InsecureTLS bool
	StonithID   string
}

// PVETokenCheck is the result of the read-only Proxmox API probe.
type PVETokenCheck struct {
	Valid     bool   `json:"valid"`
	PowerMgmt bool   `json:"power_mgmt"`
	Message   string `json:"message,omitempty"`
}

// IsHypervisorFenceAgent reports whether a stonith agent powers the node
// off from outside its kernel. fence_sbd/fence_mpath are deliberately absent:
// watchdog/SBD is already reported separately and does not protect against a
// hung hypervisor host.
func IsHypervisorFenceAgent(agent string) bool {
	switch agent {
	case "fence_pve", "fence_virsh", "fence_rhevm", "fence_redfish",
		"fence_ipmilan", "fence_amt", "fence_ibmblade", "fence_cisco_ucs":
		return true
	}
	for _, prefix := range []string{"fence_ilo", "fence_idrac", "fence_drac"} {
		if strings.HasPrefix(agent, prefix) {
			return true
		}
	}
	return false
}

// HasHypervisorFence reports whether any configured primitive is external
// fencing. A cluster with only the SBD watchdog returns false, which is the
// condition the HA readiness reports as critical.
func HasHypervisorFence(agents []string) bool {
	for _, agent := range agents {
		if IsHypervisorFenceAgent(agent) {
			return true
		}
	}
	return false
}

var (
	pveNodeRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	pveHostMapRE = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*:[0-9]+)(,[A-Za-z0-9][A-Za-z0-9._-]*:[0-9]+)*$`)
)

func (c FencePVEConfig) stonithID() string {
	if id := strings.TrimSpace(c.StonithID); id != "" {
		return id
	}
	return DefaultFencePVEStonithID
}

// ValidateFencePVEConfig rejects values that would make the generated pcs
// command wrong or unsafe to copy/paste. The token secret is only checked
// for presence: the value is never echoed.
func ValidateFencePVEConfig(c FencePVEConfig) error {
	u, err := url.Parse(strings.TrimSpace(c.Endpoint))
	if err != nil || u.Host == "" {
		return errors.New("endpoint must be a URL such as https://pve.example:8006")
	}
	if u.Scheme != "https" {
		return errors.New("endpoint must use https: the API token travels in a header")
	}
	if !strings.Contains(c.TokenID, "!") {
		return errors.New("token_id must look like user@realm!tokenid")
	}
	if strings.TrimSpace(c.TokenSecret) == "" {
		return errors.New("token_secret is empty")
	}
	if !pveNodeRE.MatchString(strings.TrimSpace(c.PVENode)) {
		return errors.New("pve_node must be the Proxmox node name (letters, digits, . _ -)")
	}
	if hm := strings.TrimSpace(c.HostMap); hm != "" && !pveHostMapRE.MatchString(hm) {
		return errors.New("host_map must look like webkvm-1:100,webkvm-2:101")
	}
	return nil
}

// FencePVECommands returns the pcs commands as text. Nothing here is
// executed: the operator reviews and runs them by hand.
//
// The token secret is rendered as a placeholder on purpose. Persisting it in
// the command output would copy it into shell history, terminal scrollback
// and API logs.
func FencePVECommands(c FencePVEConfig, nodeNames []string) (commands, notes []string, err error) {
	if err := ValidateFencePVEConfig(c); err != nil {
		return nil, nil, err
	}
	notes = []string{
		"Run these commands on the node that is currently the Pacemaker DC.",
		"Replace <PVE_TOKEN_SECRET> with the token secret you created in Proxmox (Datacenter > Permissions > API Tokens); the web UI never shows it.",
		"The token needs only VM.PowerMgmt (and VM.Audit to list the VMs) on the guests mapped below.",
	}
	hostMap := strings.TrimSpace(c.HostMap)
	if hostMap == "" {
		if len(nodeNames) == 0 {
			return nil, notes, errors.New("host_map is empty and Pacemaker did not report node names to build it")
		}
		parts := make([]string, 0, len(nodeNames))
		for _, node := range nodeNames {
			node = strings.TrimSpace(node)
			if node == "" {
				continue
			}
			parts = append(parts, node+":<VMID_"+node+">")
		}
		hostMap = strings.Join(parts, ",")
		notes = append(notes, "Replace each <VMID_...> with the Proxmox VMID of that cluster node's VM.")
	}

	// fence_pve takes the host in `ip` and the port in `ipport`; "host:8006"
	// in `ip` makes the agent resolve a name that does not exist (M13).
	u, _ := url.Parse(strings.TrimSpace(c.Endpoint))
	ip := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "8006"
	}

	commands = []string{
		"pcs property set stonith-enabled=true",
		fmt.Sprintf("pcs stonith create %s fence_pve ip=%s ipport=%s pve_node=%s token_id=%s token_secret='<PVE_TOKEN_SECRET>' pcmk_host_map=%s",
			c.stonithID(),
			shellQuote(ip),
			shellQuote(port),
			shellQuote(strings.TrimSpace(c.PVENode)),
			shellQuote(strings.TrimSpace(c.TokenID)),
			shellQuote(hostMap),
		),
		fmt.Sprintf("pcs stonith config %s", c.stonithID()),
		"pcs stonith history show",
	}
	return commands, notes, nil
}

// shellQuote wraps a value for a POSIX shell so a copy/paste cannot break
// out of the argument. pcs itself receives the unquoted value.
func shellQuote(v string) string {
	if v == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// ProbeFencePVEAgent reports whether the fence_pve agent is installed
// locally. `pcs stonith list` is read-only and also works when the cluster
// is not running.
func ProbeFencePVEAgent(ctx context.Context, runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}) bool {
	if runner == nil {
		return false
	}
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := runner.Run(commandCtx, "pcs", "stonith", "list")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "fence_pve" {
			return true
		}
	}
	return false
}

// CheckPVEToken calls Proxmox read-only and verifies the token exists and
// carries VM.PowerMgmt. It never powers anything off.
func CheckPVEToken(ctx context.Context, c FencePVEConfig) (PVETokenCheck, error) {
	if err := ValidateFencePVEConfig(c); err != nil {
		return PVETokenCheck{}, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api2/json/access/permissions", nil)
	if err != nil {
		return PVETokenCheck{}, err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+strings.TrimSpace(c.TokenID)+"="+c.TokenSecret)

	client := &http.Client{Timeout: 8 * time.Second}
	if c.InsecureTLS {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // self-signed PVE
	}
	resp, err := client.Do(req)
	if err != nil {
		return PVETokenCheck{Message: "Proxmox API unreachable: " + err.Error()}, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return PVETokenCheck{Message: fmt.Sprintf("Proxmox rejected the token (HTTP %d)", resp.StatusCode)}, nil
	default:
		return PVETokenCheck{Message: fmt.Sprintf("Proxmox returned HTTP %d", resp.StatusCode)}, nil
	}

	var payload struct {
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return PVETokenCheck{Message: "unexpected Proxmox response: " + err.Error()}, nil
	}
	check := PVETokenCheck{Valid: true}
	for _, perms := range payload.Data {
		for name, value := range perms {
			if name != "VM.PowerMgmt" {
				continue
			}
			if granted(value) {
				check.PowerMgmt = true
			}
		}
	}
	if !check.PowerMgmt {
		check.Message = "token is valid but has no VM.PowerMgmt on any path; fencing would fail"
	}
	return check, nil
}

// granted accepts both the {priv: 1} and {"priv": "1"} shapes Proxmox has
// used across versions.
func granted(raw json.RawMessage) bool {
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n == 1
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := strconv.Atoi(s); err == nil {
			return v == 1
		}
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	return false
}
