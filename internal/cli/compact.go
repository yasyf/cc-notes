package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/refs"
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
			case model.Doc:
				return printDoc(cmd, v, "", jsonOut)
			case model.Log:
				return printLog(cmd, v, jsonOut)
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
// note, a task, a doc, or a log and ids are globally unique, so it resolves
// against all four namespaces; a prefix that matches an entity in more than one
// is ambiguous and fails with an *AmbiguousError listing each match.
func resolveEntity(ctx context.Context, s *store.Store, prefix string) (string, error) {
	noteRef, noteErr := s.Resolve(ctx, refs.KindNote, prefix)
	if noteErr != nil && !errors.Is(noteErr, store.ErrNotFound) {
		return "", noteErr
	}
	taskRef, taskErr := s.Resolve(ctx, refs.KindTask, prefix)
	if taskErr != nil && !errors.Is(taskErr, store.ErrNotFound) {
		return "", taskErr
	}
	docRef, docErr := s.Resolve(ctx, refs.KindDoc, prefix)
	if docErr != nil && !errors.Is(docErr, store.ErrNotFound) {
		return "", docErr
	}
	logRef, logErr := s.Resolve(ctx, refs.KindLog, prefix)
	if logErr != nil && !errors.Is(logErr, store.ErrNotFound) {
		return "", logErr
	}
	matched := make([]string, 0, 4)
	if noteErr == nil {
		matched = append(matched, noteRef)
	}
	if taskErr == nil {
		matched = append(matched, taskRef)
	}
	if docErr == nil {
		matched = append(matched, docRef)
	}
	if logErr == nil {
		matched = append(matched, logRef)
	}
	switch len(matched) {
	case 0:
		return "", fmt.Errorf("%w: no note, task, doc, or log matches %q", store.ErrNotFound, prefix)
	case 1:
		return matched[0], nil
	default:
		return "", ambiguousAcrossKinds(ctx, s, prefix, matched)
	}
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
		}
		candidates = append(candidates, store.Candidate{ID: snap.EntityID(), Title: title})
	}
	return &store.AmbiguousError{Kind: refs.KindNote, Prefix: prefix, Candidates: candidates}
}
