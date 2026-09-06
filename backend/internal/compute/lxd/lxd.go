// Package lxd is the LXD container backend (v1.4 Fase 1).
//
// LXDBackend implements the full compute.Backend seam against the LXD
// daemon via the official Canonical Go client (github.com/canonical/lxd
// /client). Connection targets the local unix socket — the snap path
// /var/snap/lxd/common/lxd/unix.socket by default.
//
// Implementation is GRADUAL and fail-safe: Fase 1 implements the
// read path (ListVMs) so containers appear alongside VMs in the API;
// every other operation returns compute.ErrNotImplemented, which the
// handlers surface as HTTP 501 Not Implemented. As features land they
// replace the stub one at a time without touching the seam or the
// handlers.
package lxd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lxd "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"
	"github.com/gorilla/websocket"

	"webkvm/internal/backupstore"
	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// DefaultSocketPath is the default LXD daemon unix socket (snap install
// path, with the apt path as fallback).
func DefaultSocketPath() string {
	snap := "/var/snap/lxd/common/lxd/unix.socket"
	if st, err := os.Stat(snap); err == nil && !st.IsDir() {
		return snap
	}
	return "/var/lib/lxd/unix.socket"
}

// LXDBackend is the compute.Backend adapter for the LXD daemon.
type LXDBackend struct {
	client lxd.InstanceServer
}

// NewLXDBackend connects to the LXD daemon over a unix socket. An empty
// path uses DefaultSocketPath(). Returns an error (including
// compute.ErrNotImplemented if LXD is unreachable) when the daemon
// cannot be reached, so the caller can degrade to KVM-only.
func NewLXDBackend(socketPath string) (*LXDBackend, error) {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	client, err := lxd.ConnectLXDUnix(socketPath, &lxd.ConnectionArgs{})
	if err != nil {
		return nil, err
	}
	return &LXDBackend{client: client}, nil
}

// ServerInfo returns the LXD daemon version for the status page.
func (b *LXDBackend) ServerInfo() (string, error) {
	s, _, err := b.client.GetServer()
	if err != nil {
		return "", err
	}
	return s.Environment.ServerVersion, nil
}

// Close closes the underlying client connections.
func (b *LXDBackend) Close() {
	b.client.Disconnect()
}

// --- Instance lifecycle ---

// ListVMs lists every LXD instance (containers + VMs) and maps them to
// the neutral domain model. This is the Fase 1 MVP: it makes containers
// appear in the unified VM list.
func (b *LXDBackend) ListDomains() ([]models.VM, error) {
	instances, err := b.client.GetInstances(lxd.GetInstancesArgs{InstanceType: api.InstanceTypeAny})
	if err != nil {
		return nil, err
	}
	out := make([]models.VM, 0, len(instances))
	for i := range instances {
		out = append(out, instanceToVM(&instances[i]))
	}
	return out, nil
}

func (b *LXDBackend) GetDomain(id string) (models.VM, error) {
	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return models.VM{}, err
	}
	return instanceToVM(inst), nil
}

func (b *LXDBackend) DomainExists(name string) (bool, error) {
	_, _, err := b.client.GetInstance(name)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (b *LXDBackend) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	return models.VM{}, compute.ErrNotImplemented
}
func (b *LXDBackend) UpdateDomain(id string, req models.UpdateVMRequest) (models.VM, error) {
	return models.VM{}, compute.ErrNotImplemented
}
func (b *LXDBackend) DeleteDomain(id string) error {
	// Force: deleting a running container would otherwise be rejected and
	// the handler expects delete to always succeed (matching KVM).
	op, err := b.client.DeleteInstance(id, true)
	if err != nil {
		return mapLXErr(err)
	}
	return waitOperation(op)
}
func (b *LXDBackend) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	return models.VM{}, compute.ErrNotImplemented
}
func (b *LXDBackend) StartDomain(id string) error {
	return b.setState(id, "start", 30, false)
}
func (b *LXDBackend) ShutdownDomain(id string) error {
	// Graceful stop: LXD sends the guest a shutdown signal and waits up to
	// the timeout before giving up (Force=false).
	return b.setState(id, "stop", 60, false)
}
func (b *LXDBackend) ForceOffDomain(id string) error {
	// Kill: immediate forced stop (no graceful shutdown grace period).
	return b.setState(id, "stop", 0, true)
}
func (b *LXDBackend) RebootDomain(id string) error {
	return b.setState(id, "restart", 60, false)
}
func (b *LXDBackend) SuspendDomain(id string) error { return b.setState(id, "freeze", 30, false) }
func (b *LXDBackend) ResumeDomain(id string) error  { return b.setState(id, "unfreeze", 30, false) }
func (b *LXDBackend) SetDomainAutostart(id string, enabled bool) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) GetDomainAutostart(id string) (bool, error) {
	return false, compute.ErrNotImplemented
}
func (b *LXDBackend) SetBootDevice(id string, device string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) GetBootDevice(id string) (string, error)      { return "", compute.ErrNotImplemented }
func (b *LXDBackend) ValidateDomainDisks(id string) error          { return compute.ErrNotImplemented }

