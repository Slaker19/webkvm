package ha

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webkvm/internal/sysutil"
)

// Maintenance TTL bounds.
const (
	MinMaintenanceTTL = time.Minute
	MaxMaintenanceTTL = 7 * 24 * time.Hour
)

// MaintOptions tunes Begin/EndMaintenance.
type MaintOptions struct {
	Now          func() time.Time
	Timeout      time.Duration
	PollInterval time.Duration
	CPGroup      string
}

func (o MaintOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// MaintResult is the outcome of a maintenance call.
type MaintResult struct {
	Changed bool
	Until   string
	State   string
}

// BeginMaintenance unmanages the workload so an operator can touch it by
// hand (edit the XML, run virsh/incus) without Pacemaker reacting. The
// previous state and the expiry are persisted as meta on the VM primitive.
// WebKVM never re-manages on expiry: readiness raises
// workload_unmanaged_expired and the operator ends the window.
func BeginMaintenance(ctx context.Context, runner sysutil.CommandRunner, key WorkloadKey, ttl time.Duration, opts MaintOptions) (MaintResult, error) {
	if ttl < MinMaintenanceTTL || ttl > MaxMaintenanceTTL {
		return MaintResult{}, opErr(CodeInvalid, "ttl must be between %s and %s", MinMaintenanceTTL, MaxMaintenanceTTL)
	}
	unlock, ok := LockKey(key.String())
	if !ok {
		return MaintResult{}, opErr(CodeBusy, "another operation on %s is running", key)
	}
	defer unlock()
	w, err := resolve(ctx, runner, key, opts.CPGroup)
	if err != nil {
		return MaintResult{}, err
	}
	if !w.Managed && w.MaintUntil != "" {
		return MaintResult{Until: w.MaintUntil, State: w.State}, nil // already in maintenance
	}
	until := opts.now().UTC().Add(ttl).Format(time.RFC3339)
	prev := w.State
	if prev == WorkloadUnmanaged {
		prev = WorkloadStarted
	}
	if out, err := runPCS(ctx, runner, 30*time.Second, "resource", "unmanage", w.TopResource()); err != nil {
		return MaintResult{}, opErr(CodePCSFailed, "pcs resource unmanage: %s", tailOf(out, err))
	}
	if out, err := runPCS(ctx, runner, 30*time.Second, "resource", "meta", w.ResourceID, MetaMaintUntil+"="+until, MetaMaintPrev+"="+prev); err != nil {
		// Do not leave a half-applied window: re-manage.
		_, _ = runPCS(ctx, runner, 30*time.Second, "resource", "manage", w.TopResource())
		return MaintResult{}, opErr(CodePCSFailed, "pcs resource meta: %s", tailOf(out, err))
	}
	return MaintResult{Changed: true, Until: until, State: WorkloadUnmanaged}, nil
}

// EndMaintenance re-manages the workload, refreshes it and verifies it
// reaches Started when it was started before the window.
func EndMaintenance(ctx context.Context, runner sysutil.CommandRunner, key WorkloadKey, opts MaintOptions) (MaintResult, error) {
	unlock, ok := LockKey(key.String())
	if !ok {
		return MaintResult{}, opErr(CodeBusy, "another operation on %s is running", key)
	}
	defer unlock()
	w, err := resolve(ctx, runner, key, opts.CPGroup)
	if err != nil {
		return MaintResult{}, err
	}
	if w.Managed && w.MaintUntil == "" {
		return MaintResult{State: w.State}, nil // not in maintenance
	}
	prevStarted := true
	if cib, _, rerr := readCIBAndStatus(ctx, runner); rerr == nil {
		prevStarted = !bytes.Contains(cib, []byte(`name="`+MetaMaintPrev+`" value="`+WorkloadPaused+`"`)) &&
			!bytes.Contains(cib, []byte(`name="`+MetaMaintPrev+`" value="`+WorkloadStopped+`"`))
	}
	top := w.TopResource()
	if out, err := runPCS(ctx, runner, 30*time.Second, "resource", "manage", top); err != nil {
		return MaintResult{}, opErr(CodePCSFailed, "pcs resource manage: %s", tailOf(out, err))
	}
	if out, err := runPCS(ctx, runner, 30*time.Second, "resource", "meta", w.ResourceID, MetaMaintUntil+"=", MetaMaintPrev+"="); err != nil {
		return MaintResult{}, opErr(CodePCSFailed, "pcs resource meta: %s", tailOf(out, err))
	}
	if out, err := runPCS(ctx, runner, 60*time.Second, "resource", "refresh", top); err != nil {
		return MaintResult{}, opErr(CodePCSFailed, "pcs resource refresh: %s", tailOf(out, err))
	}
	if !prevStarted {
		return MaintResult{Changed: true, State: WorkloadStopped}, nil
	}
	timeout, interval := opts.Timeout, opts.PollInterval
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	last, started, werr := waitWorkload(ctx, runner, key, opts.CPGroup, timeout, interval,
		func(w Workload) bool { return w.State == WorkloadStarted })
	if werr != nil {
		return MaintResult{Changed: true, State: last.State}, werr
	}
	if !started {
		return MaintResult{Changed: true, State: last.State}, opErr(CodeStartTimeout, "%s did not reach Started after maintenance", key)
	}
	return MaintResult{Changed: true, State: last.State}, nil
}

// ExpiredMaintenance returns the workloads whose maintenance window expired.
func ExpiredMaintenance(list []Workload, now time.Time) []Workload {
	var out []Workload
	for _, w := range list {
		if !w.Managed && w.MaintenanceExpired(now) {
			out = append(out, w)
		}
	}
	return out
}

// XMLBackupPath is the backup name used before a safe XML edit.
func XMLBackupPath(xmlPath string, ts time.Time) string {
	return xmlPath + ".webkvm-bak-" + ts.UTC().Format("20060102T150405Z")
}

// ValidateDomainXML checks that data is a libvirt domain document for the
// expected VM name. The VM is transient under Pacemaker, so the XML file is
// the only definition: never push it with defineXML.
func ValidateDomainXML(data []byte, wantName string) error {
	var d struct {
		XMLName xml.Name `xml:"domain"`
		Type    string   `xml:"type,attr"`
		Name    string   `xml:"name"`
	}
	if err := xml.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("invalid domain XML: %w", err)
	}
	if d.XMLName.Local != "domain" {
		return errors.New("root element must be <domain>")
	}
	if strings.TrimSpace(d.Name) == "" {
		return errors.New("domain has no <name>")
	}
	if wantName != "" && strings.TrimSpace(d.Name) != wantName {
		return fmt.Errorf("domain name %q does not match %q", strings.TrimSpace(d.Name), wantName)
	}
	return nil
}

