package cli

import (
	"os"
	"path/filepath"

	"github.com/yasyf/fusekit/mountd"
)

// casklessEnvVar opts a machine without the shared fusekit-holder cask into
// cc-notes' own self-exec mount holder. Any non-empty value selects it.
const casklessEnvVar = "CC_NOTES_CASKLESS_HOLDER"

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
// grant survives them; see fusekit/mountd.RemoteHost.StableExecDir. Used only
// by the cask-less holder mode — the shared cask holder is already stable-path.
func stableExecDir() string {
	return filepath.Join(stateDir(), "bin")
}

// casklessEnv reports whether the private, cask-less mount holder is selected
// via the environment (CC_NOTES_CASKLESS_HOLDER set to any non-empty value).
func casklessEnv() bool {
	return os.Getenv(casklessEnvVar) != ""
}

// holderSocket resolves the effective mount-holder socket for the selected
// mode. An explicit --socket (chosen != "") wins; otherwise the shared cask
// holder binds mountd.DefaultHolderSocket() and the private cask-less holder
// binds ~/.cc-notes/mounts.sock.
func holderSocket(chosen string, caskless bool) string {
	switch {
	case chosen != "":
		return chosen
	case caskless:
		return mountsSocketPath()
	default:
		return mountd.DefaultHolderSocket()
	}
}