// setState drives the LXD instance state machine (start/stop/restart/
// freeze/unfreeze) and waits for the operation to complete.
func (b *LXDBackend) setState(id, action string, timeout int, force bool) error {
	op, err := b.client.UpdateInstanceState(id, api.InstanceStatePut{
		Action:  action,
		Timeout: timeout,
		Force:   force,
	}, "")
	if err != nil {
		return mapLXErr(err)
	}
	return waitOperation(op)
}

// waitOperation polls an LXD operation until it reaches a terminal state.
// Deliberately avoids Operation.Wait(), which subscribes to the /1.0/events
// websocket — polling Refresh()/Get() is equally correct against a real
// daemon and keeps the adapter testable against a minimal server.
func waitOperation(op lxd.Operation) error {
	const timeout = 90 * time.Second
	deadline := time.Now().Add(timeout)
	for {
		cur := op.Get()
		switch cur.Status {
		case "Success":
			if cur.Err != "" {
				return errors.New(cur.Err)
			}
			return nil
		case "Failure", "Cancelled":
			if cur.Err != "" {
				return errors.New(cur.Err)
			}
			return errors.New("LXD operation failed")
		}
		if time.Now().After(deadline) {
			return errors.New("LXD operation timed out")
		}
		if err := op.Refresh(); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// --- Disks / devices / USB ---

func (b *LXDBackend) AttachDisk(id string, req models.AttachDiskRequest) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) DetachDisk(id, target string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) ChangeDiskBus(id, target, newBus string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) UpdateDiskSource(id, target, source string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) ResizeDomainDisk(ctx context.Context, id, target string, newSizeGB int64) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *LXDBackend) AttachNetworkIface(id string, req models.AttachNetRequest) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) DetachNetworkIface(id, mac string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) UpdateNetworkIface(id, oldMAC string, req models.UpdateNetIfaceRequest) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) AttachUSBDevice(id, vendorID, productID string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) DetachUSBDevice(id, vendorID, productID string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) ListHostUSBDevices() ([]models.USBDevice, error) {
	return nil, compute.ErrNotImplemented
}

// --- Snapshots ---

func (b *LXDBackend) ListSnapshots(domainID string) ([]models.Snapshot, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) CreateSnapshot(domainID string, req models.CreateSnapshotRequest) (models.Snapshot, error) {
	return models.Snapshot{}, compute.ErrNotImplemented
}
func (b *LXDBackend) DeleteSnapshot(domainID, snapID string) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *LXDBackend) RevertSnapshot(domainID, snapID string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) ExportSnapshots(domainID string) ([]backupstore.SnapshotBackup, error) {
	return nil, compute.ErrNotImplemented
}

// --- Storage / pools / volumes / ISO ---

