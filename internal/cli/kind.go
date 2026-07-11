package cli

import (
	"context"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// kindSpec binds a snapshot type to its entity kind, giving the noun command
// groups one generic resolve-and-fold path in place of a per-kind loader.
type kindSpec[T model.Snapshot] struct {
	kind model.Kind
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
	noteSpec    = kindSpec[model.Note]{kind: model.KindNote}
	docSpec     = kindSpec[model.Doc]{kind: model.KindDoc}
	logSpec     = kindSpec[model.Log]{kind: model.KindLog}
	taskSpec    = kindSpec[model.Task]{kind: model.KindTask}
	sprintSpec  = kindSpec[model.Sprint]{kind: model.KindSprint}
	projectSpec = kindSpec[model.Project]{kind: model.KindProject}
	runbookSpec = kindSpec[model.Runbook]{kind: model.KindRunbook}
)
