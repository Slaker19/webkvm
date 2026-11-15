package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrSetupBusy means a setup job is already running.
var ErrSetupBusy = errors.New("a cluster setup is already running")

// Setup states and step states.
const (
	SetupIdle    = "idle"
	SetupRunning = "running"
	SetupDone    = "done"
	SetupFailed  = "failed"

	StepPending = "pending"
	StepRunning = "running"
	StepDone    = "done"
	StepSkipped = "skipped"
	StepFailed  = "failed"
)

const setupLogLimit = 200

// SetupStep is the live state of one stage.
type SetupStep struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// SetupStatus is what GET /cluster/status returns.
type SetupStatus struct {
	State             string      `json:"state"`
	Steps             []SetupStep `json:"steps"`
	FailedStep        string      `json:"failed_step"`
	Error             string      `json:"error"`
	Log               []string    `json:"log"`
	CommandsRemaining []string    `json:"commands_remaining"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

type persistedSetup struct {
	Hostname string      `json:"hostname"`
	Status   SetupStatus `json:"status"`
}

// SetupManager runs the cluster formation as a single-flight background job.
type SetupManager struct {
	mu       sync.Mutex
	wg       sync.WaitGroup
	path     string
	hostname string
	status   SetupStatus
	busy     bool
	now      func() time.Time
	sleep    func(time.Duration)
}

// NewSetupManager loads a persisted status from dataDir (may be empty). A job
// that was running when the panel stopped is reported as failed/interrupted.
func NewSetupManager(dataDir string) *SetupManager {
	host, _ := os.Hostname()
	m := &SetupManager{hostname: host, now: time.Now, sleep: time.Sleep, status: freshSetup()}
	if dataDir != "" {
		m.path = filepath.Join(dataDir, "ha-cluster-setup.json")
		if data, err := os.ReadFile(m.path); err == nil {
			var p persistedSetup
			if json.Unmarshal(data, &p) == nil && p.Hostname == host && p.Status.State != "" {
				m.status = normalizeSetup(p.Status)
				if m.status.State == SetupRunning {
					m.status.State = SetupFailed
					m.status.FailedStep = "interrupted"
					m.status.Error = "the panel restarted during the setup; check the cluster state before retrying"
					m.status.CommandsRemaining = []string{"sudo pcs status"}
				}
			}
		}
	}
	return m
}

func freshSetup() SetupStatus {
	return SetupStatus{State: SetupIdle, Steps: []SetupStep{}, Log: []string{}, CommandsRemaining: []string{}}
}

func normalizeSetup(s SetupStatus) SetupStatus {
	if s.Steps == nil {
		s.Steps = []SetupStep{}
	}
	if s.Log == nil {
		s.Log = []string{}
	}
	if s.CommandsRemaining == nil {
		s.CommandsRemaining = []string{}
	}
	return s
}

// Snapshot returns a deep copy.
func (m *SetupManager) Snapshot() SetupStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.status
	s.Steps = append([]SetupStep{}, s.Steps...)
	s.Log = append([]string{}, s.Log...)
	s.CommandsRemaining = append([]string{}, s.CommandsRemaining...)
	return s
}

// Wait blocks until the running job (if any) ends. Used by tests.
func (m *SetupManager) Wait() { m.wg.Wait() }

func (m *SetupManager) saveLocked() {
	m.status.UpdatedAt = m.now().UTC()
	if m.path == "" {
		return
	}
	data, err := json.MarshalIndent(persistedSetup{Hostname: m.hostname, Status: m.status}, "", "  ")
	if err == nil {
		_ = WriteFileAtomic(m.path, data, 0o600)
	}
}

func (m *SetupManager) logf(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Log = append(m.status.Log, m.now().Format("15:04:05")+" "+fmt.Sprintf(format, args...))
	if n := len(m.status.Log); n > setupLogLimit {
		m.status.Log = append([]string(nil), m.status.Log[n-setupLogLimit:]...)
	}
	m.saveLocked()
}

func (m *SetupManager) setStep(id, state, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.status.Steps {
		if m.status.Steps[i].ID == id {
			m.status.Steps[i].State = state
			m.status.Steps[i].Detail = detail
		}
	}
	m.saveLocked()
}

var setupStepOrder = []string{"preflight", "sbd_config", "cluster_setup", "cluster_start", "stonith_properties", "verify"}

// Start launches the job. req must already have passed Plan.OK and Confirm;
// the job re-validates before touching anything. onDone is called once with
// the final status (use it for the audit entry).
func (m *SetupManager) Start(env ClusterEnv, req ClusterRequest, onDone func(SetupStatus)) error {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return ErrSetupBusy
	}
	m.busy = true
	steps := make([]SetupStep, 0, len(setupStepOrder))
	for _, id := range setupStepOrder {
		steps = append(steps, SetupStep{ID: id, State: StepPending})
	}
	m.status = SetupStatus{State: SetupRunning, Steps: steps, Log: []string{}, CommandsRemaining: []string{}}
	m.saveLocked()
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(env, req)
		m.mu.Lock()
		m.busy = false
		m.mu.Unlock()
		if onDone != nil {
			onDone(m.Snapshot())
		}
	}()
	return nil
}

func (m *SetupManager) fail(step, detail string, remaining []string) {
	m.setStep(step, StepFailed, detail)
	m.logf("FAILED %s: %s", step, detail)
	m.mu.Lock()
	m.status.State = SetupFailed
	m.status.FailedStep = step
	m.status.Error = detail
	m.status.CommandsRemaining = remaining
	m.saveLocked()
	m.mu.Unlock()
}

func remainingFrom(all []stepCommands, step string) []string {
	out := []string{}
	found := false
	for _, sc := range all {
		if sc.step == step {
			found = true
		}
		if found {
			out = append(out, sc.commands...)
		}
	}
	return out
}

func tail(out []byte) string {
	text := strings.TrimSpace(string(out))
	if len(text) > 800 {
		text = "[...]" + text[len(text)-800:]
	}
	return text
}

func (m *SetupManager) pcs(ctx context.Context, env ClusterEnv, timeout time.Duration, args []string) error {
	m.logf("$ pcs %s", strings.Join(args, " "))
	out, err := env.run(ctx, timeout, "pcs", args...)
	if err != nil {
		if t := tail(out); t != "" {
			return fmt.Errorf("pcs %s failed: %v: %s", args[0], err, t)
		}
		return fmt.Errorf("pcs %s failed: %v", args[0], err)
	}
	return nil
}

func (m *SetupManager) run(env ClusterEnv, req ClusterRequest) {
	ctx := context.Background()
	sbdOn := req.SBDEnabled()

	m.setStep("preflight", StepRunning, "")
	plan := env.Plan(ctx, req)
	device := plan.WatchdogPath
	all := clusterStepCommands(req, device)
	if !plan.OK {
		m.fail("preflight", "blocked: "+strings.Join(plan.Blockers, ", "), remainingFrom(all, "cluster_setup"))
		return
	}
	if sbdOn && !plan.Watchdog.Present {
		m.fail("preflight", "no /dev/watchdog device on this node: load the watchdog module (for example sudo modprobe softdog) and retry", remainingFrom(all, "sbd_config"))
		return
	}
	m.setStep("preflight", StepDone, "plan ok")
	m.logf("preflight ok")

	if sbdOn {
		m.setStep("sbd_config", StepRunning, "")
		path, err := env.WriteSBDConfig(device, strconv.Itoa(req.SBDTimeout()))
		if err != nil {
			m.fail("sbd_config", "cannot write the SBD configuration: "+err.Error(), remainingFrom(all, "sbd_config"))
			return
		}
		m.setStep("sbd_config", StepDone, path)
		m.logf("wrote %s", path)
	} else {
		m.setStep("sbd_config", StepSkipped, "SBD disabled")
	}

	// Last look before the only step that creates cluster state.
	if reasons := env.ClusterExists(ctx); len(reasons) > 0 {
		m.fail("cluster_setup", "refusing: "+strings.Join(reasons, "; "), remainingFrom(all, "cluster_setup"))
		return
	}
	m.setStep("cluster_setup", StepRunning, "")
	if err := m.pcs(ctx, env, 5*time.Minute, setupArgs(req)); err != nil {
		m.fail("cluster_setup", err.Error(), remainingFrom(all, "cluster_setup"))
		return
	}
	if sbdOn {
		if err := m.pcs(ctx, env, 2*time.Minute, sbdEnableArgs(req, device)); err != nil {
			m.fail("cluster_setup", err.Error(), []string{pcsLine(sbdEnableArgs(req, device))})
			return
		}
	}
	m.setStep("cluster_setup", StepDone, "")

	m.setStep("cluster_start", StepRunning, "")
	if err := m.pcs(ctx, env, 3*time.Minute, []string{"cluster", "start", "--all"}); err != nil {
		m.fail("cluster_start", err.Error(), remainingFrom(all, "cluster_start"))
		return
	}
	if req.EnableOnBoot {
		if err := m.pcs(ctx, env, time.Minute, []string{"cluster", "enable", "--all"}); err != nil {
			m.fail("cluster_start", err.Error(), []string{"sudo pcs cluster enable --all", pcsLine(propertyArgs(req)), "sudo pcs status"})
			return
		}
		m.setStep("cluster_start", StepDone, "started and enabled on boot")
	} else {
		m.setStep("cluster_start", StepDone, "started (not enabled on boot)")
	}

	m.setStep("stonith_properties", StepRunning, "")
	if err := m.pcs(ctx, env, time.Minute, propertyArgs(req)); err != nil {
		m.fail("stonith_properties", err.Error(), remainingFrom(all, "stonith_properties"))
		return
	}
	m.setStep("stonith_properties", StepDone, "")

	m.setStep("verify", StepRunning, "")
	var detail string
	ok := false
	for attempt := 0; attempt < 6 && !ok; attempt++ {
		if attempt > 0 {
			m.sleep(5 * time.Second)
		}
		ok, detail = verifyCluster(ctx, env, req)
	}
	if !ok {
		m.fail("verify", detail, remainingFrom(all, "verify"))
		return
	}
	m.setStep("verify", StepDone, detail)
	m.logf("cluster formed: %s", detail)
	m.mu.Lock()
	m.status.State = SetupDone
	m.status.CommandsRemaining = []string{}
	m.saveLocked()
	m.mu.Unlock()
}

func verifyCluster(ctx context.Context, env ClusterEnv, req ClusterRequest) (bool, string) {
	st := ProbePacemaker(ctx, env.Runner)
	if st.State != StateObserved && st.State != StateBlocked {
		return false, "pacemaker state is " + st.State
	}
	if st.Quorum == nil || !*st.Quorum {
		return false, "the cluster has no quorum yet"
	}
	if st.NodesOnline < len(req.Nodes) {
		return false, fmt.Sprintf("%d of %d nodes online", st.NodesOnline, len(req.Nodes))
	}
	wd := st.WatchdogFencing != nil && *st.WatchdogFencing
	if req.SBDEnabled() && !wd {
		return false, "stonith-watchdog-timeout is not active"
	}
	if st.FencingEnabled == nil || !*st.FencingEnabled {
		return false, "stonith is not enabled"
	}
	return true, fmt.Sprintf("quorum ok, %d/%d nodes online, stonith enabled", st.NodesOnline, len(req.Nodes))
}
