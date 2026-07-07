// Package web carries the compiled cc-notes viz single-page app. The `webui`
// build tag embeds the Vite build output under dist/ into the binary, so a
// release build serves the UI with no filesystem dependency; the default
// (untagged) build compiles embed_stub.go instead and ships no assets. CI
// builds dist/ with `npm run build` before compiling with -tags webui, and the
// _fuse/_webui release assets carry it; a plain `go build` never needs dist to
// be present.
package web
