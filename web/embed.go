//go:build webui

// Package web carries the compiled cc-notes viz single-page app. The `webui`
// build tag embeds the Vite build output under dist/ into the binary, so a
// release build serves the UI with no filesystem dependency; the default
// (untagged) build compiles embed_stub.go instead and ships no assets. CI
// builds dist/ with `npm run build` before compiling with -tags webui, and the
// _fuse/_webui release assets carry it; a plain `go build` never needs dist to
// be present.
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
