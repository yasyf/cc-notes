package store

import (
	"fmt"
	"reflect"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func TestQueryTaskBlockingBatchWork(t *testing.T) {
	for _, count := range []int{32, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			db := newQueryMetadataDB(t)
			if err := db.Update(func(tx *bolt.Tx) error {
				for i := range count {
					task := model.Task{ID: model.EntityID(fmt.Sprintf("other-%05d", i)), Labels: []string{"other"}}
					if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", task)); err != nil {
						return err
					}
				}
				for _, task := range []model.Task{
					{ID: "z-done", Status: model.StatusDone, Branch: "elsewhere", Labels: []string{"outside"}, BlockedBy: []model.EntityID{"target"}},
					{ID: "a-cancelled", Status: model.StatusCancelled, Labels: []string{"outside"}, BlockedBy: []model.EntityID{"target"}},
					{ID: "b-other", BlockedBy: []model.EntityID{"second"}},
					{ID: "deleted", Deleted: true, BlockedBy: []model.EntityID{"target"}},
				} {
					if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", task)); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			ctx := &queryCardinalityWorkContext{Context: t.Context()}
			if err := db.View(func(tx *bolt.Tx) error {
				got, err := queryTasksBlockingFor(ctx, tx, queryMetadataSource{binding: "binding", kind: model.KindTask}, []model.EntityID{"target", "second", "target", "unknown"})
				want := map[model.EntityID][]model.EntityID{"target": {"a-cancelled", "z-done"}, "second": {"b-other"}, "unknown": nil}
				if err == nil && !reflect.DeepEqual(got, want) {
					t.Fatalf("selected blocking map = %#v, want %#v", got, want)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if ctx.checks != 7 {
				t.Fatalf("selected relation work=%d, want 7", ctx.checks)
			}
			t.Logf("unrelated=%d selected_unique=3 selected_edges=3 relation_context_checks=%d", count, ctx.checks)
		})
	}
}
