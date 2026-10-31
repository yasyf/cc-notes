package store

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestQueryNativeGenerationRapidWork(t *testing.T) {
	for _, count := range []int{32, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := initStore(t)
			signature := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
			packed := make([]string, 0, count+2)
			var selected, successor model.EntityID
			for i := range count + 2 {
				tip, err := s.Repo.WriteOpsCommit(t.Context(), nil, signature, "doc fixture", model.Pack{Lamport: 1, Ops: docOps(fmt.Sprintf("fixture %d", i))})
				if err != nil {
					t.Fatal(err)
				}
				id := model.EntityID(tip)
				switch i {
				case 0:
					selected = id
				case 1:
					successor = id
				}
				packed = append(packed, string(tip)+" "+refs.For(model.KindDoc, id))
			}
			slices.SortFunc(packed, func(a, b string) int { return strings.Compare(strings.Fields(a)[1], strings.Fields(b)[1]) })
			if err := os.WriteFile(filepath.Join(s.recordsCommonDir, "packed-refs"), []byte("# pack-refs with: peeled fully-peeled sorted \n"+strings.Join(packed, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			appendOps(t, s, refs.For(model.KindDoc, selected), model.AddSupersededBy{ID: successor})
			q, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
			if err != nil {
				t.Fatal(err)
			}
			if current, err := q.currentGeneration(t.Context()); err != nil || current == "" {
				if err != nil || os.Getenv("CCN_REQUIRE_NATIVE_REF_COUNTER") == "1" {
					t.Fatalf("recent native generation = %q %v", current, err)
				}
				t.Skip("filesystem does not qualify recent native generations")
			}
			var previous string
			for cycle := range 32 {
				appendOps(t, s, refs.For(model.KindDoc, selected), model.AddTag{Tag: fmt.Sprintf("cycle-%d", cycle)})
				incoming, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, selected)
				if err != nil || !hit || incoming != nil || !slices.Equal(heads, []model.EntityID{successor}) {
					t.Fatalf("cycle %d refreshed = %v %v %t %v", cycle, incoming, heads, hit, err)
				}
				generation, transaction := queryIndexGeneration(t, q)
				if generation == "" || generation == previous {
					t.Fatalf("cycle %d did not publish a changed native generation", cycle)
				}
				ctx := &queryWorkContext{Context: t.Context()}
				incoming, heads, hit, err = s.IndexedSupersession(ctx, model.KindDoc, selected)
				if err != nil || !hit || incoming != nil || !slices.Equal(heads, []model.EntityID{successor}) {
					t.Fatalf("cycle %d steady = %v %v %t %v", cycle, incoming, heads, hit, err)
				}
				checks := ctx.checks.Load()
				if checks > 32 {
					t.Fatalf("cycle %d walked %d unrelated docs: %d checkpoints", cycle, count, checks)
				}
				if afterGeneration, afterTransaction := queryIndexGeneration(t, q); afterGeneration != generation || afterTransaction != transaction {
					t.Fatalf("cycle %d steady query changed index state", cycle)
				}
				t.Logf("unrelated=%d cycle=%d steady_checks=%d", count, cycle, checks)
				previous = generation
			}
			fresh, err := Open(s.Git.Dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			ctx := &queryWorkContext{Context: t.Context()}
			_, heads, hit, err := fresh.IndexedSupersession(ctx, model.KindDoc, selected)
			if err != nil || !hit || !slices.Equal(heads, []model.EntityID{successor}) || ctx.checks.Load() > 32 {
				t.Fatalf("reopened query = %v %t %v checks=%d", heads, hit, err, ctx.checks.Load())
			}
		})
	}
}
