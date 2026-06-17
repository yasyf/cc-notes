// Package plugin embeds the Claude Code plugin assets — the using-cc-notes
// skill tree and the CI workflow template — so the binary can install them into
// a target repository without shipping separate files.
package plugin

import "embed"

// Files holds the embedded skills/ and workflows/ trees. The plain //go:embed
// form skips _- and .-prefixed entries.
//
//go:embed skills workflows
var Files embed.FS
