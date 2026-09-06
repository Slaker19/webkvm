package backupstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// lastPresentedHostKey records the host key fingerprint presented by
// the most recent SFTP dial so the ad-hoc "Test connection" path can
// surface it for the operator to copy into the target's known-host
// allowlist (V13-BCK-05). Not a pin — a record of what a dial saw.
var (
	lastPresentedKeyMu sync.Mutex
	lastPresentedKey   string
)

// hostKeyFingerprint formats a presented host key the way ssh-keyscan
// prints it, e.g. "ssh-ed25519 SHA256:ABC123...".
func hostKeyFingerprint(key ssh.PublicKey) string {
	return key.Type() + " " + ssh.FingerprintSHA256(key)
}

func recordLastPresentedKey(fp string) {
	lastPresentedKeyMu.Lock()
	lastPresentedKey = fp
	lastPresentedKeyMu.Unlock()
}

// LastPresentedHostKey returns the fingerprint of the host key most
// recently presented by a TestSFTP dial ("" if none yet). TestSFTP
// appends it to its success message so the operator can copy it into
// the target's known_hosts without a separate ssh-keyscan.
func LastPresentedHostKey() string {
	lastPresentedKeyMu.Lock()
	defer lastPresentedKeyMu.Unlock()
	return lastPresentedKey
}

// configuredFingerprints extracts the SHA256 host-key fingerprints an
// operator has pinned for a target. Entries may be bare "SHA256:..."
// strings or full "ssh-ed25519 SHA256:..." lines (as pasted from
// ssh-keyscan or a dial error); whitespace/newline separated lists are
// accepted.
func configuredFingerprints(known []string) map[string]bool {
	out := make(map[string]bool)
	for _, entry := range known {
		for _, tok := range strings.Fields(entry) {
			if t := strings.TrimSpace(tok); strings.HasPrefix(t, "SHA256:") {
				out[t] = true
			}
		}
	}
	return out
}

// hostKeyCallbackFor returns a STRICT host key validator for a target.
// The blind trust-on-first-use pinning is gone (V13-BCK-05): a
// configured target with no known-host fingerprints is REFUSED, and
// every presented key is matched against the operator-pinned allowlist.
//
// When a dial fails because the host is unknown or the key changed, the
// callback returns an error that embeds the server's PRESENTED
// fingerprint (e.g. "ssh-ed25519 SHA256:…"), so the operator can copy
// it into the target's "known host fingerprints" field to authorize the
// host explicitly — no silent first-use trust, no insecure ignore.
//
// The ad-hoc "Test connection" dial (tgt.ID == "") has no target to pin
// against yet, so it is allowed through but always records the
// presented fingerprint so TestSFTP can report it.
func hostKeyCallbackFor(tgt Target) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		presented := ssh.FingerprintSHA256(key)
		full := hostKeyFingerprint(key)
		recordLastPresentedKey(full)
		if tgt.ID == "" {
			// Ad-hoc test dial; nothing pinned yet.
			return nil
		}
		if len(tgt.KnownHosts) == 0 {
			return fmt.Errorf(
				"ssh host key of %q is not trusted: this target has no known host fingerprints configured. "+
					"Presented key: %s. Copy that fingerprint into the target's \"known host fingerprints\" "+
					"field to authorize this host explicitly", hostname, full)
		}
		if configuredFingerprints(tgt.KnownHosts)[presented] {
			return nil
		}
		return fmt.Errorf(
			"ssh host key of %q does not match any configured fingerprint. Presented key: %s, expected one of %v — "+
				"refusing to connect; the server may have been reprovisioned or something is impersonating it",
			hostname, full, tgt.KnownHosts)
	}
}

