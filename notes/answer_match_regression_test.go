package notes

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func answerMatchCohort(t *testing.T) (*Client, string, []model.Answer) {
	t.Helper()
	c, dir := newWBClient(t)
	var vocabulary strings.Builder
	for i := range 256 {
		fmt.Fprintf(&vocabulary, "lexeme%03d ", i)
	}
	body := strings.Repeat(vocabulary.String(), 16)
	answers := make([]model.Answer, 0, 24)
	for i := range 24 {
		snapshot, err := c.s.Create(t.Context(), []model.Op{model.CreateAnswer{
			Nonce: model.NewNonce(), Title: fmt.Sprintf("Nebula answer %02d", i),
			Body: body, Tags: []string{"cohort", "match:phrase beacon"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		answers = append(answers, snapshot.(model.Answer))
	}
	return c, dir, answers
}

func answerMatchBodyReads(t *testing.T, c *Client, dir string, query func(*Client, *atomic.Int64)) int64 {
	t.Helper()
	if _, hit, err := c.s.StatusMetadata(t.Context(), model.KindAnswer); err != nil || !hit {
		t.Fatalf("prime answer metadata: hit=%v error=%v", hit, err)
	}
	tips, err := c.s.Repo.ListPrefix(t.Context(), refs.Root(model.KindAnswer))
	if err != nil {
		t.Fatal(err)
	}
	objects := make(map[model.SHA][]byte, len(tips))
	for _, tip := range tips {
		data, err := os.ReadFile(labeledListObjectPath(c, tip))
		if err != nil {
			t.Fatal(err)
		}
		objects[tip] = data
	}
	defer func() {
		for tip, data := range objects {
			if err := os.WriteFile(labeledListObjectPath(c, tip), data, 0o600); err != nil {
				t.Error(err)
			}
		}
	}()
	for tip := range objects {
		if err := os.Remove(labeledListObjectPath(c, tip)); err != nil {
			t.Fatal(err)
		}
	}
	clearClientCache(t, c, "folds-v6")
	fresh, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	var reads atomic.Int64
	fresh.s.Repo.BackfillWith(func(ctx context.Context, tip model.SHA) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, ok := objects[tip]
		if !ok {
			return fmt.Errorf("unexpected answer body %s", tip)
		}
		reads.Add(1)
		return os.WriteFile(labeledListObjectPath(fresh, tip), data, 0o600)
	})
	query(fresh, &reads)
	return reads.Load()
}

func TestIndexedAnswerMatchSelectedBodies(t *testing.T) {
	c, dir, answers := answerMatchCohort(t)
	filter := SearchFilter{Labels: []string{"cohort"}, Limit: 1}
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%v", changed), func(t *testing.T) {
			if changed {
				selected := matchAnswers(answers, "phrase beacon", 1, true)[0].Answer.ID
				i := slices.IndexFunc(answers, func(a model.Answer) bool { return a.ID == selected })
				body := "changed body " + answers[i].Body
				var err error
				answers[i], err = c.EditAnswer(t.Context(), answers[i].ID, NoteEdit{Body: &body})
				if err != nil {
					t.Fatal(err)
				}
			}
			want := matchAnswers(answers, "phrase beacon", filter.Limit, true)
			if len(want) != 1 {
				t.Fatalf("reference selected %d answers", len(want))
			}
			if _, err := c.MatchAnswers(t.Context(), "phrase beacon", filter); err != nil {
				t.Fatal(err)
			}
			reads := answerMatchBodyReads(t, c, dir, func(fresh *Client, reads *atomic.Int64) {
				got, err := fresh.MatchAnswers(t.Context(), "unfindable", filter)
				if err != nil || len(got) != 0 || reads.Load() != 0 {
					t.Fatalf("zero hit: matches=%d reads=%d error=%v", len(got), reads.Load(), err)
				}
				got, err = fresh.MatchAnswers(t.Context(), "phrase beacon", filter)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("selected DTO/rank differs from reference: %v", err)
				}
			})
			if reads != 1 {
				t.Fatalf("read %d answer bodies for limit 1 within %d matching labeled answers", reads, len(answers))
			}
		})
	}
}

