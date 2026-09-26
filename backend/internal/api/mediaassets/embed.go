package mediaassets

import (
	"embed"
	"io/fs"
)

// FS embeds the bundled OS/distribution logos and brand assets that ship with WebKVM.
// They are copied into the media pool's protected "system" directory on
// first run so the UI can reference them via /api/media/system:<name>/raw
// without depending on frontend static assets.
//
//go:embed *.svg *.jpg
var FS embed.FS

// Names returns every embedded asset filename (os-linux.svg, ...).
func Names() []string {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

// Read returns the bytes of a named embedded asset.
func Read(name string) ([]byte, error) {
	return FS.ReadFile(name)
}
