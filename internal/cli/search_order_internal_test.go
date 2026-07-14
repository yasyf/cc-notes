package cli

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func noteHit(id string, tier int, updated int64) searchHit {
	return searchHit{snap: model.Note{ID: model.EntityID(id), UpdatedAt: updated}, tier: tier}
}

func logHit(id string, tier int, updated int64) searchHit {
	return searchHit{snap: model.Log{ID: model.EntityID(id), UpdatedAt: updated}, tier: tier}
}

func TestCompareSearchHits(t *testing.T) {
	cases := []struct {
		name string
		a, b searchHit
		want int
	}{
		{"higher tier first regardless of time and id", noteHit("ff", 2, 100), noteHit("aa", 1, 900), -1},
		{"lower tier last", noteHit("aa", 1, 900), noteHit("ff", 2, 100), 1},
		{"equal tier: newer updated first", noteHit("ff", 1, 900), noteHit("aa", 1, 100), -1},
		{"equal tier: older updated last", noteHit("aa", 1, 100), noteHit("ff", 1, 900), 1},
		{"equal tier and time: ascending entity id, cross-kind", noteHit("aa", 1, 500), logHit("bb", 1, 500), -1},
		{"equal tier and time: descending id pair inverts", logHit("bb", 1, 500), noteHit("aa", 1, 500), 1},
		{"identical rank, time, and id", noteHit("aa", 1, 500), noteHit("aa", 1, 500), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareSearchHits(tc.a, tc.b); got != tc.want {
				t.Fatalf("compareSearchHits = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSearchHitsFullOrder(t *testing.T) {
	hits := []searchHit{
		logHit("dd", 1, 500),
		noteHit("cc", 1, 900),
		noteHit("bb", 1, 500),
		noteHit("aa", 2, 100),
	}
	slices.SortFunc(hits, compareSearchHits)
	want := []string{"aa", "cc", "bb", "dd"}
	got := make([]string, len(hits))
	for i, h := range hits {
		got[i] = string(h.snap.EntityID())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sorted ids = %v, want %v", got, want)
	}
}
