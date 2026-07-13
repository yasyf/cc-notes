package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// kindSpec binds a snapshot type to the vocabulary the noun command groups
// share: its entity kind, a generic resolve-and-fold load, and the
// mutation-echo printer that renders a just-written snapshot.
type kindSpec[T model.Snapshot] struct {
	kind  model.Kind
	print func(cmd *cobra.Command, s *store.Store, v T, jsonOut bool) error
}

// load resolves an id prefix to its ref and folds the entity chain into T.
func (k kindSpec[T]) load(ctx context.Context, s *store.Store, prefix string) (string, T, error) {
	var zero T
	ref, err := s.Resolve(ctx, k.kind, prefix)
	if err != nil {
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
	docSpec  = kindSpec[model.Doc]{kind: model.KindDoc, print: func(cmd *cobra.Command, s *store.Store, d model.Doc, jsonOut bool) error {
		return printDoc(cmd, s, d, "", jsonOut)
	}}
	logSpec     = kindSpec[model.Log]{kind: model.KindLog, print: printLog}
	taskSpec    = kindSpec[model.Task]{kind: model.KindTask, print: printTask}
	sprintSpec  = kindSpec[model.Sprint]{kind: model.KindSprint, print: printSprint}
	projectSpec = kindSpec[model.Project]{kind: model.KindProject, print: printProject}
	runbookSpec = kindSpec[model.Runbook]{kind: model.KindRunbook, print: func(cmd *cobra.Command, _ *store.Store, rb model.Runbook, jsonOut bool) error {
		return printRunbook(cmd, rb, jsonOut)
	}}
)