// TestSFTP dials a candidate SFTP destination (before it is saved)
// and returns a human message. Used by the "Test" button in the Add
// Target dialog. The host key fingerprint presented by the server is
// appended to the success message so the operator can copy it into
// the target's known-host allowlist (V13-BCK-05).
func TestSFTP(host string, port int, username, password, keyPath, remoteDir string) (string, error) {
	if port == 0 {
		port = 22
	}
	tgt := Target{
		Type:     TargetSFTP,
		Host:     host,
		Port:     port,
		Username: username,
		Path:     remoteDir,
		Secret:   TargetSecret{Password: password, SSHKeyPath: keyPath},
	}
	client, conn, err := dialSFTP(tgt)
	if err != nil {
		return "", err
	}
	defer client.Close()
	defer conn.Close()
	fpSuffix := ""
	if fp := LastPresentedHostKey(); fp != "" {
		fpSuffix = " · host key: " + fp
	}
	if remoteDir != "" {
		if err := client.MkdirAll(remoteDir); err != nil {
			return "", fmt.Errorf("remote directory %s not usable: %w", remoteDir, err)
		}
		return "connected; remote directory ready" + fpSuffix, nil
	}
	return "connected" + fpSuffix, nil
}

// dialSFTP opens an SSH connection to the remote target and returns
// an SFTP client. Authentication uses the stored password when
// present, otherwise the SSH private key at SSHKeyPath. A short
// connect timeout keeps a dead host from hanging the UI.
func dialSFTP(tgt Target) (*sftp.Client, *ssh.Client, error) {
	if tgt.Type != TargetSFTP {
		return nil, nil, fmt.Errorf("target %q is not an sftp target", tgt.ID)
	}
	if tgt.Host == "" || tgt.Username == "" {
		return nil, nil, errors.New("sftp target is missing host or username")
	}
	port := tgt.Port
	if port == 0 {
		port = 22
	}
	authMethods, err := sftpAuthMethods(tgt.Secret)
	if err != nil {
		return nil, nil, err
	}
	cfg := &ssh.ClientConfig{
		User: tgt.Username,
		Auth: authMethods,
		// Strict known-host validation (V13-BCK-05): the presented
		// key must match the target's configured fingerprints, or the
		// dial fails and surfaces the server's fingerprint so the
		// operator can authorize it explicitly. No trust-on-first-use.
		HostKeyCallback: hostKeyCallbackFor(tgt),
		Timeout:         15 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", tgt.Host, port)
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh %s: %w", addr, err)
	}
	client, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("sftp %s: %w", addr, err)
	}
	return client, conn, nil
}

