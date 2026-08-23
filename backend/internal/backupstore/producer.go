package backupstore

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/klauspost/compress/zstd"

	"webkvm/internal/models"
)

// Linux-specific SEEK_DATA / SEEK_HOLE constants. These are
// the standard values for the lseek whence argument on
// Linux. Other Unix-like systems may differ; on macOS the
// values are the same (3 and 4) but the package doesn't
// export them. We hard-code to keep the producer simple —
// if we ever need to support a non-Linux platform without
// these, the fall-through in writeTarFileSparse will copy
// the file non-sparsely.
const (
	seekData = 3
	seekHole = 4
)

// This file is the per-VM archive producer. It is the single
// source of truth for the bytes of a "WebKVM backup of one VM":
//
//   domain.xml
//   disks/<disk-basename>           (one entry per non-cdrom disk)
//   snapshots/<snapname>.xml        (domainsnapshot XML, if any)
//   snapshots/<snapname>/<vol-file> (snapshot overlay volume, if any)
//   manifest.txt                    (name + disk<idx>=<source> lines)
//
// Two callers compose the same producer today:
//
//   * Runner.writeBackup (Phase II) — wraps the writer in
//     zstd.NewWriter → <target path>.tar.zst, one archive per VM.
//   * Connector.ExportDomain (Phase II-B4) — wraps the writer
//     in zstd.NewWriter → HTTP response body, one archive.
//
// Keeping both code paths funneled through this function means
// the bytes the operator downloads are byte-for-byte the same
// shape as the bytes the runner stores. Restoring either of
// them goes through the same `ImportDomain` path. This is the
// architectural reason D-over-C won out over a fused-endpoint
// design in the planning phase: a shared producer, two
// independent entry points, no behavioural coupling.

// SnapshotBackup holds the data needed to back up one libvirt
// snapshot: its <domainsnapshot> XML and the on-disk snapshot
// overlay volume (which is a qcow2 delta file sitting on top
// of the base disk or the previous snapshot in the chain).
type SnapshotBackup struct {
	// Name is the snapshot name (also the volume basename).
	Name string
	// DomainSnapshotXML is the raw <domainsnapshot> descriptor.
	DomainSnapshotXML string
	// VolumePath is the absolute path to the qcow2 overlay file
	// created when the snapshot was taken, typically
	// <pool>/<vmname>.<snapname>. May be empty if the volume
	// no longer exists on disk.
	VolumePath string
}

// VMBackup is the producer's input. The runner and ExportDomain
// each assemble one of these from their respective libvirt
// queries. The struct intentionally mirrors the producer's
// needs and nothing more — disk sources are absolute paths
// (the producer does not re-resolve against any dataDir).
type VMBackup struct {
	// ID is the VM name (or UUID) used in the manifest.
	ID string
	// DomainXML is the full libvirt <domain>...</domain>
	// descriptor. The producer writes it verbatim as
	// domain.xml; no transformation.
	DomainXML string
	// Disks is the list of file-backed disks to include.
	// Callers should already have filtered out cdroms and
	// any disks that live outside the libvirt pool (the
	// runner's collectVMFiles does this; ExportDomain's
	// parseDisksFiltered does this). The producer
	// re-checks the source path and skips anything that
	// no longer exists on disk — a disk can disappear
	// between the caller's query and the actual write.
	Disks []models.DiskInfo
	// Snapshots holds the per-VM snapshot metadata and
	// overlay volumes to include in the archive. The
	// runner populates this; ExportDomain leaves it empty
	// (the export endpoint exports only the current state).
	Snapshots []SnapshotBackup
}

// ProducerOpts configures the compression and disk-handling
// behaviour of the producer. The zero value means "zstd at
// level 19, no repack, no size cap" — which is what new
// archives should be by default.
type ProducerOpts struct {
	// Compression is "zstd" (default) or "gzip" (legacy).
	// The export UI exposes both for compatibility with
	// archives written by older WebKVM versions.
	Compression string
	// ZstdLevel is the zstd compression level (1..22).
	// 0 means "use the default" (19). Out-of-range values
	// are clamped, not rejected.
	ZstdLevel int
	// RepackDisks, when true, runs `qemu-img convert -c -O
	// qcow2` on each disk and streams the result into the
	// tar instead of the raw file. This is the "shrink by
	// 20-50%" path the export UI exposes. The runner
	// doesn't repack by default — the size cap is the
	// usual cost control.
	RepackDisks bool
	// DiskSizeLimit is a soft cap in bytes. Disks larger
	// than this are skipped (with a log line and a manifest
	// entry marking them as skipped). 0 = no cap. The
	// runner wires this to the per-target MaxFileSizeMB.
	DiskSizeLimit int64
}

