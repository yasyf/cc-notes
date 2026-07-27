package eval

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func controlCorpus() []Entity {
	return []Entity{
		{ID: "a", Kind: model.KindNote, Title: "alpha"},
		{ID: "b", Kind: model.KindNote, Title: "beta"},
		{ID: "c", Kind: model.KindNote, Title: "gamma"},
		{ID: "d", Kind: model.KindNote, Title: "delta"},
	}
}

func TestFullContextReturnsEverything(t *testing.T) {
	got, err := NewFullContext(controlCorpus(), 1).Retrieve(t.Context(), "nothing matches this", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("Retrieve = %d results, want the whole 4-entity corpus", len(got))
	}
	ids := make([]model.EntityID, len(got))
	for i, r := range got {
		ids[i] = r.ID
		if r.Score != FullContextScore {
			t.Errorf("result %d score = %v, want %v", i, r.Score, FullContextScore)
		}
		if r.Lane != FullContextLane {
			t.Errorf("result %d lane = %q, want %q", i, r.Lane, FullContextLane)
		}
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []model.EntityID{"a", "b", "c", "d"}) {
		t.Errorf("returned ids = %v, want the whole corpus", ids)
	}
}

func TestFullContextTopKIsSeedLuck(t *testing.T) {
	cases := []struct {
		seed int64
		want []model.EntityID
	}{
		{seed: 1, want: []model.EntityID{"d", "a"}},
		{seed: 2, want: []model.EntityID{"d", "c"}},
	}
	for _, tc := range cases {
		got, err := NewFullContext(controlCorpus(), tc.seed).Retrieve(t.Context(), "q", 2)
		if err != nil {
			t.Fatalf("Retrieve(seed=%d): %v", tc.seed, err)
		}
		ids := []model.EntityID{got[0].ID, got[1].ID}
		if !slices.Equal(ids, tc.want) {
			t.Errorf("seed %d top-2 = %v, want %v", tc.seed, ids, tc.want)
		}
	}
}

func TestFullContextEmptyCorpus(t *testing.T) {
	got, err := NewFullContext(nil, 1).Retrieve(t.Context(), "q", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Retrieve over an empty corpus = %+v, want no results", got)
	}
}
