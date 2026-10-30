package store

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func TestQueryMetadataTaskRelationProjection(t *testing.T) {
	task := model.Task{ID: "task", Parent: "parent", BlockedBy: []model.EntityID{"blocker", "blocker"}}
	row := projectQueryMetadata("binding", "tip", task)
	task.BlockedBy[0] = "changed"
	if row.Parent != "parent" || !slices.Equal(row.BlockedBy, []model.EntityID{"blocker", "blocker"}) {
		t.Fatalf("task relation projection = %#v", row)
	}
	runbook := model.Runbook{ID: "runbook", Runs: []model.RunbookRun{
		{ID: "running", Task: "b", Status: model.RunRunning},
		{ID: "finished", Task: "a", Status: model.RunSucceeded},
		{ID: "repeated", Task: "b", Status: model.RunFailed},
		{ID: "uncited", Task: "", Status: model.RunAbandoned},
	}}
	row = projectQueryMetadata("binding", "tip", runbook)
	runbook.Runs[1].Task = "changed"
	if !row.HasRunningRun || !slices.Equal(row.RunTasks, []model.EntityID{"", "a", "b"}) {
		t.Fatalf("run task projection = %#v", row)
	}
}

func TestQueryMetadataTaskPostingsMatchSnapshots(t *testing.T) {
	db := newQueryMetadataDB(t)
	tasks := []model.Task{
		{ID: "root", BlockedBy: []model.EntityID{"target", "target"}},
		{ID: "child", Parent: "target", Status: model.StatusDone},
		{ID: "both", Parent: "target", BlockedBy: []model.EntityID{"target"}},
		{ID: "nul", Parent: "target\x00", BlockedBy: []model.EntityID{"target\x00"}},
		{ID: "deleted", Parent: "target", BlockedBy: []model.EntityID{"target"}, Deleted: true},
	}
	runbooks := []model.Runbook{
		{ID: "first", Runs: []model.RunbookRun{{Task: "target"}, {Task: "target"}, {Task: ""}}},
		{ID: "second", Runs: []model.RunbookRun{{Task: "other"}}},
		{ID: "nul-runbook", Runs: []model.RunbookRun{{Task: "target\x00"}}},
		{ID: "deleted-runbook", Runs: []model.RunbookRun{{Task: "target"}}, Deleted: true},
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, task := range tasks {
			if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", task)); err != nil {
				return err
			}
		}
		for _, runbook := range runbooks {
			if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", runbook)); err != nil {
				return err
			}
		}
		return replaceQueryMetadata(tx, projectQueryMetadata("binding\x00other", "tip", model.Task{ID: "other-binding", Parent: "target", BlockedBy: []model.EntityID{"target"}}))
	}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []model.EntityID{"target", "other", "target\x00", "missing", ""} {
		var blocks, children, runs []model.EntityID
		for _, task := range tasks {
			if task.Deleted {
				continue
			}
			if slices.Contains(task.BlockedBy, target) {
				blocks = append(blocks, task.ID)
			}
			if task.Parent == target {
				children = append(children, task.ID)
			}
		}
		for _, runbook := range runbooks {
			if runbook.Deleted {
				continue
			}
			for _, run := range runbook.Runs {
				if run.Task == target {
					runs = append(runs, runbook.ID)
					break
				}
			}
		}
		for _, tc := range []struct {
			name string
			walk func(*bolt.Tx, string, model.EntityID, func([]byte) error) error
			want []model.EntityID
		}{
			{"blocked by", walkQueryMetadataBlockedBy, blocks},
			{"parent", walkQueryMetadataTaskChildren, children},
			{"run task", walkQueryMetadataRunTasks, runs},
		} {
			slices.Sort(tc.want)
			got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func([]byte) error) error {
				return tc.walk(tx, "binding", target, visit)
			})
			if !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("%s target %q = %v, want %v", tc.name, target, got, tc.want)
			}
		}
	}
}

