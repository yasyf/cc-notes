package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

func newAttachmentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attachment",
		Short: "Read attachment content from the local git-lfs store",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newAttachmentGetCmd(),
		newAttachmentPathCmd(),
	)
	return cmd
}

func newAttachmentGetCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "get ID NAME",
		Short: "Stream an attachment's content to stdout (or -o PATH)",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			ctx := cmd.Context()
			_, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			kind, id, err := c.ResolveAttachable(ctx, args[0])
			if err != nil {
				return err
			}
			att, content, err := c.OpenAttachment(ctx, kind, id, args[1])
			if err != nil {
				return err
			}
			defer func() { _ = content.Close() }()
			dest := cmd.OutOrStdout()
			if output != "" {
				//nolint:gosec // G304: output is the CLI's own -o output target for attachment get, taken by design.
				out, cerr := os.Create(output)
				if cerr != nil {
					return fmt.Errorf("write %s: %w", output, cerr)
				}
				// A write-side close can surface a deferred flush failure
				// (full disk, failed fsync); fold it into the result when the
				// copy itself succeeded, so a truncated -o file never exits 0.
				defer func() {
					if closeErr := out.Close(); closeErr != nil && err == nil {
						err = fmt.Errorf("write %s: %w", output, closeErr)
					}
				}()
				dest = out
			}
			if _, err = io.Copy(dest, content); err != nil {
				return fmt.Errorf("read attachment %s: %w", att.Name, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to PATH instead of stdout")
	return cmd
}

func newAttachmentPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path ID NAME",
		Short: "Print an attachment's absolute object path for zero-copy reads",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			kind, id, err := c.ResolveAttachable(ctx, args[0])
			if err != nil {
				return err
			}
			_, path, err := c.AttachmentPath(ctx, kind, id, args[1])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		},
	}
}