// ProducerResult is what ProduceVMArchive returns after a
// successful write. Callers can log it or surface it in the
// per-job status.
type ProducerResult struct {
	// DisksIncluded is the number of disk files actually
	// written to the tar (excludes skipped-oversize and
	// skipped-missing).
	DisksIncluded int
	// DisksSkipped counts disks that the producer chose
	// not to write: missing on disk, oversize, or a tar
	// write error mid-archive. The producer returns the
	// error and the caller decides whether the whole
	// archive is a failure.
	DisksSkipped int
	// TotalBytes is the size of the produced tar on the
	// wire, including the compressor overhead. Useful for
	// updating Job.Size.
	TotalBytes int64
}

// ProduceVMArchive writes a per-VM backup of `vm` to `w`. The
// writer is wrapped in the configured compressor; the
// resulting bytes are the per-VM archive. Both Runner.writeBackup
// (Phase II-B2) and Connector.ExportDomain (Phase II-B4) call
// this with the same arguments and get the same bytes.
//
// Errors abort the write and are returned to the caller. A
// partial archive may have been written to `w` at that point;
// the caller decides whether to discard the partial result.
// (Runner.writeBackup removes the half-written file on
// failure; ExportDomain relies on the response stream
// being closed by the client.)
func ProduceVMArchive(ctx context.Context, vm VMBackup, opts ProducerOpts, w io.Writer) (ProducerResult, error) {
	// Wrap w in the compressor. The compressor is the
	// outermost layer; the tar writer goes inside it.
	cw, cclose, err := newCompressor(w, opts.Compression, opts.ZstdLevel)
	if err != nil {
		return ProducerResult{}, err
	}
	// Track bytes written for the result. We count the
	// post-tar stream (what the caller will see on disk /
	// on the wire) so the caller doesn't have to stat
	// the file separately.
	counter := &countingWriter{w: cw}
	tw := tar.NewWriter(counter)
	defer func() {
		_ = tw.Close()
		_ = cclose()
	}()

	if err := writeTarEntry(tw, "domain.xml", []byte(vm.DomainXML), 0o644); err != nil {
		return ProducerResult{}, fmt.Errorf("write domain.xml: %w", err)
	}

	manifest := strings.Builder{}
	manifest.WriteString("name=" + vm.ID + "\n")

	// Write snapshot metadata and overlay volumes. Each
	// snapshot produces two tar entries when the volume
	// exists on disk:
	//
	//   snapshots/<name>.xml         — <domainsnapshot> XML
	//   snapshots/<name>/<basename>  — qcow2 overlay file
	//
	// If the volume is missing, only the XML is written
	// (the operator can still inspect the snapshot tree,
	// but restoring that point requires the volume).
	//
	// A synthetic snapshot named "_base" carries the root
	// base disk of the backing chain (no XML, just the volume).
	res := ProducerResult{}
	for _, snap := range vm.Snapshots {
		if snap.Name == "" {
			continue
		}
		// Write the <domainsnapshot> XML for real snapshots.
		// The synthetic "_base" entry has no XML.
		if snap.Name != "_base" && snap.DomainSnapshotXML != "" {
			xmlArc := fmt.Sprintf("snapshots/%s.xml", snap.Name)
			if err := writeTarEntry(tw, xmlArc, []byte(snap.DomainSnapshotXML), 0o644); err != nil {
				return res, fmt.Errorf("write snapshot %s xml: %w", snap.Name, err)
			}
			manifest.WriteString(fmt.Sprintf("snapshot=%s\n", snap.Name))
		}
		if snap.VolumePath != "" {
			if fi, err := os.Stat(snap.VolumePath); err == nil && !fi.IsDir() {
				volArc := fmt.Sprintf("snapshots/%s/%s", snap.Name, filepath.Base(snap.VolumePath))
				if err := writeDiskToTar(tw, snap.VolumePath, volArc); err != nil {
					return res, fmt.Errorf("write snapshot %s volume: %w", snap.Name, err)
				}
				res.DisksIncluded++
			}
		}
	}

	for i, d := range vm.Disks {
		// Honor cancellation between disks so a client
		// disconnect doesn't keep reading the next disk.
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if d.Source == "" {
			continue
		}
		fi, err := os.Stat(d.Source)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				slog.Warn("backup_producer_disk_missing",
					"vm", vm.ID, "source", d.Source)
				res.DisksSkipped++
				continue
			}
			return res, fmt.Errorf("stat disk %s: %w", d.Source, err)
		}
		if fi.IsDir() {
			// Not a regular file. CDROMs that point at a
			// directory of ISOs are excluded by the
			// callers, but defend in depth.
			res.DisksSkipped++
			continue
		}
		if opts.DiskSizeLimit > 0 && fi.Size() > opts.DiskSizeLimit {
			slog.Warn("backup_producer_disk_oversize",
				"vm", vm.ID, "source", d.Source,
				"size", fi.Size(), "limit", opts.DiskSizeLimit)
			manifest.WriteString(fmt.Sprintf("disk%d=%s  SKIPPED (oversize: %d > %d)\n",
				i, d.Source, fi.Size(), opts.DiskSizeLimit))
			res.DisksSkipped++
			continue
		}
		manifest.WriteString(fmt.Sprintf("disk%d=%s\n", i, d.Source))
		base := filepath.Base(d.Source)
		arcPath := "disks/" + base
		if opts.RepackDisks {
			if err := streamRepackedDisk(ctx, tw, d.Source, arcPath); err != nil {
				return res, fmt.Errorf("repack disk %s: %w", d.Source, err)
			}
		} else {
			if err := writeDiskToTar(tw, d.Source, arcPath); err != nil {
				return res, fmt.Errorf("write disk %s: %w", d.Source, err)
			}
		}
		res.DisksIncluded++
	}
	if err := writeTarEntry(tw, "manifest.txt", []byte(manifest.String()), 0o644); err != nil {
		return res, fmt.Errorf("write manifest.txt: %w", err)
	}
	res.TotalBytes = counter.n
	return res, nil
}

