package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/notes"
)

// newGCCmd builds "cc-notes gc": local maintenance. By default it tidies local
// state only — erasing fold-cache entries whose tip is no longer any entity ref
// tip, trimming the derived caches to their byte budgets, and retiring derived
// state no current binary reads — and touches no remote. A sync that moves refs
// runs the same tidy on its own.
// With --prune-remote it additionally physically deletes tombstoned note, doc,
// log, runbook, investigation, plan, and answer refs locally and on the default remote
// via `git push --delete`. Physical prune is best-effort and non-convergent — a
// stale clone that never saw the delete re-advertises the ref on its next push —
// which is why it is opt-in and never part of normal sync.
func newGCCmd() *cobra.Command {
	var pruneRemote bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Tidy local state; --prune-remote physically deletes tombstoned refs",
		Long: "Local maintenance. By default it tidies local state only: it erases fold-cache\n" +
			"entries whose tip is no longer any entity ref tip, trims the fold and relevance\n" +
			"caches to their byte budgets, deletes cache generations and object indexes no\n" +
			"current binary has touched in a day, and reaps the per-user state of deleted\n" +
			"clones; no network. A sync that moves refs runs the same tidy on its own.\n\n" +
			"--prune-remote additionally deletes tombstoned note, doc, log, runbook,\n" +
			"investigation, plan, and answer refs locally and on the default remote via\n" +
			"`git push --delete`. This is best-effort and non-convergent:\n" +
			"a stale clone that never saw the delete re-advertises the ref on its next push.\n" +
			"That is why it is opt-in and never part of normal sync.",
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := openClient(cmd)
			if err != nil {
				return err
			}
			report, err := c.GC(ctx, notes.GCOptions{PruneRemote: pruneRemote})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(out, gcDTO{Tidied: report.Tidied, Retired: report.Retired, Pruned: report.Pruned, Failed: report.Failed})
			}
			for _, line := range []struct {
				verb   string
				count  int
				always bool
			}{
				{"pruned", report.Pruned, false},
				{"failed", report.Failed, false},
				{"tidied", report.Tidied, true},
				{"retired", report.Retired, false},
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

// gcDTO fixes the JSON field order for a gc report: local entries tidied,
// derived state retired, and the tombstoned refs pruned and failed under
// --prune-remote (both zero without it).
type gcDTO struct {
	Tidied  int `json:"tidied"`
	Retired int `json:"retired"`
	Pruned  int `json:"pruned"`
	Failed  int `json:"failed"`
}
