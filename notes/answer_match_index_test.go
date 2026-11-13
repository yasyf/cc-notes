package notes

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestIndexedAnswerMatchReference(t *testing.T) {
	t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
	c, err := Open(gittest.InitRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	for _, fixture := range []struct {
		title string
		tags  []string
		ops   []model.Op
	}{
		{title: "Nebula quasar", tags: []string{"science", "selected", "match:phrase only", "match:phrase"}},
		{title: "Pulsar galaxy", tags: []string{"science"}},
		{title: "Nebula edited", tags: []string{"science", "selected"}, ops: []model.Op{model.SetBody{Body: "quasar orbit"}}},
		{title: "Nebula stale", tags: []string{"science"}, ops: []model.Op{model.MarkStale{Reason: "expired"}}},
		{title: "Nebula deleted", tags: []string{"science"}, ops: []model.Op{model.DeleteNote{}}},
	} {
		ops := []model.Op{model.CreateAnswer{Nonce: model.NewNonce(), Title: fixture.title, Body: strings.Repeat("quasar orbit comet ", 512), Tags: fixture.tags}}
		if _, err := c.s.Create(t.Context(), append(ops, fixture.ops...)); err != nil {
			t.Fatal(err)
		}
	}
	for _, labels := range [][]string{nil, {"science"}, {"science", "selected"}, {"absent"}} {
		answers, err := c.Answers(t.Context(), DocumentFilter{Labels: labels})
		if err != nil {
			t.Fatal(err)
		}
		answers = slices.DeleteFunc(answers, func(a model.Answer) bool { return a.StaleAt != 0 })
		for _, text := range []string{"nebula quasar orbit", "phrase only", "", "the and 42"} {
			for _, limit := range []int{-1, 0, 1, 10} {
				for _, floored := range []bool{false, true} {
					got, err := c.rankAnswers(t.Context(), text, SearchFilter{Labels: labels, Limit: limit}, floored)
					if err != nil {
						t.Fatal(err)
					}
					want := matchAnswers(answers, text, limit, floored)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("labels=%v text=%q limit=%d floored=%v: got %#v want %#v", labels, text, limit, floored, got, want)
					}
				}
			}
		}
	}
	answers, err := c.Answers(t.Context(), DocumentFilter{})
	if err != nil {
		t.Fatal(err)
	}
	answers = slices.DeleteFunc(answers, func(a model.Answer) bool { return a.StaleAt != 0 })
	text := "nebula quasar orbit"
	asked := slices.Sorted(maps.Keys(matchTerms(text)))
	corpus, err := c.s.IndexedAnswerMatches(t.Context(), nil, asked)
	if err != nil {
		t.Fatal(err)
	}
	indexed := testing.AllocsPerRun(3, func() { scoreAnswerMatches(corpus, text, asked, -1, false) })
	reference := testing.AllocsPerRun(3, func() { matchAnswers(answers, text, -1, false) })
	t.Logf("indexed_score_allocs=%.0f reference_score_allocs=%.0f ratio=%.3f", indexed, reference, indexed/reference)
	if indexed >= reference {
		t.Fatalf("indexed scorer allocations %.0f >= reference %.0f", indexed, reference)
	}
}
