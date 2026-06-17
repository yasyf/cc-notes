//go:build !fuse

package fusefs

import "github.com/yasyf/fusekit/mountd"

// HolderHost returns nil in a non-fuse build: this binary has no in-process
// fuse host, so it cannot serve mounts. The mount holder's Server.Run refuses
// to start on a nil Host, loudly and immediately — a pure binary can still
// drive a running holder over the socket, it just cannot BE one.
func HolderHost() mountd.Host { return nil }
