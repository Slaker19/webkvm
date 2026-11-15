package ha

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webkvm/internal/sysutil"
)

// DefaultLibvirtDefinitionDir holds libvirt's persistent qemu definitions.
const DefaultLibvirtDefinitionDir = "/etc/libvirt/qemu"

// RestoreOptions tunes RestoreReleased.
type RestoreOptions struct {
	// Start boots the VM after defining it.
	Start bool
	// TmpDir receives the transient XML file handed to `virsh define`
	// (DATA_DIR in production). It must be an absolute directory that is the
	// same path in the host mount namespace (not /tmp under PrivateTmp).
	TmpDir string
	// ConnectWait bounds the wait for the DRBD peers to connect and the
	// local disk to become UpToDate after `drbdadm up`.
	ConnectWait time.Duration
	Poll        time.Duration
	// ReadFile reads the definition on the replicated filesystem once it is
	// mounted (nil: only the saved XML is used).
	ReadFile func(path string) ([]byte, error)
	// ResolveDevice maps a DRBD device path to its real node (by-res links).
	ResolveDevice func(path string) (string, error)
	// HostRunner runs the steps whose effect must be visible to the host
	// (findmnt, mkdir, mount, umount, the definition file and its symlink)
	// in the HOST mount namespace (sysutil.HostNamespaceRunner in
	// production). nil: the plain runner (tests, hosts without systemd).
	HostRunner sysutil.CommandRunner
	// DefinitionDir is libvirt's persistent definition directory
	// ("" = /etc/libvirt/qemu).
	DefinitionDir string
}

// RestoreResult is the outcome of RestoreReleased.
type RestoreResult struct {
	Changed    bool     `json:"changed"`
	State      string   `json:"state"`
	UUID       string   `json:"uuid,omitempty"`
	XMLSource  string   `json:"xml_source,omitempty"`
	XMLDiffers bool     `json:"xml_differs"`
	XMLLinked  bool     `json:"xml_linked"`
	Steps      []string `json:"steps"`
}

// DRBDPeerDown is one DRBD peer that is not connected, with the command the
// operator must run on that node (there is no remote exec channel).
type DRBDPeerDown struct {
	Node       string `json:"node"`
	Connection string `json:"connection"`
	Command    string `json:"command"`
}

const virshURI = "qemu:///system"

func virsh(ctx context.Context, runner sysutil.CommandRunner, timeout time.Duration, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runner.Run(c, "virsh", append([]string{"-c", virshURI}, args...)...)
}

func runFor(ctx context.Context, runner sysutil.CommandRunner, timeout time.Duration, name string, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runner.Run(c, name, args...)
}

func domainMissing(out []byte, err error) bool {
	text := strings.ToLower(tailOf(out, err))
	return strings.Contains(text, "failed to get domain") || strings.Contains(text, "domain not found") ||
		strings.Contains(text, "no domain with matching")
}

// localDRBD is the local view of one DRBD resource.
type localDRBD struct {
	up, primary, upToDate, allConnected bool
	quorum                              string
	primaryPeers                        []string
	peers                               []drbdPeer
}

// quorumLost reports whether promoting here cannot (or must not) succeed
// because too few replicas are connected: DRBD said quorum:no, or it did not
// report quorum and this node plus its connected peers are not a majority.
func (d localDRBD) quorumLost() bool {
	switch d.quorum {
	case "no":
		return true
	case "yes":
		return false
	}
	connected := 1
	for _, p := range d.peers {
		if p.conn == "Connected" {
			connected++
		}
	}
	return connected*2 <= 1+len(d.peers)
}

// downPeers lists the peers that are not connected, each with the command
// to run on it. Names that fail validation get no command.
func (d localDRBD) downPeers(res string) []DRBDPeerDown {
	out := []DRBDPeerDown{}
	for _, p := range d.peers {
		if p.conn == "Connected" {
			continue
		}
		conn := p.conn
		if conn == "" {
			conn = "unknown"
		}
		pd := DRBDPeerDown{Node: p.name, Connection: conn}
		if drbdNameRe.MatchString(p.name) {
			pd.Command = "drbdadm up " + res
		}
		out = append(out, pd)
	}
	return out
}

func readLocalDRBD(ctx context.Context, runner sysutil.CommandRunner, res string) localDRBD {
	out, err := runFor(ctx, runner, 10*time.Second, "drbdsetup", "status", res, "--verbose")
	if err != nil {
		return localDRBD{}
	}
	var st localDRBD
	for _, r := range parseDRBDStatus(string(out)) {
		if r.name != res {
			continue
		}
		st.up = true
		st.primary = r.role == "Primary"
		st.upToDate = r.disk == "UpToDate"
		st.quorum = r.quorum
		st.allConnected = true
		for _, p := range r.peers {
			st.peers = append(st.peers, *p)
			if p.conn != "Connected" {
				st.allConnected = false
			}
			if p.role == "Primary" {
				st.primaryPeers = append(st.primaryPeers, p.name)
			}
		}
	}
	return st
}

