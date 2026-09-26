// Package vzdump provides read and write support for the Proxmox VE
// vzdump backup format for LXC containers (file-level storages).
//
// # Proxmox vzdump LXC format
//
// When Proxmox backs up a container to a file-level storage it produces a
// tar archive (optionally compressed with gzip, lzo or zstd) whose layout is:
//
//	./rootfs/               ← full container root filesystem
//	./etc/vzdump/pct.conf   ← Proxmox CT configuration (pct.conf syntax)
//	./etc/vzdump/pct.fw     ← firewall rules (optional, may be absent)
//
// The archive name follows the pattern:
//
//	vzdump-lxc-<vmid>-<YYYY_MM_DD-HH_MM_SS>.tar[.gz|.lzo|.zst]
//
// # Incus backup format
//
// Incus (and the LXD it forked from) produces a tar archive where the
// root tarball contains:
//
//	backup.yaml             ← YAML metadata (instance config, profiles …)
//	index.yaml              ← backup manifest
//	rootfs.squashfs         ← container rootfs as a SquashFS image (most
//	                          storages), OR
//	rootfs/                 ← plain directory tree (dir-backed pools)
//
// # Conversion strategy
//
// Proxmox → Incus (Import):
//  1. Walk the vzdump tar looking for ./etc/vzdump/pct.conf (proof of
//     format) and ./rootfs/ entries.
//  2. Build a minimal backup.yaml + index.yaml from the pct.conf.
//  3. Re-pack rootfs as rootfs/ directory entries (plain dir format, which
//     Incus accepts regardless of storage driver) plus the two YAML files
//     into a new tar.
//  4. Hand the resulting tar file path to IncusBackend.ImportDomain().
//
// Incus → Proxmox (Export):
//  1. Receive the raw Incus backup stream (from ExportDomain), save to a
//     tmpfile.
//  2. Detect rootfs format: squashfs blob OR rootfs/ directory tree.
//     - squashfs: expand with unsquashfs into a tempdir.
//     - dir: extract rootfs/ entries directly.
//  3. Build backup.yaml-derived pct.conf (name, CPU, memory).
//  4. Write final tar.zst with ./rootfs/... + ./etc/vzdump/pct.conf to w.
package vzdump

import (
	"archive/tar"
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"go.yaml.in/yaml/v3"
)

// IsVzdumpLXC reports whether the reader looks like a vzdump LXC backup
// (compressed or not). It probes for the magic bytes of gzip / lzo / zstd
// and then peeks at the first few tar entries looking for
// ./etc/vzdump/pct.conf or a ./rootfs/ entry. The reader position is
// restored (buffered) so the caller can still read the full archive.
//
// tarPath must be an already-written file on disk (not a stream); we
// re-open it so the magic-byte check does not disturb the caller's reader.
func IsVzdumpLXC(tarPath string) (bool, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 4096)
	head, _ := br.Peek(4)

	var r io.Reader = br
	if len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b {
		// gzip
		gz, err := newGzipReader(r)
		if err != nil {
			return false, nil
		}
		defer gz.Close()
		r = gz
	} else if len(head) >= 4 && head[0] == 0x28 && head[1] == 0xb5 && head[2] == 0x2f && head[3] == 0xfd {
		// zstd
		dec, err := zstd.NewReader(r)
		if err != nil {
			return false, nil
		}
		defer dec.Close()
		r = dec
	} else if len(head) >= 4 && head[0] == 0x89 && head[1] == 0x4c && head[2] == 0x5a && head[3] == 0x4f {
		// lzo — we cannot decompress lzo in pure Go; treat as unknown
		return false, nil
	}

	tr := tar.NewReader(r)
	for i := 0; i < 40; i++ {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		name := filepath.ToSlash(hdr.Name)
		name = strings.TrimPrefix(name, "./")
		if name == "etc/vzdump/pct.conf" || strings.HasPrefix(name, "rootfs/") {
			return true, nil
		}
		// Proxmox vzdump LXC: files are at root level (./bin, ./lib, ./etc/...)
		// with ./etc/vzdump/pct.conf as the config marker.
		if strings.HasPrefix(name, "etc/vzdump/") {
			return true, nil
		}
	}
	return false, nil
}

