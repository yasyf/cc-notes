//go:build webui

package web

import "embed"

// Dist is the built single-page app rooted at dist/, with dist/index.html the
// SPA entrypoint. The viz server subtrees it with fs.Sub and serves it with an
// index.html fallback for client-side routes.
//
//go:embed all:dist
var Dist embed.FS

// Embedded reports whether the web UI assets are compiled into this binary. It
// is true in the webui build, so the viz server serves the SPA rather than the
// inline no-UI page.
const Embedded = true
