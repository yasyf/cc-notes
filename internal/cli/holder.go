package cli

import (
	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/fusekit/mountd"
)

// newMountHolderCmd is the hidden entry point for the detached mount-holder
// process spawned by the detached `mount` path (fusekit/mountd.Spawn). One
// holder serves every repo mounted on this machine over a single well-known
// socket; it outlives the CLI invocations that drive it.
func newMountHolderCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:    "mount-holder",
		Short:  "Run the detached fuse mount holder",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// HolderHost is nil in a non-fuse build; Server.Run refuses loudly.
			// Version is cc-notes' APP version (version.String()), NEVER
			// fusekit's: a future skew-compare against the holder's wire
			// Version must read this binary's version, not the library's.
			// Probe is nil — cc-notes has no daemon driving capability probes.
			s := &mountd.Server{
				Socket:  socket,
				Host:    fusefs.HolderHost(),
				Probe:   nil,
				Version: version.String(),
			}
			return s.Run(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&socket, "socket", mountsSocketPath(), "unix socket path to serve")
	return cmd
}
