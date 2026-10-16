package fold

import (
	"fmt"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

type countingFolder struct {
	*noteFolder
	applies *int
}

func (f countingFolder) apply(op model.Op, c model.PackCommit) bool {
	*f.applies++
	return f.noteFolder.apply(op, c)
}

func TestWalkAppliesEachOpOncePerSeed(t *testing.T) {
	const n = 200
	tests := []struct {
		name         string
		checkpointAt int
		wantFolders  int
		wantApplies  int
	}{
		{"no checkpoint folds one folder", -1, 1, n - 1},
		{"seed-safe checkpoint refolds once from its state", n / 2, 2, n - 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ordered := make([]model.PackCommit, n)
			ordered[0] = model.PackCommit{SHA: "c000", Author: "alice", AuthorTime: 100, Pack: model.Pack{Lamport: 1, Ops: []model.Op{model.CreateNote{Nonce: "n", Title: "T"}}}}
			for k := 1; k < n; k++ {
				var op model.Op = model.SetBody{Body: fmt.Sprint(k)}
				if k == tt.checkpointAt {
					state, err := foldNote(ordered[:k], tolerant)
					if err != nil {
						t.Fatalf("foldNote: %v", err)
					}
					covers := make([]model.SHA, k)
					for i := range covers {
						covers[i] = ordered[i].SHA
					}
					op = model.Checkpoint{EntityID: state.EntityID(), State: state, CoversLamport: ordered[k-1].Pack.Lamport, CoversShas: covers}
				}
				ordered[k] = model.PackCommit{
					SHA:        model.SHA(fmt.Sprintf("c%03d", k)),
					Parents:    []model.SHA{ordered[k-1].SHA},
					Author:     "actor",
					AuthorTime: int64(100 + k),
					Pack:       model.Pack{Lamport: model.Lamport(k + 1), Ops: []model.Op{op}},
				}
			}
			folders, applies, steps := 0, 0, 0
			err := walk(ordered, func() folder[model.Note] {
				folders++
				return countingFolder{noteFolder: newNoteFolder(), applies: &applies}
			}, func(st Step) error {
				if st.Commit.SHA != ordered[steps].SHA {
					t.Fatalf("step %d sha = %s, want %s", steps, st.Commit.SHA, ordered[steps].SHA)
				}
				steps++
				return nil
			})
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
			if steps != n || folders != tt.wantFolders || applies != tt.wantApplies {
				t.Fatalf("steps, folders, applies = %d, %d, %d; want %d, %d, %d (a prefix refold applies %d)",
					steps, folders, applies, n, tt.wantFolders, tt.wantApplies, n*(n-1)/2)
			}
		})
	}
}
