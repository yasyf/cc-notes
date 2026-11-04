package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

func TestSourceBulkSnapshotMatchesIncrementalRoots(t *testing.T) {
	s := initStore(t)
	project := create(t, s, []model.Op{model.CreateProject{Nonce: model.NewNonce(), Title: "project"}})
	sprint := create(t, s, []model.Op{model.CreateSprint{Nonce: model.NewNonce(), Title: "sprint", Project: project.EntityID()}})
	create(t, s, []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: "task", Type: model.TypeTask}, model.SetSprint{Sprint: sprint.EntityID()}, model.SetProject{Project: project.EntityID()}})
	note := create(t, s, noteOps("attachment"))
	oid := strings.Repeat("a", 64)
	attach(t, s, refs.For(model.KindNote, note.EntityID()), "trace", oid, 1)
	other := create(t, s, docOps("conflicting attachment size"))
	attach(t, s, refs.For(model.KindDoc, other.EntityID()), "trace", oid, 2)
	deleted := create(t, s, noteOps("deleted"))
	if _, err := s.Append(t.Context(), refs.For(model.KindNote, deleted.EntityID()), []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatal(err)
	}
	expected, err := s.SourceHead(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		index := sourcegraph.New(objects)
		entities := make(map[string]sourceview.Entity)
		if err := index.Prefix(expected.EntityRoot, "", func(entry sourcegraph.Entry) error {
			var entity sourceview.Entity
			if err := json.Unmarshal(entry.Value, &entity); err != nil {
				return err
			}
			entities[entry.Key] = entity
			return nil
		}); err != nil {
			return err
		}
		actual, err := s.buildSourceSnapshot(objects, expected, entities)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("bulk revision=%+v incremental=%+v", actual, expected)
		}
		return sourcegraph.NewRetention(objects).Validate(sourcegraph.RetentionRoot{Index: actual.RetentionIndex, Commit: actual.RetentionRoot})
	}); err != nil {
		t.Fatal(err)
	}
}