// ImportFromProxmox converts a vzdump LXC archive (at srcPath, already
// decompressed or still compressed with gzip/zstd) into an Incus-compatible
// backup tar file and writes it to destPath.
//
// The resulting tar has the layout expected by
// IncusBackend.ImportDomain / CreateInstanceFromBackup:
//
//	backup.yaml
//	index.yaml
//	rootfs/       ← all ./rootfs/ entries from the vzdump, paths stripped
//
// newName is used as the instance name in backup.yaml (Incus will set it
// again on import if the caller passes a name; we put it here as a hint).
// poolName is the Incus storage pool the converted archive declares.
// Empty falls back to "default".
//
// This used to be hardcoded to "default" in both YAML files, which only
// worked on an Incus installation that happened to have a pool by that
// name. WebKVM names its pool after the volume backing it, so on a
// typical install there IS no "default" pool and the import landed on a
// pool that did not exist.
func ImportFromProxmox(srcPath, destPath, newName, poolName string) error {
	// --- open source ---
	sf, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("vzdump: open source: %w", err)
	}
	defer sf.Close()

	br := bufio.NewReaderSize(sf, 1<<20)
	head, _ := br.Peek(4)

	var srcReader io.Reader = br
	if len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := newGzipReader(srcReader)
		if err != nil {
			return fmt.Errorf("vzdump: open gzip: %w", err)
		}
		defer gz.Close()
		srcReader = gz
	} else if len(head) >= 4 && head[0] == 0x28 && head[1] == 0xb5 && head[2] == 0x2f && head[3] == 0xfd {
		dec, err := zstd.NewReader(srcReader)
		if err != nil {
			return fmt.Errorf("vzdump: open zstd: %w", err)
		}
		defer dec.Close()
		srcReader = dec
	}

	// --- read pct.conf + filesystem entries from the vzdump tar ---
	// Proxmox vzdump LXC layout:
	//   ./etc/vzdump/pct.conf   ← CT config
	//   ./etc/vzdump/pct.fw     ← firewall rules (optional)
	//   ./bin, ./lib, ./etc/... ← filesystem entries AT ROOT LEVEL (no rootfs/ prefix)
	type rootEntry struct {
		hdr  *tar.Header
		data []byte
	}
	var pctConf string
	var rootEntries []rootEntry

	srcTar := tar.NewReader(srcReader)
	for {
		hdr, err := srcTar.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("vzdump: iterate tar: %w", err)
		}

		clean := filepath.ToSlash(hdr.Name)
		clean = strings.TrimPrefix(clean, "./")

		// Extract pct.conf
		if clean == "etc/vzdump/pct.conf" {
			raw, _ := io.ReadAll(srcTar)
			pctConf = string(raw)
			continue
		}

		// Skip etc/vzdump/pct.fw and other vzdump metadata
		if strings.HasPrefix(clean, "etc/vzdump/") {
			_, _ = io.Copy(io.Discard, srcTar)
			continue
		}

		// Skip the root directory entry itself
		if clean == "" || clean == "." {
			continue
		}

		// Two possible layouts:
		// 1) Proxmox vzdump: files at root (./bin, ./lib, ./etc/...) → add rootfs/ prefix
		// 2) Already rootfs/: files under rootfs/ → strip rootfs/ prefix
		hdrCopy := *hdr
		if strings.HasPrefix(clean, "rootfs/") {
			hdrCopy.Name = strings.TrimPrefix(clean, "rootfs/")
		} else {
			hdrCopy.Name = clean
		}
		if hdrCopy.Name == "" {
			hdrCopy.Name = "."
		}

		// For hard links, update the link target to include backup/container/
		// prefix, because tar --strip-components=2 strips the same leading
		// components from both entry names AND hardlink targets.
		if hdrCopy.Linkname != "" && hdrCopy.Typeflag == tar.TypeLink {
			link := hdrCopy.Linkname
			if !strings.HasPrefix(clean, "rootfs/") {
				// Original layout (Proxmox): files at root. We add rootfs/ prefix.
				// Link targets like ./usr/bin/perl need to become
				// backup/container/rootfs/usr/bin/perl so that after
				// --strip-components=2, tar resolves them to rootfs/usr/bin/perl.
				link = "backup/container/rootfs/" + strings.TrimPrefix(link, "./")
			} else {
				// Already under rootfs/: prepend backup/container/ so that
				// --strip-components=2 produces the correct relative path.
				link = "backup/container/" + link
			}
			hdrCopy.Linkname = link
		}

		var buf []byte
		if hdr.Typeflag == tar.TypeReg {
			buf, _ = io.ReadAll(srcTar)
		}
		rootEntries = append(rootEntries, rootEntry{hdr: &hdrCopy, data: buf})
	}

	// --- parse pct.conf to enrich backup.yaml ---
	info := parsePctConf(pctConf)
	if newName != "" {
		info.name = newName
	}
	if info.name == "" {
		info.name = "imported-ct"
	}

	// --- write dest tar (plain, no compression — the caller wraps if needed) ---
	df, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("vzdump: create dest: %w", err)
	}
	defer df.Close()

	tw := tar.NewWriter(df)
	defer tw.Close()

	// backup/container/backup.yaml — Incus reads this for the container config
	backupYAML := buildBackupYAML(info, poolName)
	if err := writeTarString(tw, "backup/container/backup.yaml", backupYAML); err != nil {
		return err
	}

	// backup/index.yaml — Incus reads this first for metadata
	indexYAML := buildIndexYAML(info, poolName)
	if err := writeTarString(tw, "backup/index.yaml", indexYAML); err != nil {
		return err
	}

	// rootfs/ entries — Incus expects them under backup/container/
	for _, e := range rootEntries {
		hdr := *e.hdr
		hdr.Name = "backup/container/rootfs/" + hdr.Name
		if err := tw.WriteHeader(&hdr); err != nil {
			return fmt.Errorf("vzdump: write header %s: %w", hdr.Name, err)
		}
		if len(e.data) > 0 {
			if _, err := tw.Write(e.data); err != nil {
				return fmt.Errorf("vzdump: write data %s: %w", hdr.Name, err)
			}
		}
	}

	return tw.Close()
}

