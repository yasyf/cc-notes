package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/notes"
)

// newGCCmd builds "cc-notes gc": local maintenance. By default it tidies local
// state only — pruning fold-cache entries whose tip is no longer any entity ref
// tip, orphaned by appends, compaction, and merges — and touches no remote.
// With --prune-remote it additionally physically deletes tombstoned note refs
// locally and on the default remote via `git push --delete`. Physical prune is
// best-effort and non-convergent — a stale clone that never saw the delete
// re-advertises the ref on its next push — which is why it is opt-in and never
// part of normal sync.
func newGCCmd() *cobra.Command {
	var pruneRemote bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Tidy local state; --prune-remote physically deletes tombstoned refs",
		Long: "Local maintenance. By default it tidies local state only, pruning fold-cache\n" +
			"entries whose tip is no longer any entity ref tip; no network.\n\n" +
			"--prune-remote additionally deletes tombstoned note refs locally and on the\n" +
			"default remote via `git push --delete`. This is best-effort and non-convergent:\n" +
			"a stale clone that never saw the delete re-advertises the ref on its next push.\n" +
			"That is why it is opt-in and never part of normal sync.",
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := openClient()
			if err != nil {
				return err
			}
			report, err := c.GC(ctx, notes.GCOptions{PruneRemote: pruneRemote})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(out, gcDTO{Tidied: report.Tidied, Pruned: report.Pruned, Failed: report.Failed})
			}
			for _, line := range []struct {
				verb   string
				count  int
				always bool
			}{
				{"pruned", report.Pruned, false},
				{"failed", report.Failed, false},
				{"tidied", report.Tidied, true},
			} {
				if !line.always && line.count == 0 {
					continue
				}
				if _, err := fmt.Fprintf(out, "%s: %d\n", line.verb, line.count); err != nil {
					return err
				}
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&pruneRemote, "prune-remote", false, "physically delete tombstoned refs on the remote (best-effort, non-convergent)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// gcDTO fixes the JSON field order for a gc report: local entries tidied, and
// the tombstoned refs pruned and failed under --prune-remote (both zero
// without it).
type gcDTO struct {
	Tidied int `json:"tidied"`
	Pruned int `json:"pruned"`
	Failed int `json:"failed"`
}
