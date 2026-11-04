package notes_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

const akamaiQuestion = "Do we chase Akamai edge 403s (GSK or any tenant proxy) at all, ever?"

func akamaiRuling(extra ...string) notes.NoteSpec {
	return notes.NoteSpec{
		Title: akamaiQuestion,
		Body:  "No. Never a lane, a read, a report, a line to him. Any 403 whose body is an Akamai or customer-edge denial is classified and dropped at the first read.",
		Tags:  append([]string{"akamai", "owner-ruling", "scope:durable"}, extra...),
	}
}

func matchedTitles(t *testing.T, c *notes.Client, text string, limit int) []string {
	t.Helper()
	found, err := c.MatchAnswers(t.Context(), text, notes.SearchFilter{Labels: []string{"scope:durable"}, Limit: limit})
	if err != nil {
		t.Fatalf("MatchAnswers: %v", err)
	}
	titles := make([]string, len(found))
	for i, match := range found {
		titles[i] = match.Answer.Title
	}
	return titles
}

func createAnswers(t *testing.T, c *notes.Client, specs ...notes.NoteSpec) []model.Answer {
	t.Helper()
	made := make([]model.Answer, len(specs))
	for i, spec := range specs {
		answer, _, err := c.CreateAnswer(t.Context(), spec)
		if err != nil {
			t.Fatalf("CreateAnswer %q: %v", spec.Title, err)
		}
		made[i] = answer
	}
	return made
}

func TestMatchAnswersCarriesTheAkamaiRulingToAnAlertNamingA403(t *testing.T) {
	c, _ := newClient(t)
	createAnswers(t, c,
		akamaiRuling(),
		notes.NoteSpec{Title: "Platy's reconciliation check gets a 403 from the GitHub App", Body: "ask anubhav", Tags: []string{"scope:durable"}},
		notes.NoteSpec{Title: "Which cache backend?", Body: "Redis", Tags: []string{"scope:durable"}},
	)
	for name, alert := range map[string]string{
		"the card that reached the owner": "the old cloud runner's journal cadence calls began failing with timeouts and 403s at the GSK edge",
		"a record with no spaces":         "calls fail with local lane timeouts and GSK edge403 at20:54-55Z",
		"an alert naming only a 403":      "Alert: blob list failing with HTTP 403 Access Denied from the customer edge on plat",
	} {
		if got := matchedTitles(t, c, alert, 5); !slices.Equal(got, []string{akamaiQuestion}) {
			t.Errorf("%s: matched %q, want only the Akamai ruling", name, got)
		}
	}
}

func TestMatchAnswersLeavesAnAlertThatNamesNoneOfItsWords(t *testing.T) {
	c, _ := newClient(t)
	createAnswers(t, c, akamaiRuling())
	if got := matchedTitles(t, c, "Warn: Runner failing to change a watched run's sync cadence", 5); len(got) != 0 {
		t.Fatalf("matched %q on an alert with none of the ruling's words", got)
	}
}

func TestMatchAnswersSkipsAnAnswerSharingOneWordOfItsQuestion(t *testing.T) {
	c, _ := newClient(t)
	createAnswers(t, c, akamaiRuling())
	if got := matchedTitles(t, c, "the share link returned a 403", 5); len(got) != 0 {
		t.Fatalf("matched %q on one shared word", got)
	}
}

func TestMatchAnswersPutsAPhraseLabelFirstWithItsPhrase(t *testing.T) {
	c, _ := newClient(t)
	createAnswers(t, c,
		notes.NoteSpec{Title: "Do we alert on a GSK edge 403 in a release?", Body: "Yes.", Tags: []string{"scope:durable"}},
		akamaiRuling("match:GSK edge"),
	)
	found, err := c.MatchAnswers(t.Context(), "timeouts and 403s at the gsk edge", notes.SearchFilter{Limit: 5})
	if err != nil {
		t.Fatalf("MatchAnswers: %v", err)
	}
	if len(found) == 0 || found[0].Answer.Title != akamaiQuestion || found[0].Phrase != "GSK edge" {
		t.Fatalf("first match = %+v, want the Akamai ruling by its phrase", found)
	}
}

func TestMatchAnswersStaysAtItsLimitAsAnswersGrow(t *testing.T) {
	c, _ := newClient(t)
	specs := make([]notes.NoteSpec, 0, 104)
	specs = append(specs, akamaiRuling("match:gsk edge"))
	for i := range 3 {
		specs = append(specs, notes.NoteSpec{Title: fmt.Sprintf("Question %d on the GSK edge", i), Body: "Answer.", Tags: []string{"scope:durable"}})
	}
	for i := range 100 {
		specs = append(specs, notes.NoteSpec{Title: fmt.Sprintf("Filler %d on the cache", i), Body: "Answer.", Tags: []string{"scope:durable"}})
	}
	createAnswers(t, c, specs...)
	got := matchedTitles(t, c, "timeouts at the gsk edge", 3)
	if len(got) != 3 || got[0] != akamaiQuestion {
		t.Fatalf("matched %q, want exactly 3 led by the ruling", got)
	}
}

func TestMatchAnswersSkipsAnExpiredAnswer(t *testing.T) {
	c, _ := newClient(t)
	made := createAnswers(t, c, akamaiRuling("match:gsk edge"), notes.NoteSpec{Title: "Who owns the GSK contact?", Body: "Tevon", Tags: []string{"scope:durable", "match:gsk contact"}})
	if _, err := c.ExpireAnswer(t.Context(), made[1].ID, "he left"); err != nil {
		t.Fatalf("ExpireAnswer: %v", err)
	}
	if got := matchedTitles(t, c, "the gsk contact said so at the gsk edge", 5); !slices.Equal(got, []string{akamaiQuestion}) {
		t.Fatalf("matched %q, want the live ruling alone", got)
	}
}
