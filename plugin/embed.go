// Package plugin embeds the Claude Code plugin's CI workflow template so the
// binary can install it into a target repository without shipping a separate
// file. The skill tree under skills/ is served to Claude Code through the
// marketplace (source ./plugin), not the binary, so it is not embedded.
package plugin

import "embed"

// Files holds the embedded workflows/ tree. The plain //go:embed form skips
// _- and .-prefixed entries.
//
//go:embed workflows
var Files embed.FS
