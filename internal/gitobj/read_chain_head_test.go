package gitobj

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestReadChainFromCommitMatchesFullBFS(t *testing.T) {
	dir, _, _ := indexedCommitFixture(t)
	repo := openPackedRepo(t, dir)
	sig := Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1_700_000_000, 0)}
	write := func(parents []model.SHA, lamport model.Lamport, op model.Op) model.SHA {
		t.Helper()
		sha, err := repo.WriteOpsCommit(t.Context(), parents, sig, "head reuse fixture", model.Pack{Lamport: lamport, Ops: []model.Op{op}})
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}
	root := write(nil, 1, model.CreateNote{Nonce: model.NewNonce(), Title: "root"})
	left := write([]model.SHA{root}, 2, model.AddTag{Tag: "left"})
	right := write([]model.SHA{root}, 2, model.AddTag{Tag: "right"})
	merge := write([]model.SHA{left, right}, 3, model.SetTitle{Title: "merge"})
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/notes/fixture", string(merge))
	for _, tip := range []model.SHA{root, left, merge} {
		head, err := repo.ReadCommit(t.Context(), tip)
		if err != nil {
			t.Fatal(err)
		}
		if repo.storage.scope != nil {
			t.Fatal("head probe retained native scope")
		}
		gittest.Git(t, dir, "repack", "-adq")
		got, err := repo.ReadChainFromCommit(t.Context(), head)
		if err != nil {
			t.Fatal(err)
		}
		want, err := repo.ReadChain(t.Context(), tip)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("reused chain = %#v; full chain = %#v, %v", got, want, err)
		}
		if repo.storage.scope != nil {
			t.Fatal("chain reuse retained native scope")
		}
	}
}

func TestReadChainFromCommitMissingParentAndCancellation(t *testing.T) {
	for _, mode := range []string{"missing", "restore", "backfill-error", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			dir, common, _ := indexedCommitFixture(t)
			repo := openPackedRepo(t, dir)
			sig := Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1_700_000_000, 0)}
			root, err := repo.WriteOpsCommit(t.Context(), nil, sig, "root", model.Pack{Lamport: 1, Ops: []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "root"}}})
			if err != nil {
				t.Fatal(err)
			}
			child, err := repo.WriteOpsCommit(t.Context(), []model.SHA{root}, sig, "child", model.Pack{Lamport: 2, Ops: []model.Op{model.AddTag{Tag: "child"}}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(common, "objects", string(root)[:2], string(root)[2:])
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			repo = openPackedRepo(t, dir)
			head, err := repo.ReadCommit(t.Context(), child)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			calls := 0
			failed := errors.New("fixture backfill failure")
			if mode != "missing" {
				repo.BackfillWith(func(ctx context.Context, tip model.SHA) error {
					calls++
					if tip != child {
						t.Fatalf("backfill tip = %s, want %s", tip, child)
					}
					if mode == "backfill-error" {
						return failed
					}
					return os.WriteFile(path, data, 0o600)
				})
			}
			ctx := t.Context()
			if mode == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			chain, err := repo.ReadChainFromCommit(ctx, head)
			switch mode {
			case "restore":
				if err != nil || len(chain) != 2 || chain[0].SHA != child || chain[1].SHA != root || calls != 1 {
					t.Fatalf("restored chain = %#v, %v, calls=%d", chain, err, calls)
				}
			case "backfill-error":
				if !errors.Is(err, ErrIncompleteChain) || !errors.Is(err, failed) || calls != 1 {
					t.Fatalf("backfill failure = %v, calls=%d", err, calls)
				}
			case "missing":
				if !errors.Is(err, ErrIncompleteChain) || calls != 0 {
					t.Fatalf("missing parent = %v, calls=%d", err, calls)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) || calls != 0 {
					t.Fatalf("cancelled chain = %v, calls=%d", err, calls)
				}
			}
			if repo.storage.scope != nil {
				t.Fatal("failed or restored chain retained native scope")
			}
		})
	}
}
