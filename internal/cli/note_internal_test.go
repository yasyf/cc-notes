package cli

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/model"
)

func ids(notes []model.Note) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = string(n.ID)
	}
	return out
}

func eqIDs(t *testing.T, got []model.Note, want ...string) {
	t.Helper()
	g := ids(got)
	if len(g) != len(want) {
		t.Fatalf("ids = %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("ids = %v, want %v", g, want)
		}
	}
}

func TestRankNotes(t *testing.T) {
	t.Run("tier order title>tag>body", func(t *testing.T) {
		notes := []model.Note{
			{ID: "body", Title: "Z", Body: "the widget breaks"},
			{ID: "title", Title: "Widget design"},
			{ID: "tag", Title: "Other", Tags: []string{"widget"}},
			{ID: "none", Title: "unrelated", Body: "nothing here"},
		}
		got := rankNotes(notes, "widget", nil, "", "", "", "", 20)
		eqIDs(t, got, "title", "tag", "body")
	})

	t.Run("recency then id within tier", func(t *testing.T) {
		notes := []model.Note{
			{ID: "b", Title: "widget B", UpdatedAt: 100},
			{ID: "a", Title: "widget A", UpdatedAt: 200},
			{ID: "d", Title: "widget D", UpdatedAt: 100},
			{ID: "c", Title: "widget C", UpdatedAt: 100},
		}
		got := rankNotes(notes, "widget", nil, "", "", "", "", 20)
		eqIDs(t, got, "a", "b", "c", "d")
	})

	t.Run("limit truncation", func(t *testing.T) {
		notes := []model.Note{
			{ID: "a", Title: "widget A", UpdatedAt: 300},
			{ID: "b", Title: "widget B", UpdatedAt: 200},
			{ID: "c", Title: "widget C", UpdatedAt: 100},
		}
		got := rankNotes(notes, "widget", nil, "", "", "", "", 2)
		eqIDs(t, got, "a", "b")
	})

	t.Run("tag filter narrows", func(t *testing.T) {
		notes := []model.Note{
			{ID: "yes", Title: "widget one", Tags: []string{"design"}},
			{ID: "no", Title: "widget two", Tags: []string{"misc"}},
		}
		got := rankNotes(notes, "widget", []string{"design"}, "", "", "", "", 20)
		eqIDs(t, got, "yes")
	})

	t.Run("author filter narrows", func(t *testing.T) {
		notes := []model.Note{
			{ID: "yes", Title: "widget", Author: "ada <ada@example.com>"},
			{ID: "no", Title: "widget", Author: "ben <ben@example.com>"},
		}
		got := rankNotes(notes, "widget", nil, "ada <ada@example.com>", "", "", "", 20)
		eqIDs(t, got, "yes")
	})

	t.Run("anchor filters narrow", func(t *testing.T) {
		notes := []model.Note{
			{ID: "yes", Title: "widget", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "a.go"}}},
			{ID: "no", Title: "widget", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "b.go"}}},
		}
		got := rankNotes(notes, "widget", nil, "", "a.go", "", "", 20)
		eqIDs(t, got, "yes")
	})

	t.Run("case-insensitive match", func(t *testing.T) {
		notes := []model.Note{{ID: "a", Title: "The Widget Factory"}}
		got := rankNotes(notes, "WIDGET", nil, "", "", "", "", 20)
		eqIDs(t, got, "a")
	})
}