func (b *LXDBackend) ListStoragePools() ([]models.StoragePool, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) CreateStoragePool(ctx context.Context, req models.CreatePoolRequest) (models.StoragePool, error) {
	return models.StoragePool{}, compute.ErrNotImplemented
}
func (b *LXDBackend) UpdateStoragePool(ctx context.Context, name string, req models.UpdatePoolRequest) (models.StoragePool, error) {
	return models.StoragePool{}, compute.ErrNotImplemented
}
func (b *LXDBackend) DeletePool(name string) error            { return compute.ErrNotImplemented }
func (b *LXDBackend) RefreshPool(name string) error           { return compute.ErrNotImplemented }
func (b *LXDBackend) GetPoolPath(name string) (string, error) { return "", compute.ErrNotImplemented }
func (b *LXDBackend) DiskPoolName() string                    { return "" }
func (b *LXDBackend) ISOPoolName() string                     { return "" }
func (b *LXDBackend) ListStorageVolumes(poolName string) ([]models.StorageVolume, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) GetStorageVolume(poolName, volName string) (models.StorageVolume, error) {
	return models.StorageVolume{}, compute.ErrNotImplemented
}
func (b *LXDBackend) CreateStorageVolume(req models.CreateVolumeRequest) (models.StorageVolume, error) {
	return models.StorageVolume{}, compute.ErrNotImplemented
}
func (b *LXDBackend) ResizeStorageVolume(poolName, volName string, newSizeGB int64) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) DeleteStorageVolume(poolName, volName string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) VolumeExists(poolName, volName string) (bool, error) {
	return false, compute.ErrNotImplemented
}
func (b *LXDBackend) FindVolumeAttachments(poolName, volName string) ([]models.VolumeAttachment, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) GetISOs(poolName string) ([]models.ISOScanResult, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) RenameISO(oldName, newName, poolName string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) DeleteISO(name, poolName string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) DeleteVMDiskFiles(vmName string) (deleted []string, skipped []string, err error) {
	return nil, nil, compute.ErrNotImplemented
}
func (b *LXDBackend) RefreshCIFSSecretIfNeeded(ctx context.Context, poolName string) (*compute.SecretRef, error) {
	return nil, compute.ErrNotImplemented
}

// --- Networking ---

func (b *LXDBackend) ListNetworks() ([]models.Network, error) { return nil, compute.ErrNotImplemented }
func (b *LXDBackend) CreateNetwork(req models.CreateNetworkRequest) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *LXDBackend) UpdateNetwork(name string, req models.UpdateNetworkRequest) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *LXDBackend) DeleteNetwork(id string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) StartNetwork(name string) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *LXDBackend) StopNetwork(name string) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *LXDBackend) CheckVLANSupport(networkName string) (models.VlanSupport, error) {
	return models.VlanSupport{}, compute.ErrNotImplemented
}

// --- Console / cloud-init / metadata ---

// lxdConsoleStream adapts an LXD interactive exec (PTY over websockets)
// to the neutral ConsoleStream the serial proxy already uses. Bytes flow
// straight from/to the container PTY through the client's internal
// websocket bridge — the frontend terminal never notices the hypervisor.
type lxdConsoleStream struct {
	op     lxd.Operation
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	done   chan bool
	ctrlMu sync.Mutex
	ctrl   *websocket.Conn // window-resize/signal channel
	closed atomic.Bool
}

func (s *lxdConsoleStream) Recv(buf []byte) (int, error) { return s.stdout.Read(buf) }
func (s *lxdConsoleStream) Send(b []byte) (int, error) {
	// The KVM serial proxy also forwards resize JSON; only LXD execs are
	// real PTYs, so translate resize frames here without the proxy knowing.
	var rs struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if json.Unmarshal(b, &rs) == nil && rs.Cols > 0 && rs.Rows > 0 {
		_ = s.resize(rs.Cols, rs.Rows)
		return len(b), nil
	}
	return s.stdin.Write(b)
}
func (s *lxdConsoleStream) resize(cols, rows int) error {
	ctrl := s.controlConn()
	if ctrl == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"command": "window-resize", "width": cols, "height": rows})
	return ctrl.WriteMessage(websocket.TextMessage, payload)
}
func (s *lxdConsoleStream) controlConn() *websocket.Conn {
	s.ctrlMu.Lock()
	defer s.ctrlMu.Unlock()
	return s.ctrl
}
func (s *lxdConsoleStream) setControl(conn *websocket.Conn) {
	s.ctrlMu.Lock()
	s.ctrl = conn
	s.ctrlMu.Unlock()
}
func (s *lxdConsoleStream) Finish() error {
	// Signal SIGHUP on the control channel so the PTY shell exits.
	if c := s.controlConn(); c != nil {
		payload, _ := json.Marshal(map[string]any{"command": "signal", "signal": 1})
		_ = c.WriteMessage(websocket.TextMessage, payload)
	}
	_ = s.stdin.Close()
	return nil
}
func (s *lxdConsoleStream) Free() {
	s.closed.Store(true)
	_ = s.stdin.Close()
	_ = s.stdout.Close()
	// Wait for the exec bridge to finish (bounded).
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
	}
}

