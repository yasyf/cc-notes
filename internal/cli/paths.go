package cli

import (
	"os"
	"path/filepath"
)

// cc-notes' own state lives under ~/.cc-notes/, mirroring cc-pool's ~/.cc-pool:
// one well-known mount-holder socket (a per-repo socket is impossible — the
// sun_path limit is ~104 bytes) plus the holder log beside it. One holder
// serves N repos; its registry keys on the mountpoint, each row carrying the
// repo root as base.

// mustHome resolves the user's home directory, panicking on failure. The whole
// CLI is unusable without a home dir, so a failure here is fatal — and it
// matches cc-pool's mustHome so the holder paths are derived identically.
func mustHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return h
}

// stateDir is cc-notes' private state directory (~/.cc-notes).
func stateDir() string {
	return filepath.Join(mustHome(), ".cc-notes")
}

// mountsSocketPath is the mount-holder's unix socket path
// (~/.cc-notes/mounts.sock). The holder holds <socket>.lock for its lifetime;
// see fusekit/mountd.Server.
func mountsSocketPath() string {
	return filepath.Join(stateDir(), "mounts.sock")
}

// mountHolderLogPath is where a spawned holder's stdout and stderr land
// (~/.cc-notes/mount-holder.log).
func mountHolderLogPath() string {
	return filepath.Join(stateDir(), "mount-holder.log")
}

// stableExecDir is where the mount holder's binary is materialized as a stable
// copy before spawning (~/.cc-notes/bin), so the holder's resolved executable
// path stays put across version upgrades and the macOS "Network Volumes" TCC
// grant survives them; see fusekit/mountd.RemoteHost.StableExecDir.
func stableExecDir() string {
	return filepath.Join(stateDir(), "bin")
}
