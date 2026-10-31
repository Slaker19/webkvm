package incus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/backupstore"
	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// --- Snapshots ---

func (b *IncusBackend) ListSnapshots(domainID string) ([]models.Snapshot, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) CreateSnapshot(domainID string, req models.CreateSnapshotRequest) (models.Snapshot, error) {
	return models.Snapshot{}, compute.ErrNotImplemented
}
func (b *IncusBackend) DeleteSnapshot(domainID, snapID string) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *IncusBackend) RevertSnapshot(domainID, snapID string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) ExportSnapshots(domainID string) ([]backupstore.SnapshotBackup, error) {
	return nil, compute.ErrNotImplemented
}

// --- Networking ---

func (b *IncusBackend) ListNetworks() ([]models.Network, error) {
	if b.client == nil {
		return nil, compute.ErrNotImplemented
	}
	nets, err := b.client.GetNetworks()
	if err != nil {
		return nil, err
	}
	var result []models.Network
	for _, n := range nets {
		if n.Type != "bridge" {
			continue
		}
		kind := "isolated"
		if n.Config["ipv4.nat"] == "true" {
			kind = "nat"
		}
		cidr := n.Config["ipv4.address"]
		dhcp := n.Config["ipv4.dhcp"] != "false" && cidr != "" && cidr != "none"
		result = append(result, models.Network{
			Name:      n.Name,
			Kind:      kind,
			Forward:   kind,
			Bridge:    n.Name,
			CIDR:      cidr,
			DHCP:      dhcp,
			Active:    n.Status == "Created" || n.Status == "",
			Autostart: true,
		})
	}
	return result, nil
}
func (b *IncusBackend) CreateNetwork(req models.CreateNetworkRequest) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *IncusBackend) UpdateNetwork(name string, req models.UpdateNetworkRequest) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *IncusBackend) DeleteNetwork(id string) error { return compute.ErrNotImplemented }
func (b *IncusBackend) StartNetwork(name string) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *IncusBackend) StopNetwork(name string) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *IncusBackend) CheckVLANSupport(networkName string) (models.VlanSupport, error) {
	return models.VlanSupport{}, compute.ErrNotImplemented
}
func (b *IncusBackend) NetworkLeases(name string) ([]models.DHCPLease, error) {
	if b.client == nil {
		return nil, compute.ErrNotImplemented
	}
	leases, err := b.client.GetNetworkLeases(name)
	if err != nil {
		return nil, err
	}
	var result []models.DHCPLease
	for _, l := range leases {
		result = append(result, models.DHCPLease{
			IP:       l.Address,
			MAC:      l.Hwaddr,
			Hostname: l.Hostname,
		})
	}
	return result, nil
}
func (b *IncusBackend) ReleaseNetworkLease(br, ip, mac string) error {
	return compute.ErrNotImplemented
}

// --- Console / Metadata ---

type lxdConsoleStream struct {
	op     incus.Operation
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	done   chan bool
	ctrlMu sync.Mutex
	ctrl   *websocket.Conn // window-resize/signal channel
	closed atomic.Bool
}

func (s *lxdConsoleStream) Recv(buf []byte) (int, error) { return s.stdout.Read(buf) }
func (s *lxdConsoleStream) Send(b []byte) (int, error) {
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
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
	}
}

func (b *IncusBackend) OpenSerialConsole(id string) (compute.ConsoleStream, error) {
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

	post := api.InstanceExecPost{
		Command:     []string{"bash"},
		Interactive: true,
		WaitForWS:   true,
		Environment: map[string]string{"TERM": "xterm-256color"},
		Width:       120,
		Height:      30,
	}

	args := incus.InstanceExecArgs{
		Stdin:    stdinR,
		Stdout:   stdoutW,
		Control:  stream.setControl,
		DataDone: done,
	}

	op, err := b.client.ExecInstance(id, post, &args)
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutW.Close()
		_ = stdoutR.Close()
		return nil, mapLXErr(err)
	}
	stream.op = op
	return stream, nil
}

func (b *IncusBackend) SetUserPassword(id, user, password string) error {
	return compute.ErrNotImplemented
}