// ReadDRBDPeers returns the peer node names of a DRBD resource from the
// read-only `drbdsetup status` of this node (nil when it is not up here).
func ReadDRBDPeers(ctx context.Context, runner sysutil.CommandRunner, res string) []string {
	if !drbdResourceRE.MatchString(res) {
		return nil
	}
	var out []string
	for _, p := range readLocalDRBD(ctx, runner, res).peers {
		if drbdNameRe.MatchString(p.name) {
			out = append(out, p.name)
		}
	}
	return out
}

// hostMountSource returns the source mounted on dir as seen by the HOST.
func hostMountSource(ctx context.Context, host sysutil.CommandRunner, dir string) string {
	mo, err := runFor(ctx, host, 10*time.Second, "findmnt", "-rn", "-o", "SOURCE", "--mountpoint", dir)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(mo))
}

// definitionPath is where the domain XML lives on the replicated filesystem:
// the VirtualDomain config= of the removed stack when it is on the mount,
// else <mount>/<vm>.xml.
func definitionPath(st KVMStack, name string) string {
	if ValidReleasePath(st.ConfigPath) && strings.HasPrefix(st.ConfigPath, st.MountDir+"/") {
		return st.ConfigPath
	}
	return filepath.Join(st.MountDir, name+".xml")
}

