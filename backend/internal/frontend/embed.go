// Package frontend exposes the embedded Svelte SPA assets so they can be
// served by the API on the same port. The dist/ directory is populated
// by the build pipeline (see Makefile, target `build`).
package frontend

import "embed"

//go:embed all:dist
var FS embed.FS
