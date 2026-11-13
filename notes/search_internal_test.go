package notes

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func rankedIDs(t *testing.T, got []model.Note, want ...string) {
	t.Helper()
	ids := make([]string, len(got))
	for i, n := range got {
		ids[i] = string(n.ID)
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestRankDocuments(t *testing.T) {
	all := SearchFilter{Limit: 20}

	t.Run("tier order title>tag>body", func(t *testing.T) {
		ns := []model.Note{
			{ID: "body", Title: "Z", Body: "the widget breaks"},
			{ID: "title", Title: "Widget design"},
			{ID: "tag", Title: "Other", Tags: []string{"widget"}},
			{ID: "none", Title: "unrelated", Body: "nothing here"},
		}
		rankedIDs(t, rankDocuments(ns, "widget", all, noteRanker), "title", "tag", "body")
	})

	t.Run("recency then id within tier", func(t *testing.T) {
		ns := []model.Note{
			{ID: "b", Title: "widget B", UpdatedAt: 100},
			{ID: "a", Title: "widget A", UpdatedAt: 200},
			{ID: "d", Title: "widget D", UpdatedAt: 100},
			{ID: "c", Title: "widget C", UpdatedAt: 100},
		}
		rankedIDs(t, rankDocuments(ns, "widget", all, noteRanker), "a", "b", "c", "d")
	})

	t.Run("limit truncation", func(t *testing.T) {
		ns := []model.Note{
			{ID: "a", Title: "widget A", UpdatedAt: 300},
			{ID: "b", Title: "widget B", UpdatedAt: 200},
			{ID: "c", Title: "widget C", UpdatedAt: 100},
		}
		rankedIDs(t, rankDocuments(ns, "widget", SearchFilter{Limit: 2}, noteRanker), "a", "b")
	})

	t.Run("label, author, and anchor filters narrow", func(t *testing.T) {
		ns := []model.Note{
			{ID: "yes", Title: "widget", Tags: []string{"design"}, Author: "ada <ada@example.com>", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "a.go"}}},
			{ID: "label", Title: "widget", Tags: []string{"misc"}, Author: "ada <ada@example.com>", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "a.go"}}},
			{ID: "author", Title: "widget", Tags: []string{"design"}, Author: "ben <ben@example.com>", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "a.go"}}},
			{ID: "anchor", Title: "widget", Tags: []string{"design"}, Author: "ada <ada@example.com>", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "b.go"}}},
		}
		f := SearchFilter{Labels: []string{"design"}, Author: "ada <ada@example.com>", Anchors: AnchorFilter{Path: "a.go"}, Limit: 20}
		rankedIDs(t, rankDocuments(ns, "widget", f, noteRanker), "yes")
	})

	t.Run("case-insensitive match", func(t *testing.T) {
		ns := []model.Note{{ID: "a", Title: "The Widget Factory"}}
		rankedIDs(t, rankDocuments(ns, "WIDGET", all, noteRanker), "a")
	})

	t.Run("more words held ranks first, whatever the field", func(t *testing.T) {
		ns := []model.Note{
			{ID: "one-title", Title: "spend limits", UpdatedAt: 300},
			{ID: "all-split", Title: "Does a spend-cap pause stop the run?", UpdatedAt: 100},
			{ID: "two-body", Title: "Z", Body: "the cap and the pause", UpdatedAt: 200},
			{ID: "none", Title: "unrelated"},
		}
		rankedIDs(t, rankDocuments(ns, "spend cap pause", all, noteRanker), "all-split", "two-body", "one-title")
	})

	t.Run("words split across title and body", func(t *testing.T) {
		ns := []model.Note{
			{ID: "split", Title: "image pipeline", Body: "the decode step drops frames"},
			{ID: "body", Title: "Z", Body: "an image we decode"},
		}
		rankedIDs(t, rankDocuments(ns, "image decode", all, noteRanker), "split", "body")
	})

	t.Run("stop words and short words do not count", func(t *testing.T) {
		ns := []model.Note{
			{ID: "pin", Title: "Where should a chat's pin be stored?"},
			{ID: "stop", Title: "where should it be"},
		}
		rankedIDs(t, rankDocuments(ns, "Where should a chat pin be stored", all, noteRanker), "pin")
	})

	t.Run("a query of only stop words still searches", func(t *testing.T) {
		ns := []model.Note{{ID: "a", Title: "the plan"}, {ID: "b", Title: "a plan"}}
		rankedIDs(t, rankDocuments(ns, "the", all, noteRanker), "a")
	})
}

func TestOrderHits(t *testing.T) {
	hits := []searchHit{
		{snap: model.Log{ID: "dd", UpdatedAt: 500}, words: 1, tier: 1},
		{snap: model.Note{ID: "cc", UpdatedAt: 900}, words: 1, tier: 1},
		{snap: model.Note{ID: "bb", UpdatedAt: 500}, words: 1, tier: 1},
		{snap: model.Note{ID: "aa", UpdatedAt: 100}, words: 1, tier: 3},
		{snap: model.Answer{ID: "ee", UpdatedAt: 50}, words: 2, tier: 2},
	}
	got := orderHits(hits, -1)
	ids := make([]string, len(got))
	for i, s := range got {
		ids[i] = string(s.EntityID())
	}
	if want := []string{"ee", "aa", "cc", "bb", "dd"}; !slices.Equal(ids, want) {
		t.Fatalf("ordered ids = %v, want %v (words, then tier, then newer, then id across kinds)", ids, want)
	}
}