// ExportToProxmox converts a raw Incus backup stream (srcReader) into a
// Proxmox-compatible vzdump LXC tar.zst file written to w.
//
// The function:
//  1. Saves the Incus stream to a tempfile.
//  2. Extracts backup.yaml for container metadata.
//  3. Detects whether rootfs is squashfs blob or rootfs/ dir entries.
//     - squashfs: expands with unsquashfs (must be on PATH).
//     - dir: restores entries directly.
//  4. Writes final tar.zst with:
//     ./rootfs/...
//     ./etc/vzdump/pct.conf
//
// zstdLevel controls compression (1-22; 0 → 19).
func ExportToProxmox(srcReader io.Reader, w io.Writer, zstdLevel int) error {
	if zstdLevel < 1 || zstdLevel > 22 {
		zstdLevel = 19
	}

	// --- save to tmpfile ---
	tmp, err := os.CreateTemp("", "webkvm-incus-export-*.tar")
	if err != nil {
		return fmt.Errorf("vzdump: tmpfile: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if _, err := io.Copy(tmp, srcReader); err != nil {
		return fmt.Errorf("vzdump: save stream: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("vzdump: seek: %w", err)
	}

	// --- parse outer tar (may itself be compressed: Incus sometimes wraps) ---
	tmpPath := tmp.Name()
	tf, err := openMaybeCompressed(tmpPath)
	if err != nil {
		return fmt.Errorf("vzdump: open incus tar: %w", err)
	}
	defer tf.Close()

	type rootEntry struct {
		hdr  *tar.Header
		data []byte
	}

	var (
		meta       incusBackupMeta
		hasSquash  bool
		squashPath string // path to squashfs tmpfile (non-empty when hasSquash)
		rootItems  []rootEntry
	)

	srcTar := tar.NewReader(tf)
	for {
		hdr, err := srcTar.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("vzdump: iterate incus tar: %w", err)
		}

		name := filepath.ToSlash(hdr.Name)
		name = strings.TrimPrefix(name, "./")

		// Strip backup/container/ prefix (Incus default layout) and
		// backup/ prefix (flat layout) to normalize to rootfs/ paths.
		name = strings.TrimPrefix(name, "backup/container/")
		name = strings.TrimPrefix(name, "backup/")

		switch {
		case name == "backup.yaml":
			raw, _ := io.ReadAll(srcTar)
			_ = yaml.Unmarshal(raw, &meta)
			// Also try parsing as the newer Config format
			if meta.Container == nil {
				var cfg struct {
					Container *struct {
						Name   string            `yaml:"name"`
						Config map[string]string `yaml:"config"`
						Arch   string            `yaml:"architecture"`
					} `yaml:"container"`
				}
				if err := yaml.Unmarshal(raw, &cfg); err == nil && cfg.Container != nil {
					meta.Container = cfg.Container
				}
			}

		case name == "rootfs.squashfs":
			hasSquash = true
			// Stream squashfs to a tmpfile instead of loading into RAM
			// (containers can be multiple gigabytes).
			sqf, sqErr := os.CreateTemp("", "webkvm-sqsh-*.sfs")
			if sqErr != nil {
				return fmt.Errorf("vzdump: squash tmp: %w", sqErr)
			}
			squashPath = sqf.Name()
			if _, sqErr = io.Copy(sqf, srcTar); sqErr != nil {
				sqf.Close()
				os.Remove(squashPath)
				return fmt.Errorf("vzdump: write squash: %w", sqErr)
			}
			sqf.Close()

		case strings.HasPrefix(name, "rootfs/"):
			hdrCopy := *hdr
			hdrCopy.Name = name
			// Normalize hard link targets: strip backup/container/ prefix
			if hdrCopy.Linkname != "" && hdrCopy.Typeflag == tar.TypeLink {
				hdrCopy.Linkname = strings.TrimPrefix(hdrCopy.Linkname, "backup/container/")
			}
			var buf []byte
			if hdr.Typeflag == tar.TypeReg {
				buf, _ = io.ReadAll(srcTar)
			}
			rootItems = append(rootItems, rootEntry{hdr: &hdrCopy, data: buf})

		default:
			// skip index.yaml and other metadata
			_, _ = io.Copy(io.Discard, srcTar)
		}
	}

	// --- expand squashfs if needed ---
	var rootfsDir string
	if hasSquash && squashPath != "" {
		defer os.Remove(squashPath)

		rootfsDir, err = os.MkdirTemp("", "webkvm-rootfs-*")
		if err != nil {
			return fmt.Errorf("vzdump: rootfs tmpdir: %w", err)
		}
		defer os.RemoveAll(rootfsDir)

		cmd := exec.Command("unsquashfs", "-f", "-d", rootfsDir, squashPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("vzdump: unsquashfs failed: %w: %s", err, out)
		}
	}

	// --- build pct.conf from metadata ---
	pctConf := buildPctConf(meta)

	// --- write output tar.zst ---
	enc, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(zstdLevel)))
	if err != nil {
		return fmt.Errorf("vzdump: zstd writer: %w", err)
	}

	tw := tar.NewWriter(enc)

	// ./etc/vzdump/pct.conf
	if err := writeTarString(tw, "./etc/vzdump/pct.conf", pctConf); err != nil {
		return err
	}

	if rootfsDir != "" {
		// squashfs-expanded path: walk rootfsDir
		err = filepath.Walk(rootfsDir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, _ := filepath.Rel(rootfsDir, path)
			tarName := "./rootfs/" + filepath.ToSlash(rel)
			if rel == "." {
				tarName = "./rootfs"
			}

			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = tarName

			// resolve symlink target for header
			if info.Mode()&os.ModeSymlink != 0 {
				link, _ := os.Readlink(path)
				hdr.Linkname = link
			}

			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			ff, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, ff)
			ff.Close()
			return err
		})
		if err != nil {
			return fmt.Errorf("vzdump: walk rootfs: %w", err)
		}
	} else {
		// dir-entry path: emit rootItems already collected
		for _, e := range rootItems {
			hdr := *e.hdr
			// ensure ./rootfs/ prefix
			if !strings.HasPrefix(hdr.Name, "./") {
				hdr.Name = "./" + hdr.Name
			}
			if err := tw.WriteHeader(&hdr); err != nil {
				return fmt.Errorf("vzdump: write hdr %s: %w", hdr.Name, err)
			}
			if len(e.data) > 0 {
				if _, err := tw.Write(e.data); err != nil {
					return fmt.Errorf("vzdump: write data %s: %w", hdr.Name, err)
				}
			}
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("vzdump: close tar: %w", err)
	}
	return enc.Close()
}