// RestoreReleased brings a released KVM VM back on THIS node: DRBD up and
// promoted, filesystem mounted (in the host mount namespace), saved domain
// defined, its definition linked from /etc/libvirt/qemu to the replicated
// filesystem (what HA enable / VirtualDomain config= expect), optionally
// started. Every step that fails rolls back the steps this call performed,
// in reverse order; nothing ever deletes data. A VM already defined with
// the saved UUID whose mount is visible to the host, whose DRBD resource is
// Primary here and whose definition is linked is a no-op (Changed false);
// otherwise the missing pieces are repaired.
//
// The caller holds the workload lock and has checked RestoreConflicts.
func RestoreReleased(ctx context.Context, runner sysutil.CommandRunner, e ReleasedVM, opts RestoreOptions) (RestoreResult, error) {
	res := RestoreResult{Steps: []string{}}
	if !WorkloadNameRE.MatchString(e.Name) {
		return res, opErr(CodeInvalid, "invalid VM name")
	}
	if e.XML == "" || e.Unrestorable {
		return res, opErr(CodeReleaseUnrestorable, "no domain XML was saved for %s; restore it by hand (runbook)", e.Name)
	}
	if _, err := CheckDomainXML([]byte(e.XML), e.Name, e.UUID); err != nil {
		return res, err
	}
	st := e.Stack
	storage := st.DRBDResource != ""
	if storage {
		if !drbdResourceRE.MatchString(st.DRBDResource) || !drbdDeviceRE.MatchString(st.Device) || !ValidReleasePath(st.MountDir) {
			return res, opErr(CodeReleaseXMLInvalid, "the saved storage parameters are not valid")
		}
		if st.FSType != "" && st.FSType != "ext4" && st.FSType != "xfs" {
			return res, opErr(CodeReleaseXMLInvalid, "unsupported filesystem type")
		}
	}
	if !filepath.IsAbs(opts.TmpDir) {
		return res, opErr(CodeInvalid, "no temporary directory for the domain XML")
	}
	host := opts.HostRunner
	if host == nil {
		host = runner
	}
	defDir := opts.DefinitionDir
	if defDir == "" {
		defDir = DefaultLibvirtDefinitionDir
	}
	wait := opts.ConnectWait
	if wait <= 0 {
		wait = 15 * time.Second
	}
	poll := opts.Poll
	if poll <= 0 {
		poll = time.Second
	}

	// Already defined? Same UUID = repair/no-op; another domain with that
	// name = conflict, never overwritten.
	alreadyDefined := false
	out, err := virsh(ctx, runner, 15*time.Second, "domuuid", e.Name)
	switch {
	case err == nil:
		got := strings.TrimSpace(string(out))
		if e.UUID != "" && normUUID(got) != normUUID(e.UUID) {
			return res, opErr(CodeDomainConflict, "a different domain named %s already exists on this node; nothing was changed", e.Name)
		}
		alreadyDefined = true
		res.UUID = got
		if !storage {
			res.State = "defined"
			return res, nil
		}
	case !domainMissing(out, err):
		return res, opErr(CodeDefineFailed, "cannot query libvirt: %s", tailOf(out, err))
	}

	cfgPath := definitionPath(st, e.Name)
	cfgNew := cfgPath + ".webkvm-new"
	defLink := filepath.Join(defDir, e.Name+".xml")
	var didUp, didPrimary, didMount, didDefine, didWriteTmp, didWriteXML bool
	rollback := func(cause *OpError) error {
		var left []string
		if didDefine {
			if o, err := virsh(ctx, runner, 30*time.Second, "undefine", "--keep-nvram", e.Name); err != nil {
				left = append(left, "domain "+e.Name+" defined ("+tailOf(o, err)+")")
			}
		}
		if didWriteTmp {
			if o, err := runFor(ctx, host, 30*time.Second, "rm", "-f", "--", cfgNew); err != nil {
				left = append(left, cfgNew+" ("+tailOf(o, err)+")")
			}
		}
		if didWriteXML {
			if o, err := runFor(ctx, host, 30*time.Second, "rm", "-f", "--", cfgPath); err != nil {
				left = append(left, cfgPath+" written ("+tailOf(o, err)+")")
			}
		}
		if didMount {
			if o, err := runFor(ctx, host, 60*time.Second, "umount", st.MountDir); err != nil {
				left = append(left, st.MountDir+" mounted ("+tailOf(o, err)+")")
			}
		}
		if didPrimary {
			if o, err := runFor(ctx, runner, 60*time.Second, "drbdadm", "secondary", st.DRBDResource); err != nil {
				left = append(left, st.DRBDResource+" Primary ("+tailOf(o, err)+")")
			}
		}
		if didUp {
			if o, err := runFor(ctx, runner, 60*time.Second, "drbdadm", "down", st.DRBDResource); err != nil {
				left = append(left, st.DRBDResource+" up ("+tailOf(o, err)+")")
			}
		}
		if len(left) > 0 {
			oe := opErr(CodeRollbackFailed, "%s failed (%s) and the rollback left: %s", cause.Code, cause.Msg, strings.Join(left, "; "))
			oe.Leftovers = left
			oe.Details = cause.Details
			return oe
		}
		cause.Msg += "; every step was rolled back"
		return cause
	}

	if storage {
		// Mount state as the HOST sees it (libvirt/qemu and Pacemaker live
		// there); a mount only in this service's namespace does not count.
		mounted := false
		if src := hostMountSource(ctx, host, st.MountDir); src != "" {
			if !sameDevice(src, st.Device, opts.ResolveDevice) {
				return res, opErr(CodeMountConflict, "%s is already mounted from another device; nothing was changed", st.MountDir)
			}
			mounted = true
		}
		drbd := readLocalDRBD(ctx, runner, st.DRBDResource)
		if alreadyDefined && mounted && drbd.primary {
			// Everything in place; only the definition link may be missing.
			linked, lerr := linkDefinition(ctx, host, e, cfgPath, defLink, defLink, !definitionOnFS(opts, e, cfgPath), nil)
			if lerr != nil {
				return res, lerr
			}
			res.State, res.XMLLinked = "defined", true
			if linked != "" {
				res.Changed = true
				res.Steps = append(res.Steps, linked)
			}
			return res, nil
		}
		if !drbd.up {
			if mounted {
				return res, opErr(CodeMountConflict, "%s is mounted but DRBD resource %s is down; nothing was changed", st.MountDir, st.DRBDResource)
			}
			if o, err := runFor(ctx, runner, 60*time.Second, "drbdadm", "up", st.DRBDResource); err != nil {
				return res, opErr(CodeDRBDUpFailed, "drbdadm up %s: %s; nothing was changed", st.DRBDResource, tailOf(o, err))
			}
			didUp = true
			res.Steps = append(res.Steps, "drbdadm up "+st.DRBDResource)
		}
		deadline := time.Now().Add(wait)
		for {
			drbd = readLocalDRBD(ctx, runner, st.DRBDResource)
			if len(drbd.primaryPeers) > 0 || (drbd.upToDate && (drbd.allConnected || drbd.quorum == "yes")) || drbd.primary || !time.Now().Before(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				return res, rollback(opErr(CodeDRBDNotUpToDate, "cancelled while waiting for %s", st.DRBDResource))
			case <-time.After(poll):
			}
		}
		if len(drbd.primaryPeers) > 0 {
			return res, rollback(opErr(CodeDRBDPrimaryElsewhere, "%s is Primary on %s", st.DRBDResource, strings.Join(drbd.primaryPeers, ", ")))
		}
		if drbd.up && !drbd.primary && drbd.quorumLost() {
			// The DRBD resource agent left the resource down on every node:
			// promoting here would be refused (quorum) or unsafe. WebKVM has
			// no channel to run commands on the peers: tell the operator.
			peers := drbd.downPeers(st.DRBDResource)
			names := make([]string, 0, len(peers))
			for _, p := range peers {
				names = append(names, p.Node)
			}
			oe := opErr(CodeDRBDPeersDown, "%s has no quorum on this node: peers not connected (%s); run `drbdadm up %s` on each of them, then retry",
				st.DRBDResource, strings.Join(names, ", "), st.DRBDResource)
			oe.Details = map[string]any{"resource": st.DRBDResource, "peers": peers}
			return res, rollback(oe)
		}
		if !drbd.up || !drbd.upToDate {
			return res, rollback(opErr(CodeDRBDNotUpToDate, "the local disk of %s is not UpToDate", st.DRBDResource))
		}
		if !drbd.primary {
			if o, err := runFor(ctx, runner, 60*time.Second, "drbdadm", "primary", st.DRBDResource); err != nil {
				return res, rollback(opErr(CodeDRBDPromoteFailed, "drbdadm primary %s: %s", st.DRBDResource, tailOf(o, err)))
			}
			didPrimary = true
			res.Steps = append(res.Steps, "drbdadm primary "+st.DRBDResource)
		}
		if !mounted {
			if o, err := runFor(ctx, host, 10*time.Second, "mkdir", "-p", st.MountDir); err != nil {
				return res, rollback(opErr(CodeMountFailed, "mkdir %s: %s", st.MountDir, tailOf(o, err)))
			}
			fstype := st.FSType
			if fstype == "" {
				fstype = "ext4"
			}
			if o, err := runFor(ctx, host, 120*time.Second, "mount", "-t", fstype, st.Device, st.MountDir); err != nil {
				return res, rollback(opErr(CodeMountFailed, "mount %s: %s", st.MountDir, tailOf(o, err)))
			}
			didMount = true
			res.Steps = append(res.Steps, "mount "+st.Device+" "+st.MountDir)
			// The host (libvirt, Pacemaker) must see it, or the VM cannot
			// start and a hidden mount would keep DRBD open.
			if src := hostMountSource(ctx, host, st.MountDir); src == "" || !sameDevice(src, st.Device, opts.ResolveDevice) {
				return res, rollback(opErr(CodeMountNotVisible, "%s was mounted but the host mount namespace does not show it", st.MountDir))
			}
		}
	}

	if alreadyDefined {
		// Repair of the storage under an existing definition: link it.
		linked, lerr := linkDefinition(ctx, host, e, cfgPath, defLink, defLink, !definitionOnFS(opts, e, cfgPath),
			func(tmpWritten, written bool) { didWriteTmp, didWriteXML = tmpWritten, written })
		if lerr != nil {
			return res, rollback(lerr)
		}
		res.State, res.XMLLinked, res.Changed = "defined", true, true
		if linked != "" {
			res.Steps = append(res.Steps, linked)
		}
		return res, nil
	}

	// XML of record: the definition on the replicated filesystem when it is
	// readable and has the same identity (it may be newer), else the saved one.
	xmlText, source := e.XML, ReleaseSourceSaved
	if opts.ReadFile != nil && ValidReleasePath(st.ConfigPath) && (!storage || strings.HasPrefix(st.ConfigPath, st.MountDir+"/")) {
		if data, err := opts.ReadFile(st.ConfigPath); err == nil {
			if _, cerr := CheckDomainXML(data, e.Name, e.UUID); cerr == nil {
				xmlText, source = string(data), ReleaseSourceConfigFile
			}
		}
	}
	res.XMLSource = source
	res.XMLDiffers = source == ReleaseSourceConfigFile && XMLDigest(xmlText) != XMLDigest(e.XML)
	uuid, err := CheckDomainXML([]byte(xmlText), e.Name, e.UUID)
	if err != nil {
		var oe *OpError
		if !errors.As(err, &oe) {
			oe = opErr(CodeReleaseXMLInvalid, "%v", err)
		}
		return res, rollback(oe)
	}
	tmp, err := os.CreateTemp(opts.TmpDir, ".restore-*.xml")
	if err != nil {
		return res, rollback(opErr(CodeDefineFailed, "temporary XML file: %v", err))
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	_, werr := tmp.WriteString(xmlText)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		return res, rollback(opErr(CodeDefineFailed, "temporary XML file could not be written"))
	}
	if o, err := virsh(ctx, runner, 60*time.Second, "define", tmpName); err != nil {
		return res, rollback(opErr(CodeDefineFailed, "virsh define: %s", tailOf(o, err)))
	}
	didDefine = true
	res.Steps = append(res.Steps, "virsh define "+e.Name)
	if storage {
		// Only after a successful define: put the definition on the
		// replicated filesystem (unless that file IS the one just defined)
		// and point libvirt's definition at it, as HA enable expects.
		linked, lerr := linkDefinition(ctx, host, e, cfgPath, defLink, tmpName, source != ReleaseSourceConfigFile,
			func(tmpWritten, written bool) { didWriteTmp, didWriteXML = tmpWritten, written })
		if lerr != nil {
			return res, rollback(lerr)
		}
		res.XMLLinked = true
		if linked != "" {
			res.Steps = append(res.Steps, linked)
		}
	}
	res.Changed = true
	res.UUID = uuid
	res.State = "defined"
	if opts.Start {
		if o, err := virsh(ctx, runner, 180*time.Second, "start", e.Name); err != nil {
			res.Changed = false
			res.State = ""
			return res, rollback(opErr(CodeStartFailed, "virsh start %s: %s", e.Name, tailOf(o, err)))
		}
		res.State = "running"
		res.Steps = append(res.Steps, "virsh start "+e.Name)
	}
	return res, nil
}

