package cli

import (
	"os"
	"path/filepath"

	"github.com/yasyf/fusekit/mountd"
)

// cc-notes keeps its managed mountpoints under ~/.cc-notes/mnt; the shared
// fusekit-holder cask (its own socket ~/.fusekit/holder.sock) serves them as a
// tenant, and cc-notes' content server (contentd) renders the tree over a bridge
// data socket under ~/.fusekit/spool/cc-notes. cc-notes hosts no holder of its
// own — it is a plain tenant of the shared holder.

// mustHome resolves the user's home directory, panicking on failure. The whole
// CLI is unusable without a home dir, so a failure here is fatal.
func mustHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return h
}

// stateDir is cc-notes' private state directory (~/.cc-notes); it homes the
// managed mountpoints under mnt/.
func stateDir() string {
	return filepath.Join(mustHome(), ".cc-notes")
}

// holderSocket resolves the effective mount-holder socket: an explicit --socket
// (chosen != "") wins; otherwise cc-notes dials the shared fusekit-holder cask's
// default socket.
func holderSocket(chosen string) string {
	if chosen != "" {
		return chosen
	}
	return mountd.DefaultHolderSocket()
}

// legacyPrivateHolderSocket is where cc-notes' RETIRED private in-process mount
// holder bound its socket (~/.cc-notes/mounts.sock). Current cc-notes never
// binds it; the incumbent check dials it to detect a pre-cutover holder still
// serving the same mounts (see refuseIncumbentHolder).
func legacyPrivateHolderSocket() string {
	return filepath.Join(stateDir(), "mounts.sock")
}

// fusekitSpoolDir is cc-notes' bridge spool directory under the shared fusekit
// state root (~/.fusekit/spool/cc-notes). contentd binds its content socket
// here; the shared holder dials it in tree mode.
func fusekitSpoolDir() string {
	return filepath.Join(mustHome(), ".fusekit", "spool", holderOwner)
}

// contentSocketPath is contentd's bridge data socket
// (~/.fusekit/spool/cc-notes/c.sock), the ContentSocket every cc-notes tree
// mount points the holder at.
func contentSocketPath() string {
	return filepath.Join(fusekitSpoolDir(), "c.sock")
}