func TestQueryMetadataTaskPostingsReplaceDeleteRollback(t *testing.T) {
	for _, kind := range []model.Kind{model.KindTask, model.KindRunbook} {
		t.Run(string(kind), func(t *testing.T) {
			db := newQueryMetadataDB(t)
			makeRow := func(target model.EntityID, deleted bool) queryMetadata {
				if kind == model.KindTask {
					return projectQueryMetadata("binding", "tip", model.Task{ID: "source", Parent: target, BlockedBy: []model.EntityID{target, target}, Deleted: deleted})
				}
				return projectQueryMetadata("binding", "tip", model.Runbook{ID: "source", Runs: []model.RunbookRun{{Task: target}, {Task: target}}, Deleted: deleted})
			}
			walks := []func(*bolt.Tx, string, model.EntityID, func([]byte) error) error{walkQueryMetadataRunTasks}
			if kind == model.KindTask {
				walks = []func(*bolt.Tx, string, model.EntityID, func([]byte) error) error{walkQueryMetadataBlockedBy, walkQueryMetadataTaskChildren}
			}
			assertTarget := func(target model.EntityID, want []model.EntityID) {
				t.Helper()
				for _, walk := range walks {
					got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func([]byte) error) error { return walk(tx, "binding", target, visit) })
					if !slices.Equal(got, want) {
						t.Fatalf("target %q = %v, want %v", target, got, want)
					}
				}
			}
			if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, makeRow("old", false)) }); err != nil {
				t.Fatal(err)
			}
			rollback := errors.New("rollback")
			if err := db.Update(func(tx *bolt.Tx) error {
				if err := replaceQueryMetadata(tx, makeRow("new", false)); err != nil {
					return err
				}
				return rollback
			}); !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			assertTarget("old", []model.EntityID{"source"})
			assertTarget("new", nil)
			if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, makeRow("new", false)) }); err != nil {
				t.Fatal(err)
			}
			assertTarget("old", nil)
			assertTarget("new", []model.EntityID{"source"})
			if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, makeRow("new", true)) }); err != nil {
				t.Fatal(err)
			}
			assertTarget("new", nil)
			if err := db.Update(func(tx *bolt.Tx) error { return deleteQueryMetadata(tx, "binding", "source") }); err != nil {
				t.Fatal(err)
			}
			if err := db.View(func(tx *bolt.Tx) error {
				binding := queryMetadataBinding(tx, "binding")
				for _, bucket := range [][]byte{queryMetadataBlockedByBucket, queryMetadataParentsBucket, queryMetadataRunTasksBucket} {
					if key, _ := binding.Bucket(bucket).Cursor().First(); key != nil {
						t.Fatalf("delete retained %s posting: %x", bucket, key)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQueryMetadataTaskPostingsReadOnlyMatchingRows(t *testing.T) {
	for _, count := range []int{8, 80} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			db := newQueryMetadataDB(t)
			if err := db.Update(func(tx *bolt.Tx) error {
				for i := range count {
					for _, snapshot := range []model.Snapshot{
						model.Task{ID: model.EntityID(fmt.Sprintf("task-%d", i)), Parent: "other", BlockedBy: []model.EntityID{"other"}},
						model.Runbook{ID: model.EntityID(fmt.Sprintf("runbook-%d", i)), Runs: []model.RunbookRun{{Task: "other"}}},
					} {
						row := projectQueryMetadata("binding", "tip", snapshot)
						if err := replaceQueryMetadata(tx, row); err != nil {
							return err
						}
						if err := queryMetadataBinding(tx, "binding").Bucket(queryMetadataRowsBucket).Put([]byte(row.ID), []byte("invalid unrelated JSON")); err != nil {
							return err
						}
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for _, walk := range []func(*bolt.Tx, string, model.EntityID, func([]byte) error) error{walkQueryMetadataBlockedBy, walkQueryMetadataTaskChildren, walkQueryMetadataRunTasks} {
				if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func([]byte) error) error { return walk(tx, "binding", "absent", visit) }); got != nil {
					t.Fatalf("absent target visited unrelated rows: %v", got)
				}
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				row := projectQueryMetadata("binding", "tip", model.Task{ID: "selected", Parent: "selected", BlockedBy: []model.EntityID{"selected"}})
				return replaceQueryMetadata(tx, row)
			}); err != nil {
				t.Fatal(err)
			}
			for _, walk := range []func(*bolt.Tx, string, model.EntityID, func([]byte) error) error{walkQueryMetadataBlockedBy, walkQueryMetadataTaskChildren} {
				if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func([]byte) error) error { return walk(tx, "binding", "selected", visit) }); !slices.Equal(got, []model.EntityID{"selected"}) {
					t.Fatalf("selected target = %v", got)
				}
				stop := errors.New("visitor stopped")
				if err := db.View(func(tx *bolt.Tx) error {
					return walk(tx, "binding", "selected", func([]byte) error { return stop })
				}); !errors.Is(err, stop) {
					t.Fatalf("visitor error = %v", err)
				}
			}
		})
	}
}

func TestQueryMetadataTaskRelationRejectsDigestCollision(t *testing.T) {
	db := newQueryMetadataDB(t)
	if err := db.Update(func(tx *bolt.Tx) error {
		row := projectQueryMetadata("binding", "tip", model.Task{ID: "wrong", Parent: "other", BlockedBy: []model.EntityID{"other"}})
		if err := replaceQueryMetadata(tx, row); err != nil {
			return err
		}
		binding := queryMetadataBinding(tx, "binding")
		for _, name := range [][]byte{queryMetadataBlockedByBucket, queryMetadataParentsBucket} {
			if err := binding.Bucket(name).Put(queryMetadataPostingKey(queryMetadataSupersedersPrefix(model.KindTask, "target"), row), nil); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, walk := range []func(*bolt.Tx, string, model.EntityID, func([]byte) error) error{walkQueryMetadataBlockedBy, walkQueryMetadataTaskChildren} {
		if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func([]byte) error) error { return walk(tx, "binding", "target", visit) }); got != nil {
			t.Fatalf("digest collision returned wrong relation: %v", got)
		}
	}
}

func TestQueryMetadataTaskRelationBucketsAreRequired(t *testing.T) {
	db := newQueryMetadataDB(t)
	for _, name := range [][]byte{queryMetadataBlockedByBucket, queryMetadataParentsBucket, queryMetadataRunTasksBucket} {
		if err := db.Update(func(tx *bolt.Tx) error {
			binding, err := ensureQueryMetadataBinding(tx, "binding")
			if err != nil {
				return err
			}
			if !queryMetadataBucketsComplete(binding) {
				t.Fatal("new binding is incomplete")
			}
			if err := binding.DeleteBucket(name); err != nil {
				return err
			}
			if queryMetadataBucketsComplete(binding) {
				t.Fatalf("missing %s bucket considered complete", name)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
