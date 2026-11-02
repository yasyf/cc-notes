//go:build darwin

package fusefs

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/sourcedriver"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

func TestGitDriverCachedMutationHistoryCost(t *testing.T) {
	if os.Getenv("CCN_REQUIRE_HELPER_DRIVER_HISTORY_COST") != "1" {
		t.Skip("hosted actual driver history proof")
	}
	for _, depth := range []int{32, 1024} {
		t.Run(strconv.Itoa(depth), func(t *testing.T) {
			driver, tenant, source := newGitDriverTest(t)
			signature := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1_700_000_000, 0).UTC()}
			var root, tip model.SHA
			for position := range depth {
				var parents []model.SHA
				var ops []model.Op
				if position == 0 {
					ops = []model.Op{model.CreateNote{Nonce: "0123456789abcdef0123456789abcdef", Title: "Before", Body: "initial body"}}
				} else {
					parents = []model.SHA{tip}
					ops = []model.Op{model.SetBody{Body: fmt.Sprintf("history body %08d", position)}}
				}
				var err error
				tip, err = source.Repo.WriteOpsCommit(t.Context(), parents, signature, "driver history fixture", model.Pack{Lamport: model.Lamport(position + 1), Ops: ops})
				if err != nil {
					t.Fatal(err)
				}
				if position == 0 {
					root = tip
				}
			}
			ref := refs.For(model.KindNote, model.EntityID(root))
			if err := source.RecordsGit.UpdateRef(t.Context(), ref, tip, ""); err != nil {
				t.Fatal(err)
			}
			if err := source.Reindex(t.Context()); err != nil {
				t.Fatal(err)
			}
			snapshot, err := source.LoadAt(t.Context(), tip)
			if err != nil {
				t.Fatal(err)
			}
			note := snapshot.(model.Note)
			key := "entity:note:" + string(root)
			head := refreshDriver(t, driver, tenant.Authority)
			object := sourceLocator(tenant, key, 3)
			parent := sourceLocator(tenant, "kind:note", 3)
			body := sourceview.NewNoteTemplate("After", nil, nil)
			request := mutationRequest(t, tenant, "aa", head.Revision, catalog.SourceMutationContext{Operation: writableFileOperation(catalog.MutationRevise, sourceview.Filename(note), true), Object: &object, Parent: &parent}, body)
			ctx, work := gitobj.WithReadWork(t.Context())
			ctx, graph := sourcegraph.WithWork(ctx)
			started := time.Now()
			receipt, err := driver.ApplyMutation(ctx, tenant.Authority, request, newMemorySource(body))
			elapsed := time.Since(started)
			if err != nil {
				t.Fatal(err)
			}
			listings, entries, decodes := work.Snapshot()
			graphWork := graph.Snapshot()
			result := struct {
				Depth                                int
				ElapsedNS                            int64
				RefListings, RefEntries, PackDecodes int64
				GraphWork                            sourcegraph.Work
			}{depth, int64(elapsed), listings, entries, decodes, graphWork}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.State != sourcedriver.MutationApplied || receipt.Expected != head.Revision || receipt.Result != sourcedriver.LogicalID(key) {
				t.Fatalf("receipt=%+v", receipt)
			}
			current, err := source.Load(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if current.EntityID() != note.ID || current.(model.Note).Title != "After" || current.(model.Note).Head == tip {
				t.Fatalf("snapshot=%+v", current)
			}
			chain, err := source.Repo.ReadChain(t.Context(), current.(model.Note).Head)
			if err != nil {
				t.Fatal(err)
			}
			complete, err := fold.Strict(chain)
			if err != nil || !reflect.DeepEqual(current, complete) {
				t.Fatalf("driver snapshot differs from full fold: %v", err)
			}
			t.Logf("GIT_DRIVER_HISTORY_COST_RESULT: %s", encoded)
			if listings != 0 || entries != 0 || decodes > 8 || graphWork.NodeReads > 1024 || graphWork.KeysVisited > 64 {
				t.Fatalf("actual driver history cost regression: %s", encoded)
			}
		})
	}
}
