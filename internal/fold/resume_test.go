package fold_test

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

func TestResumeEqualsTheRootFoldAcrossAMergeAboveTheCheckpoint(t *testing.T) {
	row := func(key, state string) model.UpsertRow {
		return model.UpsertRow{Key: key, Fields: map[string]string{"state": state}, Position: key}
	}
	covered := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "desk", Columns: []string{"state"}}),
		mk("b01", []string{"aaa"}, "bob", 110, 2, row("r1", "open")),
		mk("b02", []string{"b01"}, "bob", 120, 3, row("r2", "open"), model.AddLabel{Label: "hot"}),
		mk("b03", []string{"b02"}, "carol", 130, 4, model.RemoveRow{Key: "r1"}, model.AddComment{Body: "dropped r1"}),
		mk("b04", []string{"b03"}, "carol", 140, 5, row("r3", "open")),
	}
	suffix := []model.PackCommit{
		mk("m01", []string{"b04"}, "dave", 150, 6, row("r2", "merged")),
		mk("m02", []string{"m01"}, "dave", 160, 8, model.SetColumns{Columns: []string{"state", "owner"}}),
		mk("s01", []string{"b02"}, "erin", 150, 6, row("r2", "closed"), row("r1", "reopened")),
		mk("s02", []string{"s01"}, "erin", 170, 7, model.AddComment{Body: "side"}, model.RemoveLabel{Label: "hot"}),
		mk("mrg", []string{"m02", "s02"}, "dave", 180, 9),
		mk("t01", []string{"mrg"}, "frank", 190, 10, row("r4", "open")),
	}
	state, err := fold.Fold(covered)
	if err != nil {
		t.Fatalf("fold covered history: %v", err)
	}
	want, err := fold.Fold(append(append([]model.PackCommit{}, covered...), suffix...))
	if err != nil {
		t.Fatalf("root fold: %v", err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 20 {
		shuffled := append([]model.PackCommit{}, suffix...)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got, err := fold.Resume(state, shuffled)
		if err != nil {
			t.Fatalf("Resume: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Resume over a suffix whose side forked below the checkpoint =\n%+v\nwant the root fold\n%+v", got, want)
		}
	}
}

func TestResumeRefusesASuffixHoldingACreate(t *testing.T) {
	state, err := fold.Fold([]model.PackCommit{mk("aaa", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "desk"})})
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if _, err := fold.Resume(state, []model.PackCommit{mk("bbb", []string{"aaa"}, "bob", 110, 2, model.CreateLedger{Nonce: "m", Title: "again"})}); err == nil {
		t.Fatal("Resume over a suffix holding a second create succeeded, want ErrDuplicateCreate")
	}
}
