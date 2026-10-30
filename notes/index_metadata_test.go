package notes

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestTaskRelationsDoNotReadUnrelatedBodies(t *testing.T) {
	for _, count := range []int{8, 80} {
		for _, relation := range []string{"blocking", "blocking-batch", "children", "runs"} {
			t.Run(fmt.Sprintf("%s/%d", relation, count), func(t *testing.T) {
				c, dir := newWBClient(t)
				gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
				target := statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "target", Type: model.TypeTask}).EntityID()
				statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "selected", Type: model.TypeTask, Parent: target}, model.AddDep{ID: target})
				rb, _, err := c.CreateRunbook(t.Context(), RunbookSpec{Title: "selected", Steps: []string{"deploy"}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.StartRun(t.Context(), rb.ID, target); err != nil {
					t.Fatal(err)
				}
				var unrelated []model.Snapshot
				for i := range count {
					unrelated = append(unrelated,
						statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: fmt.Sprintf("other task %d", i), Type: model.TypeTask}),
						statusCreate(t, c, model.CreateRunbook{Nonce: model.NewNonce(), Title: fmt.Sprintf("other runbook %d", i)}),
					)
				}
				read := func(ctx context.Context, client *Client) (any, error) {
					switch relation {
					case "blocking":
						return client.TasksBlocking(ctx, target)
					case "blocking-batch":
						return client.TasksBlockingFor(ctx, []model.EntityID{target, target, "unknown"})
					case "children":
						return client.TaskChildren(ctx, target)
					case "runs":
						return client.TaskRuns(ctx, target)
					default:
						panic("unknown relation")
					}
				}
				want, err := read(t.Context(), c)
				if err != nil {
					t.Fatal(err)
				}
				if _, hit, err := c.s.StatusMetadata(t.Context(), model.KindTask, model.KindRunbook); err != nil || !hit {
					t.Fatalf("warm metadata = %v, %v", hit, err)
				}
				for _, snapshot := range unrelated {
					removeStatusCommit(t, c, snapshot.Meta().Head)
				}
				clearStatusFoldCache(t, c)
				fresh, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := fresh.s.ListTasks(t.Context()); err == nil {
					t.Fatal("unrelated task corpus unexpectedly readable")
				}
				if _, err := fresh.s.ListRunbooks(t.Context()); err == nil {
					t.Fatal("unrelated runbook corpus unexpectedly readable")
				}
				if relation == "blocking-batch" {
					if got, err := fresh.TasksBlockingFor(t.Context(), nil); err != nil || len(got) != 0 {
						t.Fatalf("empty selection read unrelated corpus: %#v, %v", got, err)
					}
				}
				got, err := read(t.Context(), fresh)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("warm targeted query with %d unreadable unrelated records per kind = %#v, %v; want %#v", count, got, err, want)
				}
				statusCreate(t, fresh, model.CreateTask{Nonce: model.NewNonce(), Title: "one changed task", Type: model.TypeTask})
				statusCreate(t, fresh, model.CreateRunbook{Nonce: model.NewNonce(), Title: "one changed runbook"})
				got, err = read(t.Context(), fresh)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("incremental targeted query = %#v, %v; want %#v", got, err, want)
				}
			})
		}
	}
}
