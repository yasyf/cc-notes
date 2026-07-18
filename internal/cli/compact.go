package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// newCompactCmd builds "cc-notes compact ID": collapse an entity's op-log into
// a checkpoint so future folds seed from it instead of replaying every op. The
// id and the full folded state are preserved; the covered objects stay in the
// object database. Compaction is local-only — it never pushes — so it skips the
// remote auto-install other mutations run.
func newCompactCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "compact ID",
		Short: "Collapse an entity's op-log into a checkpoint",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			kind, id, err := c.ResolveEntity(ctx, args[0])
			if err != nil {
				return err
			}
			snap, err := s.Compact(ctx, refs.For(kind, id))
			if err != nil {
				return err
			}
			switch v := snap.(type) {
			case model.Note:
				return printNote(cmd, c, v, jsonOut)
			case model.Doc:
				return printDoc(cmd, c, v, "", jsonOut)
			case model.Log:
				return printLog(cmd, c, v, jsonOut)
			case model.Task:
				return printTask(cmd, c, v, jsonOut)
			case model.Sprint:
				return printSprint(cmd, c, v, jsonOut)
			case model.Project:
				return printProject(cmd, c, v, jsonOut)
			case model.Runbook:
				return printRunbook(cmd, v, jsonOut)
			case model.Investigation:
				return printInvestigation(cmd, c, v, jsonOut)
			default:
				panic(fmt.Sprintf("compact: unexpected snapshot %T", snap))
			}
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}
