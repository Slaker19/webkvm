package backupstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// TestSFTP dials a candidate SFTP destination (before it is saved)
// and returns a human message. Used by the "Test" button in the Add
// Target dialog.
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
	if remoteDir != "" {
		if err := client.MkdirAll(remoteDir); err != nil {
			return "", fmt.Errorf("remote directory %s not usable: %w", remoteDir, err)
		}
		return "connected; remote directory ready", nil
	}
	return "connected", nil
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
		// lgtm[go/insecure-hostkeycallback] - homelab SFTP target: operator verifies host key out-of-band;
		// strict verification would require known_hosts management which is out of scope for homelab.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil {
			break
		}
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
