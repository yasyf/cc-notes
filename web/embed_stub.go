//go:build !webui

package web

import "embed"

// Dist is empty in the default build: the web UI is not compiled in, so no
// assets are embedded and the viz server serves the inline no-UI page instead.
var Dist embed.FS

// Embedded reports whether the web UI assets are compiled into this binary. It
// is false in the default build, so the viz server never reaches for dist/ and
// a plain `go build` needs no dist directory present.
const Embedded = false