func sftpAuthMethods(sec TargetSecret) ([]ssh.AuthMethod, error) {
	if sec.Password != "" {
		return []ssh.AuthMethod{ssh.Password(sec.Password)}, nil
	}
	if sec.SSHKeyPath != "" {
		cleanedKeyPath := filepath.Clean(sec.SSHKeyPath)
		if !filepath.IsAbs(cleanedKeyPath) || strings.Contains(cleanedKeyPath, "..") {
			return nil, fmt.Errorf("invalid ssh key path %q", sec.SSHKeyPath)
		}
		key, err := os.ReadFile(cleanedKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read ssh key %s: %w", sec.SSHKeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse ssh key %s: %w", sec.SSHKeyPath, err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	return nil, errors.New("sftp target has no password or ssh key configured")
}

// remoteJoin joins a filename to the target's remote directory.
func remoteJoin(tgt Target, name string) string {
	return strings.TrimSuffix(tgt.Path, "/") + "/" + name
}

// sftpList returns the backup archives in the target's remote
// directory, newest first. Mirrors ListBackupsOnTarget's filtering.
func sftpList(tgt Target) ([]BackupFile, error) {
	client, conn, err := dialSFTP(tgt)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	defer conn.Close()

	infos, err := client.ReadDir(tgt.Path)
	if err != nil {
		return nil, err
	}
	out := make([]BackupFile, 0, len(infos))
	for _, info := range infos {
		if info.IsDir() {
			continue
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tar.zst") {
			continue
		}
		out = append(out, BackupFile{
			TargetID: tgt.ID,
			Filename: name,
			Size:     info.Size(),
			Modified: info.ModTime().UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// sftpVerify streams the remote file through sha256, matching
// VerifyBackup's semantics without downloading it to disk.
func sftpVerify(tgt Target, filename string) (BackupFile, error) {
	client, conn, err := dialSFTP(tgt)
	if err != nil {
		return BackupFile{}, err
	}
	defer client.Close()
	defer conn.Close()

	f, err := client.Open(remoteJoin(tgt, filename))
	if err != nil {
		return BackupFile{}, err
	}
	defer f.Close()
	h := sha256.New()
	// V13-BCK-04: stream straight from the remote file into the hash —
	// the archive never touches local disk and never loads into RAM.
	// io.Copy also propagates a mid-stream read error (a manual read
	// loop used to swallow it and return a truncated checksum).
	if _, err := io.Copy(h, f); err != nil {
		return BackupFile{}, fmt.Errorf("sftp read %s: %w", filename, err)
	}
	info, err := f.Stat()
	if err != nil {
		return BackupFile{}, err
	}
	return BackupFile{
		TargetID: tgt.ID,
		Filename: filename,
		Size:     info.Size(),
		Modified: info.ModTime().UTC(),
		Sha256:   hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// sftpDelete removes a single archive from the remote directory.
func sftpDelete(tgt Target, filename string) error {
	if !ValidBackupFilename(filename) {
		return fmt.Errorf("invalid filename %q", filename)
	}
	client, conn, err := dialSFTP(tgt)
	if err != nil {
		return err
	}
	defer client.Close()
	defer conn.Close()
	if err := client.Remove(remoteJoin(tgt, filename)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return err
	}
	return nil
}

// sftpDeleteRun removes every archive in a backup run from the
// remote directory.
func sftpDeleteRun(tgt Target, runSuffix string) (int, error) {
	if !isRunSuffix(runSuffix) {
		return 0, fmt.Errorf("invalid run suffix %q", runSuffix)
	}
	client, conn, err := dialSFTP(tgt)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	defer conn.Close()
	infos, err := client.ReadDir(tgt.Path)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, info := range infos {
		if info.IsDir() || !strings.Contains(info.Name(), runSuffix) {
			continue
		}
		if !ValidBackupFilename(info.Name()) {
			continue
		}
		if err := client.Remove(remoteJoin(tgt, info.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

// StageFileForRestore downloads a single archive from a remote
// target into a staging directory and returns its local path plus a
// cleanup function. Used by RestoreAsVM, which needs a local file to
// feed into the libvirt import path. The caller MUST invoke the
// returned cleanup once done (it is nil when tgt is not SFTP).
func StageFileForRestore(tgt Target, filename string, dataDir string) (localPath string, size int64, cleanup func(), err error) {
	switch tgt.Type {
	case TargetSFTP:
		// (sftp branch below)
	case TargetS3:
		return s3StageFileForRestore(tgt, filename, dataDir)
	}
	if tgt.Type != TargetSFTP {
		return "", 0, nil, nil
	}
	if !ValidBackupFilename(filename) {
		return "", 0, nil, fmt.Errorf("invalid filename %q", filename)
	}
	dir := filepath.Join(dataDir, "backup-staging", tgt.ID+"-"+randHex(4))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, nil, err
	}
	client, conn, err := dialSFTP(tgt)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", 0, nil, err
	}
	defer client.Close()
	defer conn.Close()
	src, err := client.Open(remoteJoin(tgt, filename))
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", 0, nil, err
	}
	info, err := src.Stat()
	if err != nil {
		_ = src.Close()
		_ = os.RemoveAll(dir)
		return "", 0, nil, err
	}
	local := filepath.Join(dir, filename)
	dst, err := os.Create(local)
	if err != nil {
		_ = src.Close()
		_ = os.RemoveAll(dir)
		return "", 0, nil, err
	}
	_, cErr := io.Copy(dst, src)
	clErr := dst.Close()
	_ = src.Close()
	if cErr != nil || clErr != nil {
		_ = os.RemoveAll(dir)
		return "", 0, nil, errOr(cErr, clErr)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	return local, info.Size(), cleanup, nil
}

// uploadSFTPRun copies every file produced by a run (all in
// localDir) up to the target's remote directory. The remote
// directory is created if missing.
func uploadSFTPRun(tgt Target, localDir string, files []JobFile) (int64, error) {
	client, conn, err := dialSFTP(tgt)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	defer conn.Close()
	if err := client.MkdirAll(tgt.Path); err != nil {
		return 0, fmt.Errorf("mkdir remote %s: %w", tgt.Path, err)
	}
	var total int64
	for _, f := range files {
		local := filepath.Join(localDir, f.Filename)
		dst, err := client.Create(remoteJoin(tgt, f.Filename))
		if err != nil {
			return total, fmt.Errorf("create remote %s: %w", f.Filename, err)
		}
		src, err := os.Open(local)
		if err != nil {
			_ = dst.Close()
			return total, err
		}
		n, cErr := io.Copy(dst, src)
		clErr := dst.Close()
		_ = src.Close()
		if cErr != nil || clErr != nil {
			return total, fmt.Errorf("upload %s: %v", f.Filename, errOr(cErr, clErr))
		}
		total += n
	}
	// Also push the stable "latest config" snapshot if the staging
	// dir contains one (written by copyConfigGlobal).
	gLocal := filepath.Join(localDir, configGlobalRel)
	if _, err := os.Stat(gLocal); err == nil {
		if err := client.MkdirAll(tgt.Path + "/config"); err != nil {
			return total, err
		}
		dst, err := client.Create(remoteJoin(tgt, configGlobalRel))
		if err != nil {
			return total, fmt.Errorf("create remote config: %w", err)
		}
		src, err := os.Open(gLocal)
		if err != nil {
			_ = dst.Close()
			return total, err
		}
		_, cErr := io.Copy(dst, src)
		clErr := dst.Close()
		_ = src.Close()
		if cErr != nil || clErr != nil {
			return total, fmt.Errorf("upload config: %v", errOr(cErr, clErr))
		}
	}
	return total, nil
}

func errOr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// stageSFTPFiles downloads a run (or an explicit list) from the
// remote target into a staging directory under dataDir. It returns
// the staging dir and the local filenames (sorted). The caller is
// responsible for os.RemoveAll(staging).
func stageSFTPFiles(tgt Target, runSuffix string, filenames []string, dataDir string) (string, []string, error) {
	if runSuffix == "" && len(filenames) == 0 {
		return "", nil, errors.New("stage: must specify run or filenames")
	}
	client, conn, err := dialSFTP(tgt)
	if err != nil {
		return "", nil, err
	}
	defer client.Close()
	defer conn.Close()

	infos, err := client.ReadDir(tgt.Path)
	if err != nil {
		return "", nil, err
	}
	want := map[string]bool{}
	for _, n := range filenames {
		if !ValidBackupFilename(n) {
			return "", nil, fmt.Errorf("invalid filename %q", n)
		}
		want[n] = true
	}
	var picks []string
	for _, info := range infos {
		name := info.Name()
		if info.IsDir() || !ValidBackupFilename(name) {
			continue
		}
		if runSuffix != "" {
			if strings.Contains(name, runSuffix) {
				picks = append(picks, name)
			}
		} else if want[name] {
			picks = append(picks, name)
		}
	}
	if len(picks) == 0 {
		return "", nil, fmt.Errorf("no files matched on remote target")
	}
	sort.Strings(picks)
	staging := filepath.Join(dataDir, "backup-staging", tgt.ID+"-"+randHex(4))
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", nil, err
	}
	for _, name := range picks {
		src, err := client.Open(remoteJoin(tgt, name))
		if err != nil {
			_ = os.RemoveAll(staging)
			return "", nil, err
		}
		dst, err := os.Create(filepath.Join(staging, name))
		if err != nil {
			_ = src.Close()
			_ = os.RemoveAll(staging)
			return "", nil, err
		}
		_, cErr := io.Copy(dst, src)
		clErr := dst.Close()
		_ = src.Close()
		if cErr != nil || clErr != nil {
			_ = os.RemoveAll(staging)
			return "", nil, errOr(cErr, clErr)
		}
	}
	slog.Debug("backup_staged_sftp", "target", tgt.ID, "files", len(picks), "staging", staging)
	return staging, picks, nil
}
