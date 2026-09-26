package vzdump

import (
	"compress/gzip"
	"io"

	"github.com/klauspost/compress/zstd"
)

// newGzipReader wraps r in a gzip.Reader and returns it as an io.ReadCloser.
func newGzipReader(r io.Reader) (io.ReadCloser, error) {
	return gzip.NewReader(r)
}

// zstdCloser wraps a *zstd.Decoder so it satisfies io.Closer (whose
// Close must return error). zstd.Decoder.Close() returns nothing.
type zstdCloser struct{ d *zstd.Decoder }

func (z zstdCloser) Close() error { z.d.Close(); return nil }