// VzdumpFilename returns a Proxmox-compatible filename for a vzdump LXC
// backup, e.g. "vzdump-lxc-0-2026_09_21-15_04_05.tar.zst".
// vmid is the numeric Proxmox VMID; pass 0 if unknown.
func VzdumpFilename(name string, ts time.Time) string {
	stamp := ts.Format("2006_01_02-15_04_05")
	// Use the container name as a suffix since we don't have a Proxmox VMID.
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
	return fmt.Sprintf("vzdump-lxc-%s-%s.tar.zst", safe, stamp)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// pctInfo holds the fields we extract from a vzdump pct.conf.
type pctInfo struct {
	name   string
	cores  string
	memory string // in MB
	arch   string
	ostype string
}

// parsePctConf parses Proxmox's pct.conf key: value format.
func parsePctConf(raw string) pctInfo {
	info := pctInfo{arch: "amd64", ostype: "unmanaged"}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "hostname":
			info.name = v
		case "cores":
			info.cores = v
		case "memory":
			info.memory = v
		case "arch":
			info.arch = v
		case "ostype":
			info.ostype = v
		}
	}
	return info
}

// resolvePool normalizes the target pool name for the generated YAML.
// Incus itself uses "default" when nothing else is known, so that stays
// the fallback — but a caller that knows the real pool now gets to say
// so instead of having its choice overwritten by a constant.
func resolvePool(poolName string) string {
	if p := strings.TrimSpace(poolName); p != "" {
		return p
	}
	return "default"
}