// OpenSerialConsole opens an interactive shell (bash) inside the LXD
// instance and returns a ConsoleStream bridged to it. A stopped or
// missing instance returns compute.ErrDomainNotRunning so the serial
// proxy retries until the container is running.
func (b *LXDBackend) OpenSerialConsole(id string) (compute.ConsoleStream, error) {
	// Pre-check the instance is running: exec on a stopped container
	// would fail asynchronously with no websockets to attach to.
	state, _, err := b.client.GetInstanceState(id)
	if err != nil {
		return nil, mapLXErr(err)
	}
	if state.Status != "Running" {
		return nil, compute.ErrDomainNotRunning
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	done := make(chan bool)
	stream := &lxdConsoleStream{stdin: stdinW, stdout: stdoutR, done: done}

	exec := api.InstanceExecPost{
		Command:     []string{"bash"},
		Interactive: true,
		WaitForWS:   true,
		Environment: map[string]string{"TERM": "xterm-256color"},
		Width:       120,
		Height:      30,
	}
	op, err := b.client.ExecInstance(id, exec, &lxd.InstanceExecArgs{
		Stdin:    stdinR,
		Stdout:   stdoutW,
		Control:  stream.setControl,
		DataDone: done,
	})
	if err != nil {
		stdinW.Close()
		stdoutR.Close()
		return nil, mapLXErr(err)
	}
	stream.op = op
	return stream, nil
}
func (b *LXDBackend) SetUserPassword(id, user, password string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) GetVMMeta(uuid string) (models.VMMeta, error) {
	return models.VMMeta{}, compute.ErrNotImplemented
}
func (b *LXDBackend) SetVMMeta(uuid string, meta models.VMMeta) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) UpdateVMMeta(uuid string, upd models.VMMetaUpdate) (models.VMMeta, error) {
	return models.VMMeta{}, compute.ErrNotImplemented
}
func (b *LXDBackend) GetVNCInfo(id string) (compute.GraphicsInfo, error) {
	return compute.GraphicsInfo{}, compute.ErrNotImplemented
}
func (b *LXDBackend) GetDomainIP(id string) string { return "" }
func (b *LXDBackend) GetDomainXML(id string) (string, error) {
	return "", compute.ErrNotImplemented
}
func (b *LXDBackend) GuestGetClipboard(id string) (string, error) {
	return "", compute.ErrNotImplemented
}
func (b *LXDBackend) GuestSetClipboard(id, text string) error { return compute.ErrNotImplemented }

// --- Backup / export / OVA / import ---

func (b *LXDBackend) ExportDomain(ctx context.Context, id string, opts compute.ExportBackupOptions, w io.Writer) (backupstore.ProducerResult, error) {
	return backupstore.ProducerResult{}, compute.ErrNotImplemented
}
func (b *LXDBackend) ExportDomainOVA(ctx context.Context, id string, opts compute.OVAOptions, w io.Writer) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) EstimateExportSize(ctx context.Context, id string, compress bool) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *LXDBackend) EstimateOVASize(ctx context.Context, id string, target compute.OVATarget) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *LXDBackend) ImportDomain(tarPath, newName, poolName string, opts compute.ImportOpts) (string, string, []string, error) {
	return "", "", nil, compute.ErrNotImplemented
}
func (b *LXDBackend) ImportOVA(ovaPath, newName, poolName string) (string, string, error) {
	return "", "", compute.ErrNotImplemented
}

