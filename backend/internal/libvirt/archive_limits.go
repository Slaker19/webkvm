package libvirt

import (
	"fmt"
	"io"
	"syscall"
)

// Decompression / extraction limits for untrusted archives (audit #24).

// maxOVFBytes bounds the OVF descriptor read from an OVA.
const maxOVFBytes = 4 << 20

// poolReserveBytes is the free space left untouched in the destination
// filesystem while importing.
const poolReserveBytes = 1 << 30

// readAllLimited reads at most max bytes from r and fails when the source
// holds more.
func readAllLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("archive entry exceeds %d bytes", max)
	}
	return b, nil
}

// copyLimited copies at most max bytes from src to dst and fails when src
// holds more.
func copyLimited(dst io.Writer, src io.Reader, max int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, max+1))
	if err == nil && n > max {
		err = fmt.Errorf("archive entry exceeds %d bytes", max)
	}
	return n, err
}

// diskBudget tracks how many bytes an import may still write into a
// filesystem: the space free when the import started minus a reserve. A
// hostile archive therefore cannot fill the pool (or the volume holding it).
type diskBudget struct {
	remaining int64
}

func newDiskBudget(dir string) *diskBudget {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return &diskBudget{remaining: -1} // unknown: do not enforce
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	rem := free - poolReserveBytes
	if rem < 0 {
		rem = 0
	}
	return &diskBudget{remaining: rem}
}

// take consumes n bytes from the budget and returns the cap to apply to the
// copy (n itself), or an error when the archive entry cannot fit.
func (b *diskBudget) take(n int64) error {
	if b == nil || b.remaining < 0 {
		return nil
	}
	if n > b.remaining {
		return fmt.Errorf("archive entry of %d bytes does not fit in the destination (%d bytes available after reserve)", n, b.remaining)
	}
	b.remaining -= n
	return nil
}