// linkDefinition makes defLink (libvirt's persistent definition) a symlink
// to cfgPath on the replicated filesystem, every command in the host mount
// namespace. With write, the XML in srcFile is first installed at cfgPath
// atomically (0600 temp file, fsync, rename) — only when cfgPath does not
// exist: a file there that is not this VM's definition is never
// overwritten. progress reports what was written (for the rollback). It
// returns the step done ("" when the link was already in place).
func linkDefinition(ctx context.Context, host sysutil.CommandRunner, e ReleasedVM, cfgPath, defLink, srcFile string, write bool,
	progress func(tmpWritten, written bool)) (string, *OpError) {
	if progress == nil {
		progress = func(bool, bool) {}
	}
	fail := func(format string, args ...any) (string, *OpError) {
		return "", opErr(CodeXMLLinkFailed, format, args...)
	}
	if o, err := runFor(ctx, host, 10*time.Second, "readlink", "--", defLink); err == nil && strings.TrimSpace(string(o)) == cfgPath {
		return "", nil
	}
	if write {
		o, err := runFor(ctx, host, 10*time.Second, "stat", "-c", "%F", "--", cfgPath)
		switch {
		case err == nil:
			return fail("%s already exists and is not the definition of %s; it was not overwritten", cfgPath, e.Name)
		case !strings.Contains(strings.ToLower(tailOf(o, err)), "no such file"):
			return fail("cannot check %s: %s", cfgPath, tailOf(o, err))
		}
		cfgNew := cfgPath + ".webkvm-new"
		if o, err := runFor(ctx, host, 30*time.Second, "install", "-m", "0600", "-T", srcFile, cfgNew); err != nil {
			return fail("write %s: %s", cfgNew, tailOf(o, err))
		}
		progress(true, false)
		if o, err := runFor(ctx, host, 30*time.Second, "sync", "--", cfgNew); err != nil {
			return fail("sync %s: %s", cfgNew, tailOf(o, err))
		}
		if o, err := runFor(ctx, host, 30*time.Second, "mv", "-fT", cfgNew, cfgPath); err != nil {
			return fail("rename %s: %s", cfgPath, tailOf(o, err))
		}
		progress(false, true)
	}
	if o, err := runFor(ctx, host, 10*time.Second, "ln", "-sfnT", cfgPath, defLink); err != nil {
		return fail("link %s -> %s: %s", defLink, cfgPath, tailOf(o, err))
	}
	return "link " + defLink + " -> " + cfgPath, nil
}

// definitionOnFS reports whether cfgPath holds this VM's definition (same
// name and UUID), read through opts.ReadFile.
func definitionOnFS(opts RestoreOptions, e ReleasedVM, cfgPath string) bool {
	if opts.ReadFile == nil {
		return false
	}
	data, err := opts.ReadFile(cfgPath)
	if err != nil {
		return false
	}
	_, cerr := CheckDomainXML(data, e.Name, e.UUID)
	return cerr == nil
}

func sameDevice(src, device string, resolve func(string) (string, error)) bool {
	if src == device {
		return true
	}
	if resolve == nil {
		return false
	}
	a, err1 := resolve(src)
	b, err2 := resolve(device)
	return err1 == nil && err2 == nil && a == b
}
