package fold_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

func yields(commits []model.PackCommit) func() (model.PackCommit, bool, error) {
	next := 0
	return func() (model.PackCommit, bool, error) {
		if next == len(commits) {
			return model.PackCommit{}, false, nil
		}
		next++
		return commits[next-1], true, nil
	}
}

func TestStreamMatchesFold(t *testing.T) {
	for _, tc := range extendCases() {
		t.Run(string(tc.kind), func(t *testing.T) {
			chain := extendChain(tc.create, tc.steps)
			want, err := fold.Fold(chain)
			if err != nil {
				t.Fatalf("Fold: %v", err)
			}
			got, err := fold.Stream(nil, yields(chain))
			if err != nil {
				t.Fatalf("Stream from the root: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Stream from the root:\n got  %+v\n want %+v", got, want)
			}
			strict, err := fold.Strict(chain)
			if err != nil {
				t.Fatalf("Strict: %v", err)
			}
			for k := 1; k < len(chain); k++ {
				prefix, err := fold.Strict(chain[:k])
				if err != nil {
					t.Fatalf("Strict(chain[:%d]): %v", k, err)
				}
				got, err := fold.Stream(roundTripped(t, prefix), yields(chain[k:]))
				if err != nil {
					t.Fatalf("Stream from cached Strict(chain[:%d]): %v", k, err)
				}
				if !reflect.DeepEqual(got, strict) {
					t.Fatalf("Stream from cached Strict(chain[:%d]):\n got  %+v\n want %+v", k, got, strict)
				}
			}
		})
	}
}

func TestStreamRefuses(t *testing.T) {
	chain := extendChain(model.CreateLedger{Nonce: "n", Title: "lg", Columns: []string{"state"}}, [][]model.Op{
		{model.UpsertRow{Key: "a", Fields: map[string]string{"state": "open"}}},
		{model.UpsertRow{Key: "b", Fields: map[string]string{"state": "open"}}},
	})
	head := string(chain[len(chain)-1].SHA)
	lamport := uint64(len(chain) + 1)
	row := model.UpsertRow{Key: "c", Fields: map[string]string{"state": "new"}}
	base, err := fold.Strict(chain)
	if err != nil {
		t.Fatalf("Strict: %v", err)
	}
	broken := errors.New("read failed")
	cases := []struct {
		name string
		base model.Snapshot
		next func() (model.PackCommit, bool, error)
		want error
	}{
		{"no create", nil, yields(nil), fold.ErrNoCreate},
		{"first commit not a root", nil, yields(chain[1:]), fold.ErrMissingParent},
		{"gap in the chain", nil, yields([]model.PackCommit{chain[0], chain[2]}), fold.ErrMissingParent},
		{"merge commit", base, yields([]model.PackCommit{mk("n1", []string{head, "other"}, "bob", 900, lamport, row)}), fold.ErrMissingParent},
		{"checkpoint commit", base, yields([]model.PackCommit{cp("n1", head, "compactor", 900, lamport, base, lamport-1, "c00", "c01", "c02")}), fold.ErrExtendCheckpoint},
		{"foreign op on a cached base", base, yields([]model.PackCommit{mk("n1", []string{head}, "bob", 900, lamport, model.Claim{Assignee: "bob"})}), fold.ErrKindMismatch},
		{"read error", base, func() (model.PackCommit, bool, error) { return model.PackCommit{}, false, broken }, broken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := fold.Stream(tc.base, tc.next); !errors.Is(err, tc.want) {
				t.Fatalf("Stream error = %v, want %v", err, tc.want)
			}
		})
	}
}
