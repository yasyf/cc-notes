package store

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestStructuralEntityTombstonesAreDurableAndHidden(t *testing.T) {
	for _, test := range []struct {
		name string
		kind model.Kind
		ops  []model.Op
	}{
		{name: "task", kind: model.KindTask, ops: taskOps("Task", "main")},
		{name: "sprint", kind: model.KindSprint, ops: sprintOps("Sprint")},
		{name: "project", kind: model.KindProject, ops: projectOps("Project")},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := initStore(t)
			created := create(t, source, test.ops)
			deleted, err := source.Append(t.Context(), refs.For(test.kind, created.EntityID()), []model.Op{model.DeleteNote{}})
			if err != nil {
				t.Fatalf("Append tombstone: %v", err)
			}
			if !deleted.Meta().Deleted {
				t.Fatal("tombstone did not fold into structural entity metadata")
			}
			live, err := source.ListSnapshots(t.Context(), test.kind, ListOpts{})
			if err != nil || len(live) != 0 {
				t.Fatalf("live snapshots = %d err=%v", len(live), err)
			}
			all, err := source.ListSnapshots(t.Context(), test.kind, ListOpts{IncludeDeleted: true})
			if err != nil || len(all) != 1 || !all[0].Meta().Deleted {
				t.Fatalf("all snapshots = %+v err=%v", all, err)
			}
		})
	}
}