// Capabilities reports what LXD supports (Fase 1: read-only listing).
func (b *LXDBackend) Capabilities() compute.Capabilities {
	return compute.Capabilities{}
}

// --- helpers ---

// instanceToVM maps an LXD instance to the neutral domain model. Pure
// and unit-tested (the fake-server integration test feeds real payloads).
func instanceToVM(i *api.Instance) models.VM {
	vm := models.VM{
		ID:         i.Name,
		Name:       i.Name,
		Type:       "container",
		Hypervisor: "lxd",
		State:      lxdState(i.Status),
		VCPUs:      parseIntConfig(i.Config["limits.cpu"]),
		RAMMB:      parseMemoryMB(i.Config["limits.memory"]),
		Autostart:  i.Config["boot.autostart"] == "true",
	}
	if i.Type == string(api.InstanceTypeVM) {
		vm.Type = "vm"
	}
	return vm
}

// lxdState maps the LXD status string to the neutral VMState.
func lxdState(status string) models.VMState {
	switch strings.ToLower(status) {
	case "running":
		return models.VMStateRunning
	case "stopped":
		return models.VMStateShutoff
	case "frozen":
		return models.VMStatePaused
	case "error":
		return models.VMStateCrashed
	default:
		return models.VMStateUnknown
	}
}

// parseIntConfig parses an integer config value ("2", "4"), returning 0
// for ranges/percentages we cannot map to a plain count.
func parseIntConfig(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if !strings.ContainsAny(v, "-%") {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// parseMemoryMB parses an LXD memory limit ("4GB", "512MiB", "1GiB")
// into MB. Returns 0 when unparseable.
func parseMemoryMB(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	mult := int64(1)
	num := v
	upper := strings.ToUpper(v)
	switch {
	case strings.HasSuffix(upper, "GIB"):
		mult, num = 1024, strings.TrimSuffix(v, "GiB")
	case strings.HasSuffix(upper, "GB"):
		mult, num = 1000, strings.TrimSuffix(v, "GB")
	case strings.HasSuffix(upper, "MIB"):
		mult, num = 1, strings.TrimSuffix(v, "MiB")
	case strings.HasSuffix(upper, "MB"):
		mult, num = 1, strings.TrimSuffix(v, "MB")
	case strings.HasSuffix(upper, "KIB"):
		mult, num = 0, strings.TrimSuffix(v, "KiB")
	case strings.HasSuffix(upper, "KB"):
		mult, num = 0, strings.TrimSuffix(v, "KB")
	}
	if mult == 0 { // sub-MB value
		return 1
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil {
		return 0
	}
	return int64(n * float64(mult))
}

// isNotFound reports whether an LXD error is a "not found" (instance
// absent), so DomainExists can distinguish it from real failures.
func isNotFound(err error) bool {
	var statusErr api.StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Status() == httpNotFound
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// httpNotFound mirrors net/http.StatusNotFound to avoid importing net/http.
const httpNotFound = 404

// mapLXErr translates LXD daemon errors to the neutral compute sentinels
// where a meaningful mapping exists; anything else passes through.
func mapLXErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "not running") ||
		strings.Contains(strings.ToLower(err.Error()), "instance is not running") {
		return compute.ErrDomainNotRunning
	}
	if isNotFound(err) {
		return err
	}
	return err
}

// lxdCloudInitConfig builds the native LXD instance config keys from a
// cloud-init request (v1.4 Fase 2). Unlike KVM (NoCloud seed ISO), LXD
// accepts cloud-init directly as instance config: user.user-data carries
// the #cloud-config document and user.network-config the network YAML.
// networkBridge is the LXD managed bridge to attach by default (empty =
// no network-config block).
func lxdCloudInitConfig(cfg cloudinit.Config, networkBridge string) (map[string]string, bool) {
	out := map[string]string{}
	if cfg.User == "" && cfg.ProvisionScript == "" && cfg.Hostname == "" {
		return nil, false // nothing to provision
	}
	userData := cloudinit.BuildUserData(cfg)
	if userData != "" {
		out["user.user-data"] = userData
	}
	if networkBridge != "" {
		out["user.network-config"] = "network:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: true\n"
	}
	return out, true
}
