// Package plugin embeds the Claude Code plugin assets — the using-cc-notes
// skill tree and the capt-hook enforcement hooks — so the binary can install
// them into a target repository without shipping separate files.
package plugin

import "embed"

// Files holds the embedded skills/ and hooks/ trees. The plain //go:embed
// form skips _- and .-prefixed entries, so __pycache__ never ships.
//
//go:embed skills hooks
var Files embed.FS