// newCompressor wraps w in the requested compressor. "zstd"
// (and the empty default) gives zstd at opts.ZstdLevel;
// "gzip" gives gzip at default level. Anything else is an
// error so a typo in opts.Compression surfaces immediately
// rather than silently writing an uncompressed tar.
func newCompressor(w io.Writer, kind string, zstdLevel int) (io.Writer, func() error, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "zstd":
		level := zstdLevel
		if level <= 0 {
			level = 19
		}
		if level > 22 {
			level = 22
		}
		zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)))
		if err != nil {
			return nil, nil, err
		}
		return zw, zw.Close, nil
	case "gzip":
		gz := gzip.NewWriter(w)
		return gz, gz.Close, nil
	default:
		return nil, nil, fmt.Errorf("unsupported compression %q (use zstd or gzip)", kind)
	}
}

// writeTarEntry writes a single file entry to tw with the
// given name, contents, and mode. The name must not contain
// ".." (defence in depth — the caller controls the names but
// an accidental ".." would be silently archived and then
// escape on extract).
func writeTarEntry(tw *tar.Writer, name string, data []byte, mode int64) error {
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid tar entry name %q (must not contain ..)", name)
	}
	hdr := &tar.Header{
		Name:     name,
		Mode:     mode,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if len(data) > 0 {
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// writeDiskToTar writes srcPath into the tar as arcName. The
// default is non-sparse (the file is read straight through);
// the sparse variant below does SEEK_DATA / SEEK_HOLE to
// avoid copying qcow2 holes. For now the producer uses the
// non-sparse path; the runner doesn't need sparse (the size
// cap drops the large disks anyway) and the export already
// has qemu-img convert for true block-level compaction.
func writeDiskToTar(tw *tar.Writer, srcPath, arcName string) error {
	return writeTarFileSparse(tw, srcPath, arcName, false)
}

// writeFileToTar writes the file at srcPath as a single tar
// entry named arcName. Used for the small text files
// (users.json, config.json, audit.log, etc.) that go into
// the config archive.
//
// The implementation reads the entire file into memory in
// one call before writing the tar header. This is the A5
// fix for the production bug surfaced by the Phase II
// release: the previous version did f.Stat() (to get the
// tar header size) followed by io.Copy(tw, f) (to write the
// body). If the file was truncated between those two calls
// — most commonly by logrotate running while the backup was
// in flight, or by the running backend writing to
// audit.log — io.Copy returned EOF before writing all the
// declared bytes, and the next WriteHeader failed with
// "archive/tar: missed writing N bytes" (where N was the
// truncated tail, commonly 4096 for one block). Reading
// the file fully first closes the race: the bytes we read
// are the bytes we declare in the header.
//
// Memory: this path is for config files only, all of which
// are small (the audit.log cap is 10 MiB). A 10 MiB buffer
// in memory is fine. The VM disk path (writeDiskToTar)
// continues to use streaming io.Copy because a 100 GiB qcow2
// does not fit in memory.
func writeFileToTar(tw *tar.Writer, srcPath, arcName string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name:     arcName,
		Mode:     0o644,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	return nil
}

// writeTarFileSparse writes srcPath into the tar archive.
// When sparse is true, the file is written using
// SEEK_DATA / SEEK_HOLE lseek so qcow2 holes are not
// written (saves significant bytes on thinly-provisioned
// disks). The tar header is rewritten for each segment.
// When sparse is false, the file is written as a single
// tar entry, which is simpler but copies any holes.
func writeTarFileSparse(tw *tar.Writer, srcPath, arcName string, sparse bool) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name:     arcName,
		Mode:     0o644,
		Size:     info.Size(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !sparse {
		_, err = io.Copy(tw, f)
		return err
	}
	// Sparse path: walk the file in (data, hole) segments.
	// The first segment is always data (or EOF). We use
	// SEEK_DATA / SEEK_HOLE to find the boundaries; on
	// platforms that don't support these, fall back to
	// the non-sparse copy.
	const chunkSize = 1 << 20 // 1 MiB read chunks
	buf := make([]byte, chunkSize)
	pos := int64(0)
	for {
		dataStart, err := syscall.Seek(int(f.Fd()), pos, seekData)
		if err != nil {
			// No more data segments from `pos` onwards.
			// Either EOF or a hole all the way to the end.
			break
		}
		// Seek to the next hole to find the segment's end.
		holeStart, err := syscall.Seek(int(f.Fd()), dataStart, seekHole)
		if err != nil {
			// Couldn't find a hole — assume the rest of
			// the file is data.
			holeStart = info.Size()
		}
		segLen := holeStart - dataStart
		if _, err := f.Seek(dataStart, io.SeekStart); err != nil {
			return err
		}
		// Write `segLen` bytes from f into tw in chunkSize
		// blocks.
		remaining := segLen
		for remaining > 0 {
			n := int64(chunkSize)
			if remaining < n {
				n = remaining
			}
			nn, err := io.ReadFull(f, buf[:n])
			if err != nil {
				return err
			}
			if _, err := tw.Write(buf[:nn]); err != nil {
				return err
			}
			remaining -= int64(nn)
		}
		pos = holeStart
		if pos >= info.Size() {
			break
		}
	}
	return nil
}

// streamRepackedDisk runs `qemu-img convert -c -O qcow2` on
// srcPath and pipes the stdout (the re-packed qcow2 bytes)
// directly into the tar as arcName. The disk file is never
// reified on the host's filesystem; it streams end-to-end
// from qemu-img through Go to the tar writer. This is the
// "shrink the backup by 20-50%" path the export UI exposes
// via `?compress=1`.
//
// If qemu-img is not on PATH, the helper returns an error;
// the producer surfaces it to the caller (the runner
// ignores the failure and the operator sees it in the
// backup job).
func streamRepackedDisk(ctx context.Context, tw *tar.Writer, srcPath, arcName string) error {
	qemuImg, err := exec.LookPath("qemu-img")
	if err != nil {
		return fmt.Errorf("qemu-img not on PATH (required for RepackDisks): %w", err)
	}
	// Tar header first, then stream the body.
	hdr := &tar.Header{
		Name:     arcName,
		Mode:     0o644,
		Size:     0, // unknown until qemu-img finishes
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, qemuImg, "convert", "-c", "-O", "qcow2", srcPath, "-")
	cmd.Stdout = tw
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("qemu-img convert: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// countingWriter counts the number of bytes that pass through
// it. Used by ProduceVMArchive to populate ProducerResult.TotalBytes
// without requiring the caller to stat the output file.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
