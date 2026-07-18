package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// kindSpec binds a snapshot type to the vocabulary the noun command groups
// share: its entity kind, a generic resolve-and-fold load, and the
// mutation-echo printer that renders a just-written snapshot.
type kindSpec[T model.Snapshot] struct {
	kind  model.Kind
	print func(cmd *cobra.Command, c *notes.Client, v T, jsonOut bool) error
}

// load resolves an id prefix to its ref and folds the entity chain into T.
func (k kindSpec[T]) load(ctx context.Context, s *store.Store, prefix string) (string, T, error) {
	var zero T
	ref, err := s.Resolve(ctx, k.kind, prefix)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", zero, crossKindHint(ctx, s, k.kind, prefix, err)
		}
		return "", zero, err
	}
	snap, err := s.Load(ctx, ref)
	if err != nil {
		return "", zero, err
	}
	return ref, snap.(T), nil
}

var (
	noteSpec = kindSpec[model.Note]{kind: model.KindNote, print: printNote}
	docSpec  = kindSpec[model.Doc]{kind: model.KindDoc, print: func(cmd *cobra.Command, c *notes.Client, d model.Doc, jsonOut bool) error {
		return printDoc(cmd, c, d, "", jsonOut)
	}}
	logSpec     = kindSpec[model.Log]{kind: model.KindLog, print: printLog}
	taskSpec    = kindSpec[model.Task]{kind: model.KindTask, print: printTask}
	sprintSpec  = kindSpec[model.Sprint]{kind: model.KindSprint, print: printSprint}
	projectSpec = kindSpec[model.Project]{kind: model.KindProject, print: printProject}
	runbookSpec = kindSpec[model.Runbook]{kind: model.KindRunbook, print: func(cmd *cobra.Command, _ *notes.Client, rb model.Runbook, jsonOut bool) error {
		return printRunbook(cmd, rb, jsonOut)
	}}
	investigationSpec = kindSpec[model.Investigation]{kind: model.KindInvestigation, print: printInvestigation}
)
