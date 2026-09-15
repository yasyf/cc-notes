package notes_test

import (
	"slices"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func TestAnswerLifecycle(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "cache/cache.go", "v1\n")

	durable, _, err := c.CreateAnswer(ctx, notes.NoteSpec{
		Title: "Which cache backend?", Body: "Redis\nOptions: Redis | Memcached",
		Tags:    []string{"scope:durable", "header:Cache"},
		Anchors: notes.AnchorSpec{Paths: []string{"cache/cache.go"}, Branches: []string{"main"}},
	})
	if err != nil {
		t.Fatalf("CreateAnswer durable: %v", err)
	}
	if durable.VerifiedAt == 0 {
		t.Fatal("answer born with VerifiedAt == 0; a fresh add must be verified")
	}
	ephemeral, _, err := c.CreateAnswer(ctx, notes.NoteSpec{Title: "Run the slow tests now?", Body: "No", Tags: []string{"scope:ephemeral"}})
	if err != nil {
		t.Fatalf("CreateAnswer ephemeral: %v", err)
	}

	got, err := c.Answers(ctx, notes.DocumentFilter{Labels: []string{"scope:durable"}})
	if err != nil {
		t.Fatalf("Answers: %v", err)
	}
	if ids := answerIDs(got); !slices.Equal(ids, []model.EntityID{durable.ID}) {
		t.Fatalf("durable answers = %v, want [%s]", ids, durable.ID)
	}

	hits, err := c.SearchAnswers(ctx, "memcached", notes.SearchFilter{Limit: -1})
	if err != nil {
		t.Fatalf("SearchAnswers: %v", err)
	}
	if ids := answerIDs(hits); !slices.Equal(ids, []model.EntityID{durable.ID}) {
		t.Fatalf("search memcached = %v, want the body match [%s]", ids, durable.ID)
	}

	replacement, _, err := c.CreateAnswer(ctx, notes.NoteSpec{Title: "Which cache backend?", Body: "Memcached", Tags: []string{"scope:durable"}})
	if err != nil {
		t.Fatalf("CreateAnswer replacement: %v", err)
	}
	if _, err := c.SupersedeAnswer(ctx, durable.ID, replacement.ID); err != nil {
		t.Fatalf("SupersedeAnswer: %v", err)
	}
	live, err := c.Answers(ctx, notes.DocumentFilter{})
	if err != nil {
		t.Fatalf("Answers after supersede: %v", err)
	}
	if ids := answerIDs(live); slices.Contains(ids, durable.ID) || len(ids) != 2 {
		t.Fatalf("live answers = %v, want the replacement and the ephemeral answer only", ids)
	}
	heads, err := c.AnswerSupersedeHeads(ctx, durable.ID)
	if err != nil {
		t.Fatalf("AnswerSupersedeHeads: %v", err)
	}
	if !slices.Equal(heads, []model.EntityID{replacement.ID}) {
		t.Fatalf("supersede heads = %v, want [%s]", heads, replacement.ID)
	}

	if _, err := c.ExpireAnswer(ctx, ephemeral.ID, "session over"); err != nil {
		t.Fatalf("ExpireAnswer: %v", err)
	}
	reviews, err := c.ReviewAnswers(ctx, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("ReviewAnswers: %v", err)
	}
	if len(reviews) != 1 || reviews[0].Answer.ID != ephemeral.ID || reviews[0].Verdict != notes.VerdictExpired {
		t.Fatalf("reviews = %+v, want only the expired ephemeral answer", reviews)
	}

	report, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.Answers != (notes.SummaryCount{Total: 2, NeedsReview: 1}) {
		t.Fatalf("status answers = %+v, want 2 total, 1 needing review", report.Answers)
	}

	body := "Valkey"
	edited, err := c.EditAnswer(ctx, replacement.ID, notes.NoteEdit{Body: &body, AddTags: []string{"header:Cache"}})
	if err != nil {
		t.Fatalf("EditAnswer: %v", err)
	}
	if edited.Body != "Valkey" || !slices.Equal(edited.Tags, []string{"header:Cache", "scope:durable"}) {
		t.Fatalf("edited = body %q tags %v, want Valkey with both tags", edited.Body, edited.Tags)
	}
	if _, err := c.RemoveAnswer(ctx, edited.ID); err != nil {
		t.Fatalf("RemoveAnswer: %v", err)
	}
	if _, err := c.Note(ctx, edited.ID); err == nil {
		t.Fatal("Note() loaded an answer id; answers must live outside the note namespace")
	}
}

func answerIDs(answers []model.Answer) []model.EntityID {
	out := make([]model.EntityID, len(answers))
	for i, a := range answers {
		out[i] = a.ID
	}
	return out
}
