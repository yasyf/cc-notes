package gitobj

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestNativeReadCommitClosesScopeAndSeesLaterPacks(t *testing.T) {
	dir, _, missingOps := indexedCommitFixture(t)
	repo := openPackedRepo(t, dir)
	backfills := 0
	repo.BackfillWith(func(context.Context, model.SHA) error { backfills++; return nil })
	sig := Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1_700_000_000, 0)}
	root, err := repo.WriteOpsCommit(t.Context(), nil, sig, "root", model.Pack{Lamport: 1, Ops: []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "root"}}})
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/notes/fixture", string(root))
	gittest.Git(t, dir, "repack", "-adq")
	for _, tip := range []model.SHA{"invalid", "1111111111111111111111111111111111111111", model.SHA(missingOps.String())} {
		if _, err := repo.ReadCommit(t.Context(), tip); err == nil {
			t.Fatalf("accepted unreadable commit %s", tip)
		}
		if repo.storage.scope != nil {
			t.Fatal("failed read retained its native scope")
		}
	}
	if backfills != 0 {
		t.Fatal("one-commit probe invoked history backfill")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := repo.ReadCommit(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled commit = %v", err)
	}
	if repo.storage.scope != nil {
		t.Fatal("cancelled read retained its native scope")
	}
	first, err := repo.ReadCommit(t.Context(), root)
	if err != nil || first.SHA != root {
		t.Fatalf("fresh scope = %#v, %v", first, err)
	}
	if repo.storage.scope != nil {
		t.Fatal("successful read retained its native scope")
	}
	child, err := repo.WriteOpsCommit(t.Context(), []model.SHA{root}, sig, "later", model.Pack{Lamport: 2, Ops: []model.Op{model.AddTag{Tag: "later"}}})
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/notes/fixture", string(child), string(root))
	gittest.Git(t, dir, "repack", "-adq")
	actual, err := repo.ReadCommit(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := repo.ReadChain(t.Context(), child)
	if err != nil || len(chain) != 2 || !reflect.DeepEqual(actual, chain[0]) {
		t.Fatalf("later commit = %#v; chain = %#v, %v", actual, chain, err)
	}
	if repo.storage.scope != nil {
		t.Fatal("later pack read retained its native scope")
	}
}
