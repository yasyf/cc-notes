package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
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
			ref, err := resolveEntity(ctx, s, args[0])
			if err != nil {
				return err
			}
			snap, err := s.Compact(ctx, ref)
			if err != nil {
				return err
			}
			switch v := snap.(type) {
			case model.Note:
				return printNote(cmd, v, jsonOut)
			case model.Task:
				return printTask(cmd, s, v, jsonOut)
			default:
				return fmt.Errorf("compact: unexpected snapshot %T", snap)
			}
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// resolveEntity expands a kind-agnostic id prefix into a ref. An entity is a
// note or a task and ids are globally unique, so it resolves against both
// namespaces; a prefix that matches one entity in each is ambiguous and fails
// with an *AmbiguousError listing both.
func resolveEntity(ctx context.Context, s *store.Store, prefix string) (string, error) {
	noteRef, noteErr := s.Resolve(ctx, refs.KindNote, prefix)
	if noteErr != nil && !errors.Is(noteErr, store.ErrNotFound) {
		return "", noteErr
	}
	taskRef, taskErr := s.Resolve(ctx, refs.KindTask, prefix)
	if taskErr != nil && !errors.Is(taskErr, store.ErrNotFound) {
		return "", taskErr
	}
	switch {
	case noteErr == nil && taskErr == nil:
		return "", ambiguousAcrossKinds(ctx, s, prefix, noteRef, taskRef)
	case noteErr == nil:
		return noteRef, nil
	case taskErr == nil:
		return taskRef, nil
	default:
		return "", fmt.Errorf("%w: no note or task matches %q", store.ErrNotFound, prefix)
	}
}

func ambiguousAcrossKinds(ctx context.Context, s *store.Store, prefix, noteRef, taskRef string) error {
	candidates := make([]store.Candidate, 0, 2)
	for _, ref := range []string{noteRef, taskRef} {
		snap, err := s.Load(ctx, ref)
		if err != nil {
			return err
		}
		title := ""
		switch v := snap.(type) {
		case model.Note:
			title = v.Title
		case model.Task:
			title = v.Title
		}
		candidates = append(candidates, store.Candidate{ID: snap.EntityID(), Title: title})
	}
	return &store.AmbiguousError{Kind: refs.KindNote, Prefix: prefix, Candidates: candidates}
}
