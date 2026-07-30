package eval

import (
	"fmt"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func splitQuestions(ids ...string) []Question {
	out := make([]Question, len(ids))
	for i, id := range ids {
		out[i] = Question{ID: id, Query: id, GoldEntityIDs: []model.EntityID{"g"}}
	}
	return out
}

func splitIDs(questions []Question) []string {
	out := make([]string, len(questions))
	for i, q := range questions {
		out[i] = q.ID
	}
	return out
}

func TestSplitPartitionsWithoutOverlap(t *testing.T) {
	questions := splitQuestions("q001", "q002", "q003", "q004", "q005", "q006", "q007", "q008")
	selection, holdout := Split(questions)
	if len(selection)+len(holdout) != len(questions) {
		t.Fatalf("folds hold %d+%d questions, want all %d", len(selection), len(holdout), len(questions))
	}
	for _, id := range splitIDs(selection) {
		if slices.Contains(splitIDs(holdout), id) {
			t.Errorf("%s is in both folds; a weight chosen on it would be scored on it", id)
		}
	}
	for _, q := range questions {
		if !slices.Contains(splitIDs(selection), q.ID) && !slices.Contains(splitIDs(holdout), q.ID) {
			t.Errorf("%s fell out of both folds", q.ID)
		}
	}
	if len(selection) == 0 || len(holdout) == 0 {
		t.Errorf("folds = %v / %v, want eight questions to fill both", splitIDs(selection), splitIDs(holdout))
	}
}

// TestSplitKeepsEveryQuestionInItsFoldAsTheSetGrows is why the fold is a hash of
// the id rather than a position: adding questions must not silently re-run an
// old selection on questions that used to be held out.
func TestSplitKeepsEveryQuestionInItsFoldAsTheSetGrows(t *testing.T) {
	before := splitQuestions("q001", "q002", "q003", "q004")
	grown := before
	for i := range 20 {
		grown = append(grown, splitQuestions(fmt.Sprintf("new%02d", i))...)
	}
	wasSelection, wasHoldout := Split(before)
	nowSelection, nowHoldout := Split(grown)
	for _, id := range splitIDs(wasSelection) {
		if !slices.Contains(splitIDs(nowSelection), id) {
			t.Errorf("%s moved out of the selection fold when the set grew", id)
		}
	}
	for _, id := range splitIDs(wasHoldout) {
		if !slices.Contains(splitIDs(nowHoldout), id) {
			t.Errorf("%s moved out of the held-out fold when the set grew", id)
		}
	}
}

func TestSplitIsDeterministic(t *testing.T) {
	questions := splitQuestions("q001", "q017", "q042", "alpha", "beta")
	first, firstHeld := Split(questions)
	second, secondHeld := Split(questions)
	if !slices.Equal(splitIDs(first), splitIDs(second)) || !slices.Equal(splitIDs(firstHeld), splitIDs(secondHeld)) {
		t.Errorf("Split is not reproducible: %v/%v then %v/%v",
			splitIDs(first), splitIDs(firstHeld), splitIDs(second), splitIDs(secondHeld))
	}
}

// TestSplitLeavesAFoldEmptyWhenTheSetIsTooSmall is the reachable refusal
// condition: five of the six repositories in the shipped question set hold
// three questions or fewer, and one holds a single question, which cannot fill
// both folds at all.
func TestSplitLeavesAFoldEmptyWhenTheSetIsTooSmall(t *testing.T) {
	cases := []struct {
		name          string
		id            string
		wantSelection []string
		wantHoldout   []string
	}{
		{"cc-notes' only question is held out", "q049", nil, []string{"q049"}},
		{"the monorepo's second question selects", "q002", []string{"q002"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selection, holdout := Split(splitQuestions(tc.id))
			if !slices.Equal(splitIDs(selection), tc.wantSelection) || !slices.Equal(splitIDs(holdout), tc.wantHoldout) {
				t.Fatalf("Split(%s) = %v / %v, want %v / %v",
					tc.id, splitIDs(selection), splitIDs(holdout), tc.wantSelection, tc.wantHoldout)
			}
		})
	}
}

func TestGradedAndSessioned(t *testing.T) {
	questions := []Question{
		{ID: "graded", GoldEntityIDs: []model.EntityID{"g"}},
		{ID: "abstain", ExpectAbstain: true},
		{ID: "graded on a branch", GoldEntityIDs: []model.EntityID{"g"}, Session: Session{Branch: "yasyf/pulumi"}},
		{ID: "graded on a path", GoldEntityIDs: []model.EntityID{"g"}, Session: Session{Paths: []string{"a/b.go"}}},
	}
	if got := splitIDs(Graded(questions)); !slices.Equal(got, []string{"graded", "graded on a branch", "graded on a path"}) {
		t.Errorf("Graded = %v, want the three questions naming gold", got)
	}
	if got := Sessioned(questions); got != 2 {
		t.Errorf("Sessioned = %d, want 2", got)
	}
	if !(Session{}).Empty() || (Session{Branch: "b"}).Empty() || (Session{Paths: []string{"p"}}).Empty() {
		t.Error("Session.Empty does not distinguish the zero session from a populated one")
	}
}