func TestIndexedAnswerMatchTransitions(t *testing.T) {
	c, _ := newWBClient(t)
	answers := make([]model.Answer, 0, 3)
	for _, spec := range []NoteSpec{
		{Title: "quasar quasar orbit", Body: "quasar quasar comet", Tags: []string{"science"}},
		{Title: "pulsar galaxy", Body: "pulsar galaxy", Tags: []string{"science", "match:phrase only"}},
		{Title: "nebula", Body: "nebula", Tags: []string{"science"}},
	} {
		snapshot, err := c.s.Create(t.Context(), []model.Op{model.CreateAnswer{Nonce: model.NewNonce(), Title: spec.Title, Body: spec.Body, Tags: spec.Tags}})
		if err != nil {
			t.Fatal(err)
		}
		answers = append(answers, snapshot.(model.Answer))
	}
	check := func(t *testing.T) {
		t.Helper()
		for _, labels := range [][]string{nil, {"science"}, {"selected"}} {
			all, err := c.Answers(t.Context(), DocumentFilter{Labels: labels})
			if err != nil {
				t.Fatal(err)
			}
			all = slices.DeleteFunc(all, func(a model.Answer) bool { return a.StaleAt != 0 })
			for _, text := range []string{"quasar quasar orbit comet", "nova nova comet", "phrase only"} {
				for _, floored := range []bool{false, true} {
					got, err := c.rankAnswers(t.Context(), text, SearchFilter{Labels: labels, Limit: -1}, floored)
					want := matchAnswers(all, text, -1, floored)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("labels=%v text=%q floored=%v: DTO/rank differs: %v", labels, text, floored, err)
					}
				}
			}
		}
	}
	check(t)
	title, body := "nova nova comet", "nova nova quasar comet"
	for _, step := range []struct {
		name  string
		write func() (model.Answer, error)
	}{
		{"title", func() (model.Answer, error) { return c.EditAnswer(t.Context(), answers[0].ID, NoteEdit{Title: &title}) }},
		{"body", func() (model.Answer, error) { return c.EditAnswer(t.Context(), answers[0].ID, NoteEdit{Body: &body}) }},
		{"add-label", func() (model.Answer, error) {
			return c.EditAnswer(t.Context(), answers[0].ID, NoteEdit{AddTags: []string{"selected"}})
		}},
		{"remove-label", func() (model.Answer, error) {
			return c.EditAnswer(t.Context(), answers[0].ID, NoteEdit{RemoveTags: []string{"science"}})
		}},
		{"stale", func() (model.Answer, error) { return c.ExpireAnswer(t.Context(), answers[0].ID, "changed") }},
		{"unstale", func() (model.Answer, error) { return c.UnexpireAnswer(t.Context(), answers[0].ID) }},
		{"supersede", func() (model.Answer, error) { return c.SupersedeAnswer(t.Context(), answers[0].ID, answers[1].ID) }},
		{"unsupersede", func() (model.Answer, error) { return c.UnsupersedeAnswer(t.Context(), answers[0].ID, answers[1].ID) }},
		{"delete", func() (model.Answer, error) { return c.RemoveAnswer(t.Context(), answers[0].ID) }},
	} {
		t.Run(step.name, func(t *testing.T) {
			if _, err := step.write(); err != nil {
				t.Fatal(err)
			}
			check(t)
		})
	}
}

func TestIndexedAnswerMatchCapturedGeneration(t *testing.T) {
	c, _ := newWBClient(t)
	snapshot, err := c.s.Create(t.Context(), []model.Op{model.CreateAnswer{Nonce: model.NewNonce(), Title: "quasar", Body: "quasar quasar orbit", Tags: []string{"science", "match:phrase only"}}})
	if err != nil {
		t.Fatal(err)
	}
	a := snapshot.(model.Answer)
	labels, asked := []string{"science"}, []string{"quasar"}
	pinned, err := c.s.PinnedKinds(t.Context(), model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	before, err := pinned.IndexedAnswerMatches(t.Context(), labels, asked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.s.IndexedAnswerMatches(t.Context(), labels, asked); err != nil {
		t.Fatal(err)
	}
	body := "nova nova"
	if _, err := c.EditAnswer(t.Context(), a.ID, NoteEdit{Body: &body, RemoveTags: labels}); err != nil {
		t.Fatal(err)
	}
	retained, err := pinned.IndexedAnswerMatches(t.Context(), labels, asked)
	if err != nil || !reflect.DeepEqual(retained, before) {
		t.Fatalf("captured ranking rows changed: %v", err)
	}
	want := matchAnswers([]model.Answer{a}, "quasar", -1, false)
	got, err := FromStore(pinned).NearestAnswers(t.Context(), "quasar", SearchFilter{Labels: labels, Limit: -1})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("pinned DTO/rank changed: %v", err)
	}
	if current, err := c.MatchAnswers(t.Context(), "quasar", SearchFilter{Labels: labels, Limit: -1}); err != nil || len(current) != 0 {
		t.Fatalf("current label membership: matches=%d error=%v", len(current), err)
	}
	materialized, err := c.s.MaterializeAnswerMatches(t.Context(), before.Answers)
	if err != nil || !reflect.DeepEqual(materialized, []model.Answer{a}) {
		t.Fatalf("captured tip materialization changed: %v", err)
	}
}
