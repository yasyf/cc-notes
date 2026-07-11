package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
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
			s, err := openStore()
			if err != nil {
				return err
			}
			ref, err := resolveAnyEntity(ctx, s, args[0])
			if err != nil {
				return err
			}
			snap, err := s.Compact(ctx, ref)
			if err != nil {
				return err
			}
			switch v := snap.(type) {
			case model.Note:
				return printNote(cmd, s, v, jsonOut)
			case model.Doc:
				return printDoc(cmd, s, v, "", jsonOut)
			case model.Log:
				return printLog(cmd, s, v, jsonOut)
			case model.Task:
				return printTask(cmd, s, v, jsonOut)
			case model.Sprint:
				return printSprint(cmd, s, v, jsonOut)
			case model.Project:
				return printProject(cmd, s, v, jsonOut)
			case model.Runbook:
				return printRunbook(cmd, v, jsonOut)
			default:
				panic(fmt.Sprintf("compact: unexpected snapshot %T", snap))
			}
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func ambiguousAcrossKinds(ctx context.Context, s *store.Store, prefix string, matched []string) error {
	candidates := make([]store.Candidate, 0, len(matched))
	for _, ref := range matched {
		snap, err := s.Load(ctx, ref)
		if err != nil {
			return err
		}
		title := ""
		switch v := snap.(type) {
		case model.Note:
			title = v.Title
		case model.Doc:
			title = v.Title
		case model.Log:
			title = v.Title
		case model.Task:
			title = v.Title
		case model.Sprint:
			title = v.Title
		case model.Project:
			title = v.Title
		case model.Runbook:
			title = v.Title
		}
		candidates = append(candidates, store.Candidate{ID: snap.EntityID(), Title: title})
	}
	return &store.AmbiguousError{Kind: model.KindNote, Prefix: prefix, Candidates: candidates}
}