// buildBackupYAML builds a minimal Incus backup.yaml from pctInfo.
func buildBackupYAML(info pctInfo, poolName string) string {
	pool := resolvePool(poolName)
	cpu := "1"
	if info.cores != "" {
		cpu = info.cores
	}
	mem := "512"
	if info.memory != "" {
		mem = info.memory
	}

	return fmt.Sprintf(`container:
  architecture: %s
  config:
    limits.cpu: "%s"
    limits.memory: "%sMB"
  devices:
    root:
      path: /
      pool: %s
      type: disk
  name: %s
  profiles:
  - default
  stateful: false
  description: Imported from Proxmox vzdump
  ephemeral: false
  expanded_config:
    limits.cpu: "%s"
    limits.memory: "%sMB"
  expanded_devices:
    root:
      path: /
      pool: %s
      type: disk
  status: Stopped
volume:
  name: %s
  type: container
  content_type: filesystem
  config: {}
  pool: %s
  project: default
`, info.arch, cpu, mem, pool, info.name, cpu, mem, pool, info.name, pool)
}

// buildIndexYAML builds an Incus index.yaml with the flat top-level fields
// that backup.GetInfo() expects: name, backend, pool, type.
func buildIndexYAML(info pctInfo, poolName string) string {
	return fmt.Sprintf(`name: %s
backend: dir
pool: %s
type: container
`, info.name, resolvePool(poolName))
}

// incusBackupMeta is the subset of Incus backup.yaml we need.
type incusBackupMeta struct {
	Container *struct {
		Name   string            `yaml:"name"`
		Config map[string]string `yaml:"config"`
		Arch   string            `yaml:"architecture"`
	} `yaml:"container"`
}

// buildPctConf generates a minimal Proxmox pct.conf from Incus metadata.
func buildPctConf(meta incusBackupMeta) string {
	name := "webkvm-export"
	cores := "1"
	memory := "512"
	arch := "amd64"

	if meta.Container != nil {
		if meta.Container.Name != "" {
			name = meta.Container.Name
		}
		if meta.Container.Arch != "" {
			arch = meta.Container.Arch
		}
		cfg := meta.Container.Config
		if v := cfg["limits.cpu"]; v != "" {
			cores = v
		}
		if v := cfg["limits.memory"]; v != "" {
			// strip units (MB/GB) — pct.conf wants raw MB number
			v = strings.ToUpper(v)
			v = strings.TrimSuffix(v, "MB")
			v = strings.TrimSuffix(v, "GB") // rough; pct.conf uses MB
			memory = v
		}
	}

	return fmt.Sprintf(`# Exported by WebKVM from Incus container %s
# %s
arch: %s
cores: %s
hostname: %s
memory: %s
ostype: unmanaged
rootfs: local:subvol-%s-disk-0,size=8G
`, name, time.Now().UTC().Format(time.RFC3339), arch, cores, name, memory, name)
}

// writeTarString adds a single regular file entry to tw.
func writeTarString(tw *tar.Writer, name, content string) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     0644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("vzdump: write header %s: %w", name, err)
	}
	if _, err := io.WriteString(tw, content); err != nil {
		return fmt.Errorf("vzdump: write body %s: %w", name, err)
	}
	return nil
}

// openMaybeCompressed opens a (possibly gzip or zstd compressed) file and
// returns a ReadCloser of the raw (decompressed) stream.
type multiCloser struct {
	io.Reader
	closers []io.Closer
}

func (m *multiCloser) Close() error {
	var last error
	for i := len(m.closers) - 1; i >= 0; i-- {
		if err := m.closers[i].Close(); err != nil {
			last = err
		}
	}
	return last
}

func openMaybeCompressed(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	br := bufio.NewReaderSize(f, 4096)
	head, _ := br.Peek(4)

	mc := &multiCloser{closers: []io.Closer{f}}

	if len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := newGzipReader(br)
		if err != nil {
			f.Close()
			return nil, err
		}
		mc.closers = append(mc.closers, gz)
		mc.Reader = gz
		return mc, nil
	}
	if len(head) >= 4 && head[0] == 0x28 && head[1] == 0xb5 && head[2] == 0x2f && head[3] == 0xfd {
		dec, err := zstd.NewReader(br)
		if err != nil {
			f.Close()
			return nil, err
		}
		mc.closers = append(mc.closers, zstdCloser{dec})
		mc.Reader = dec
		return mc, nil
	}

	mc.Reader = br
	return mc, nil
}