const (
	metaTagsKey = "user.webkvm.tags"
	metaDescKey = "user.webkvm.desc"
)

type lxdWebKVMMeta struct {
	Alias     string   `json:"alias,omitempty"`
	Notes     string   `json:"notes,omitempty"`
	Cover     string   `json:"cover,omitempty"`
	Groups    []string `json:"groups,omitempty"`
	OwnerID   string   `json:"owner_id,omitempty"`
	Template  bool     `json:"template"`
	Shared    bool     `json:"shared,omitempty"`
	CiUser    string   `json:"ci_user,omitempty"`
	AppInfo   string   `json:"app_info,omitempty"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
}

func (b *IncusBackend) GetVMMeta(uuid string) (models.VMMeta, error) {
	inst, _, err := b.client.GetInstance(uuid)
	if err != nil {
		return models.VMMeta{}, err
	}
	return lxdMetaToModel(inst.Config), nil
}

func lxdMetaToModel(config map[string]string) models.VMMeta {
	meta := models.VMMeta{}
	if raw, ok := config[metaTagsKey]; ok && raw != "" {
		meta.Tags = strings.Split(raw, ",")
	}
	if raw, ok := config[metaDescKey]; ok && raw != "" {
		var d lxdWebKVMMeta
		if err := json.Unmarshal([]byte(raw), &d); err == nil {
			meta.Alias, meta.Notes, meta.Cover = d.Alias, d.Notes, d.Cover
			meta.Groups, meta.OwnerID = d.Groups, d.OwnerID
			meta.Template, meta.CiUser = d.Template, d.CiUser
			meta.Shared = d.Shared
			meta.AppInfo, meta.UpdatedAt = d.AppInfo, d.UpdatedAt
		}
	}
	return meta
}

func applyMetaToConfig(config map[string]string, meta models.VMMeta) {
	if len(meta.Tags) == 0 {
		delete(config, metaTagsKey)
	} else {
		config[metaTagsKey] = strings.Join(meta.Tags, ",")
	}
	d := lxdWebKVMMeta{
		Alias: meta.Alias, Notes: meta.Notes, Cover: meta.Cover,
		Groups: meta.Groups, OwnerID: meta.OwnerID, Template: meta.Template,
		Shared: meta.Shared, CiUser: meta.CiUser, AppInfo: meta.AppInfo, UpdatedAt: meta.UpdatedAt,
	}
	if d.Alias == "" && d.Notes == "" && d.Cover == "" && len(d.Groups) == 0 &&
		d.OwnerID == "" && !d.Template && !d.Shared && d.CiUser == "" && d.AppInfo == "" && d.UpdatedAt == 0 {
		delete(config, metaDescKey)
		return
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return
	}
	config[metaDescKey] = string(raw)
}

func (b *IncusBackend) SetVMMeta(uuid string, meta models.VMMeta) error {
	return b.setMetaConfig(uuid, func(cfg map[string]string) { applyMetaToConfig(cfg, meta) })
}

func (b *IncusBackend) UpdateVMMeta(uuid string, upd models.VMMetaUpdate) (models.VMMeta, error) {
	inst, etag, err := b.client.GetInstance(uuid)
	if err != nil {
		return models.VMMeta{}, err
	}
	current := lxdMetaToModel(inst.Config)
	if upd.Alias != nil {
		current.Alias = *upd.Alias
	}
	if upd.Notes != nil {
		current.Notes = *upd.Notes
	}
	if upd.Cover != nil {
		current.Cover = *upd.Cover
	}
	if upd.Groups != nil {
		if *upd.Groups == nil {
			current.Groups = nil
		} else {
			current.Groups = *upd.Groups
		}
	}
	if upd.Tags != nil {
		if *upd.Tags == nil {
			current.Tags = nil
		} else {
			current.Tags = *upd.Tags
		}
	}
	if upd.OwnerID != nil {
		current.OwnerID = *upd.OwnerID
	}
	if upd.Template != nil {
		current.Template = *upd.Template
	}
	if upd.Shared != nil {
		current.Shared = *upd.Shared
	}
	if upd.CiUser != nil {
		current.CiUser = *upd.CiUser
	}
	if upd.AppInfo != nil {
		current.AppInfo = *upd.AppInfo
	}
	current.UpdatedAt = time.Now().Unix()
	if err := b.setMetaConfig(uuid, func(cfg map[string]string) { applyMetaToConfig(cfg, current) }, etag); err != nil {
		return models.VMMeta{}, err
	}
	return current, nil
}

func (b *IncusBackend) setMetaConfig(uuid string, fn func(map[string]string), etag ...string) error {
	inst, curETag, err := b.client.GetInstance(uuid)
	if err != nil {
		return err
	}
	cfg := inst.Config
	if cfg == nil {
		cfg = map[string]string{}
	}
	fn(cfg)
	if len(etag) > 0 {
		curETag = etag[0]
	}
	op, err := b.client.UpdateInstance(uuid, api.InstancePut{
		Config:      cfg,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, curETag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) GetVNCInfo(id string) (compute.GraphicsInfo, error) {
	return compute.GraphicsInfo{}, compute.ErrNotImplemented
}
func (b *IncusBackend) GetSPICEInfo(id string) (compute.GraphicsInfo, error) {
	return compute.GraphicsInfo{}, compute.ErrNotImplemented
}
func (b *IncusBackend) GetDomainIP(id string) string { return "" }
func (b *IncusBackend) GetDomainXML(id string) (string, error) {
	return "", compute.ErrNotImplemented
}
func (b *IncusBackend) GuestGetClipboard(id string) (string, error) {
	return "", compute.ErrNotImplemented
}
func (b *IncusBackend) GuestSetClipboard(id, text string) error { return compute.ErrNotImplemented }

func (b *IncusBackend) GetGuestInfo(id string) (compute.GuestInfo, error) {
	return compute.GuestInfo{}, compute.ErrNotImplemented
}
func (b *IncusBackend) FSTrim(id string) (compute.GuestFSTrimResult, error) {
	return compute.GuestFSTrimResult{}, compute.ErrNotImplemented
}
func (b *IncusBackend) GuestExec(id, path string, args []string, timeoutSec int) (compute.GuestExecResult, error) {
	return compute.GuestExecResult{}, compute.ErrNotImplemented
}
func (b *IncusBackend) FSFreeze(id string, freeze bool) (int, error) {
	return 0, compute.ErrNotImplemented
}
func (b *IncusBackend) FSFreezeStatus(id string) (string, error) {
	return "", compute.ErrNotImplemented
}
func (b *IncusBackend) GuestSyncTime(id string) error {
	return compute.ErrNotImplemented
}

// --- Backup / Export / Import ---

func (b *IncusBackend) ExportDomain(ctx context.Context, id string, opts compute.ExportBackupOptions, w io.Writer) (backupstore.ProducerResult, error) {
	backupName := fmt.Sprintf("webkvm-export-%d", time.Now().UnixNano())
	op, err := b.client.CreateInstanceBackup(id, api.InstanceBackupsPost{Name: backupName})
	if err != nil {
		return backupstore.ProducerResult{}, err
	}
	if err := waitOperationLong(op); err != nil {
		if dop, derr := b.client.DeleteInstanceBackup(id, backupName); derr == nil {
			_ = waitOperation(dop)
		}
		return backupstore.ProducerResult{}, err
	}
	defer func() {
		if dop, derr := b.client.DeleteInstanceBackup(id, backupName); derr == nil {
			_ = waitOperation(dop)
		}
	}()

	body, err := b.exportStream(ctx, id, backupName)
	if err != nil {
		return backupstore.ProducerResult{}, err
	}
	defer body.Close()

	var out io.Writer = w
	var zw io.Closer
	if opts.Compress == "zstd" {
		level := opts.ZstdLevel
		if level < 1 || level > 22 {
			level = 19
		}
		enc, zerr := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)))
		if zerr != nil {
			return backupstore.ProducerResult{}, zerr
		}
		zw = enc
		out = enc
	}
	n, err := io.Copy(out, body)
	if err != nil {
		return backupstore.ProducerResult{}, err
	}
	if zw != nil {
		if cerr := zw.Close(); cerr != nil {
			return backupstore.ProducerResult{}, cerr
		}
	}
	return backupstore.ProducerResult{DisksIncluded: 1, TotalBytes: n}, nil
}

func (b *IncusBackend) exportStream(ctx context.Context, id, backup string) (io.ReadCloser, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", b.socketPath)
		},
	}
	client := &http.Client{Transport: transport}
	u := fmt.Sprintf("http://unix.socket/1.0/instances/%s/backups/%s/export", url.PathEscape(id), url.PathEscape(backup))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("LXD export HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp.Body, nil
}

func (b *IncusBackend) ExportDomainOVA(ctx context.Context, id string, opts compute.OVAOptions, w io.Writer) error {
	return compute.ErrNotImplemented
}

func (b *IncusBackend) EstimateExportSize(ctx context.Context, id string, compress bool) (int64, error) {
	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return 0, err
	}
	if root, ok := inst.Devices["root"]; ok {
		if gb := parseDiskSizeGB(root["size"]); gb > 0 {
			return gb * int64(1<<30), nil
		}
	}
	return 0, nil
}

func (b *IncusBackend) EstimateOVASize(ctx context.Context, id string, target compute.OVATarget) (int64, error) {
	return 0, compute.ErrNotImplemented
}

func (b *IncusBackend) ImportDomain(tarPath, newName, poolName string, opts compute.ImportOpts) (string, string, []string, error) {
	if b.client == nil {
		return "", "", nil, errors.New("incus client not initialized")
	}
	f, err := os.Open(tarPath)
	if err != nil {
		return "", "", nil, fmt.Errorf("open backup file: %w", err)
	}
	defer f.Close()

	if opts.OnProgress != nil {
		opts.OnProgress(10, "restore_copying", map[string]any{"file": filepath.Base(tarPath)})
	}

	args := incus.InstanceBackupArgs{
		BackupFile: f,
		Name:       newName,
		PoolName:   poolName,
	}
	if poolName == "" || poolName == "webkvm-disks" {
		args.PoolName = ""
	}

	op, err := b.client.CreateInstanceFromBackup(args)
	if err != nil {
		return "", "", nil, fmt.Errorf("create instance from backup: %w", err)
	}
	if err := waitOperationLong(op); err != nil {
		return "", "", nil, fmt.Errorf("import backup: %w", err)
	}

	resolvedName := newName
	if resolvedName != "" {
		inst, curETag, gerr := b.client.GetInstance(resolvedName)
		if gerr == nil {
			changed := false
			for k := range inst.Config {
				if strings.HasPrefix(k, "volatile.eth") || strings.HasPrefix(k, "volatile.last_state") {
					delete(inst.Config, k)
					changed = true
				}
			}
			if opts.Autostart != nil {
				if *opts.Autostart {
					inst.Config["boot.autostart"] = "true"
				} else {
					inst.Config["boot.autostart"] = "false"
				}
				changed = true
			}
			if opts.Network != "" {
				if inst.Devices == nil {
					inst.Devices = map[string]map[string]string{}
				}
				eth0 := inst.Devices["eth0"]
				if eth0 == nil {
					eth0 = map[string]string{"type": "nic", "nictype": "bridged"}
				}
				eth0["parent"] = opts.Network
				eth0["network"] = opts.Network
				inst.Devices["eth0"] = eth0
				changed = true
			}
			if changed {
				put := inst.Writable()
				put.Devices = inst.Devices
				if uop, uerr := b.client.UpdateInstance(resolvedName, put, curETag); uerr == nil {
					_ = waitOperation(uop)
				} else {
					slog.Warn("import: failed to update instance network", "name", resolvedName, "err", uerr)
				}
			}
		}
	}

	if opts.OnProgress != nil {
		opts.OnProgress(100, "done", nil)
	}

	return resolvedName, resolvedName, nil, nil
}

func (b *IncusBackend) ImportOVA(ovaPath, newName, poolName string) (string, string, error) {
	return "", "", compute.ErrNotImplemented
}

func (b *IncusBackend) Capabilities() compute.Capabilities {
	return compute.Capabilities{
		SupportsSerialConsole: true,
	}
}