// SafeEditXML backs up xmlPath, validates newData, atomically replaces the
// file, then calls start. If start fails the backup is restored and the start
// error is returned. The backup path is returned in every case where it was
// created.
func SafeEditXML(xmlPath string, newData []byte, wantName string, now time.Time, start func() error) (backup string, err error) {
	if err := ValidateDomainXML(newData, wantName); err != nil {
		return "", err
	}
	old, err := os.ReadFile(xmlPath)
	if err != nil {
		return "", err
	}
	backup = XMLBackupPath(xmlPath, now)
	if err := os.WriteFile(backup, old, 0o600); err != nil {
		return "", err
	}
	if err := WriteFileAtomic(xmlPath, newData, 0o600); err != nil {
		return backup, err
	}
	if start == nil {
		return backup, nil
	}
	if serr := start(); serr != nil {
		if rerr := RestoreXML(xmlPath, backup); rerr != nil {
			return backup, fmt.Errorf("start failed (%v) and restore failed: %w", serr, rerr)
		}
		return backup, fmt.Errorf("start failed, previous XML restored: %w", serr)
	}
	return backup, nil
}

// RestoreXML puts a backup back in place atomically.
func RestoreXML(xmlPath, backup string) error {
	if filepath.Dir(xmlPath) != filepath.Dir(backup) || !strings.HasPrefix(filepath.Base(backup), filepath.Base(xmlPath)+".webkvm-bak-") {
		return errors.New("backup does not belong to this XML file")
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		return err
	}
	return WriteFileAtomic(xmlPath, data, 0o600)
}
