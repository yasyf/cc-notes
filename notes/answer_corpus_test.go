package notes

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestAnswerCorpusMatchesCanonicalProgramBranchUnion(t *testing.T) {
	c, _ := newWBClient(t)
	for _, spec := range []NoteSpec{
		{Title: "both 雪", Body: "body🙂\n", Tags: []string{"scope:durable", "program:test"}, Anchors: AnchorSpec{Branches: []string{"main"}}},
		{Title: "program", Body: "", Tags: []string{"scope:durable", "program:test"}},
		{Title: "branch", Body: "other program", Tags: []string{"scope:durable", "program:other"}, Anchors: AnchorSpec{Branches: []string{"main"}}},
		{Title: "ephemeral", Tags: []string{"program:test"}, Anchors: AnchorSpec{Branches: []string{"main"}}},
		{Title: "other", Tags: []string{"scope:durable", "program:other"}, Anchors: AnchorSpec{Branches: []string{"dev"}}},
	} {
		if _, _, err := c.CreateAnswer(t.Context(), spec); err != nil {
			t.Fatal(err)
		}
	}
	for _, branch := range []string{"main", ""} {
		t.Run("branch="+branch, func(t *testing.T) {
			program, err := c.Answers(t.Context(), DocumentFilter{Labels: []string{"scope:durable", "program:test"}})
			if err != nil {
				t.Fatal(err)
			}
			combined := slices.Clone(program)
			if branch != "" {
				answers, err := c.Answers(t.Context(), DocumentFilter{Labels: []string{"scope:durable"}, Anchor: AnchorFilter{Branch: branch}})
				if err != nil {
					t.Fatal(err)
				}
				combined = append(combined, answers...)
			}
			ordered := make([]model.Answer, 0, len(combined))
			positions := make(map[model.EntityID]int)
			for _, answer := range combined {
				if position, ok := positions[answer.ID]; ok {
					ordered[position] = answer
				} else {
					positions[answer.ID] = len(ordered)
					ordered = append(ordered, answer)
				}
			}
			winners := make(map[string]model.Answer)
			for _, answer := range ordered {
				winners[answer.ID.Short()] = answer
			}
			want := make([]model.Answer, 0, len(winners))
			for _, answer := range winners {
				want = append(want, answer)
			}
			slices.SortFunc(want, func(a, b model.Answer) int { return cmp.Compare(a.ID.Short(), b.ID.Short()) })
			cut, err := c.OpenAnswerCorpus(t.Context(), AnswerCorpusFilter{Program: "test", Branch: branch, CacheDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cut.Close() }()
			if cut.Binding == "" || cut.Generation == "" || len(cut.Records) != len(want) {
				t.Fatalf("cut metadata = %+v; want %d", cut, len(want))
			}
			ids := make([]string, 0, len(want))
			for i, answer := range want {
				record := cut.Records[i]
				if record.ID != answer.ID || record.ShortID != answer.ID.Short() || record.Path != answer.ID.Short()+".md" || record.Tip != answer.Head || !record.UpdatedAt.Equal(answer.Meta().UpdatedAt) {
					t.Fatalf("record = %+v; want %+v", record, answer)
				}
				text, err := cut.Text(t.Context(), record.ShortID)
				if expected := "# " + answer.Title + "\n\n" + answer.Body + "\n"; err != nil || text != expected {
					t.Fatalf("text = %q, %v; want %q", text, err, expected)
				}
				ids = append(ids, record.ShortID)
			}
			slices.Reverse(ids)
			slices.Reverse(want)
			if got, err := cut.Pick(t.Context(), ids); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("canonical pick = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestAnswerCorpusPreservesTolerantPartialAnswer(t *testing.T) {
	c, _ := newWBClient(t)
	snapshot, err := c.s.CreateExact(t.Context(), []model.Op{model.CreateAnswer{Nonce: model.NewNonce(), Title: "partial", Body: "canonical body", Tags: []string{"scope:durable", "program:test"}}})
	if err != nil {
		t.Fatal(err)
	}
	a := snapshot.(model.Answer)
	signature := gitobj.Signature{Name: "Raw Writer", Email: "raw@example.com", When: time.Unix(a.UpdatedAt+1, 0)}
	partial, err := c.s.Repo.WriteOpsCommit(t.Context(), []model.SHA{a.Head}, signature, "partial", model.Pack{Lamport: 2, Ops: []model.Op{model.SetStatus{Status: model.StatusDone}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.s.PublishRef(t.Context(), refs.For(model.KindAnswer, a.ID), partial, a.Head); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	for range 2 {
		want, err := c.Answers(t.Context(), DocumentFilter{Labels: []string{"scope:durable", "program:test"}})
		if err != nil || len(want) != 1 || want[0].SkippedOps != 1 || want[0].Head != partial {
			t.Fatalf("legacy tolerant answer = %+v, %v", want, err)
		}
		cut, err := c.OpenAnswerCorpus(t.Context(), AnswerCorpusFilter{Program: "test", CacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := cut.Text(t.Context(), a.ID.Short()); err != nil || got != "# partial\n\ncanonical body\n" {
			t.Fatalf("partial text = %q, %v", got, err)
		}
		if got, err := cut.Pick(t.Context(), []string{a.ID.Short()}); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("partial canonical pick = %+v, %v; want %+v", got, err, want)
		}
		if err := cut.Close(); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(c.s.CommonDir(), "cc-notes", "relevant-v6"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "list-") {
			t.Fatalf("partial answer entered normal list result cache: %s", entry.Name())
		}
	}
}
