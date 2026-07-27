package eval

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

const validSet = `{"version":1,"corpus":{"repo":"cc-notes"},"questions":[` +
	`{"id":"q1","repo":"cc-notes","query":"how does fold linearize","category":"mechanism","axis":"single-hop",` +
	`"gold_entity_ids":["aaaa"],"gold_answer":"lamport then author time","must_not_retrieve":["bbbb"],` +
	`"expect_abstain":false,"notes":"n"},` +
	`{"id":"q2","repo":"cc-notes","query":"who owns the kafka topic","category":"absent","axis":"abstention",` +
	`"gold_entity_ids":[],"expect_abstain":true}]}`

func TestDecodeQuestionsValid(t *testing.T) {
	qs, err := DecodeQuestions([]byte(validSet))
	if err != nil {
		t.Fatalf("DecodeQuestions: %v", err)
	}
	if qs.Version != 1 {
		t.Errorf("Version = %d, want 1", qs.Version)
	}
	if got := string(qs.Corpus); got != `{"repo":"cc-notes"}` {
		t.Errorf("Corpus = %s, want {\"repo\":\"cc-notes\"}", got)
	}
	if len(qs.Questions) != 2 {
		t.Fatalf("len(Questions) = %d, want 2", len(qs.Questions))
	}
	q := qs.Questions[0]
	want := Question{
		ID: "q1", Repo: "cc-notes", Query: "how does fold linearize",
		Category: "mechanism", Axis: "single-hop",
		GoldEntityIDs: []model.EntityID{"aaaa"}, GoldAnswer: "lamport then author time",
		MustNotRetrieve: []model.EntityID{"bbbb"}, Notes: "n",
	}
	if q.ID != want.ID || q.Repo != want.Repo || q.Query != want.Query || q.Category != want.Category ||
		q.Axis != want.Axis || q.GoldAnswer != want.GoldAnswer || q.Notes != want.Notes || q.ExpectAbstain {
		t.Errorf("Questions[0] = %+v, want %+v", q, want)
	}
	if !slices.Equal(q.GoldEntityIDs, want.GoldEntityIDs) {
		t.Errorf("GoldEntityIDs = %v, want %v", q.GoldEntityIDs, want.GoldEntityIDs)
	}
	if !slices.Equal(q.MustNotRetrieve, want.MustNotRetrieve) {
		t.Errorf("MustNotRetrieve = %v, want %v", q.MustNotRetrieve, want.MustNotRetrieve)
	}
	if !qs.Questions[1].ExpectAbstain || len(qs.Questions[1].GoldEntityIDs) != 0 {
		t.Errorf("Questions[1] = %+v, want an abstain question with no gold", qs.Questions[1])
	}
}

func TestDecodeQuestionsRejects(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		want    error
		wantMsg string
	}{
		{name: "malformed json", data: `{"version":1,`, wantMsg: "unexpected end of JSON input"},
		{name: "version zero", data: `{"questions":[{"id":"a","query":"q","gold_entity_ids":["x"]}]}`, want: ErrUnsupportedVersion},
		{name: "version two", data: `{"version":2,"questions":[{"id":"a","query":"q","gold_entity_ids":["x"]}]}`, want: ErrUnsupportedVersion},
		{name: "no questions", data: `{"version":1,"questions":[]}`, want: ErrEmptyQuestionSet},
		{name: "empty id", data: `{"version":1,"questions":[{"id":"","query":"q","gold_entity_ids":["x"]}]}`, want: ErrInvalidQuestion},
		{name: "duplicate id", data: `{"version":1,"questions":[` +
			`{"id":"a","query":"q","gold_entity_ids":["x"]},` +
			`{"id":"a","query":"r","gold_entity_ids":["y"]}]}`, want: ErrInvalidQuestion},
		{name: "empty query", data: `{"version":1,"questions":[{"id":"a","query":"","gold_entity_ids":["x"]}]}`, want: ErrInvalidQuestion},
		{name: "abstain with gold", data: `{"version":1,"questions":[{"id":"a","query":"q","gold_entity_ids":["x"],"expect_abstain":true}]}`, want: ErrInvalidQuestion},
		{name: "graded without gold", data: `{"version":1,"questions":[{"id":"a","query":"q","gold_entity_ids":[]}]}`, want: ErrInvalidQuestion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeQuestions([]byte(tc.data))
			if err == nil {
				t.Fatalf("DecodeQuestions(%s) = nil error, want a failure", tc.data)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("DecodeQuestions error = %v, want %v", err, tc.want)
			}
			if tc.wantMsg != "" && err.Error() != tc.wantMsg {
				t.Fatalf("DecodeQuestions error = %q, want %q", err, tc.wantMsg)
			}
		})
	}
}

func TestLoadQuestions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "questions.json")
	if err := os.WriteFile(path, []byte(validSet), 0o600); err != nil {
		t.Fatalf("write question set: %v", err)
	}
	qs, err := LoadQuestions(path)
	if err != nil {
		t.Fatalf("LoadQuestions: %v", err)
	}
	if len(qs.Questions) != 2 || qs.Questions[0].ID != "q1" {
		t.Errorf("LoadQuestions = %+v, want the two-question set", qs.Questions)
	}

	if _, err := LoadQuestions(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadQuestions(missing) = %v, want os.ErrNotExist", err)
	}
}

func TestQuestionSetRepos(t *testing.T) {
	qs := QuestionSet{Questions: []Question{
		{ID: "a", Repo: "zeta"},
		{ID: "b", Repo: "alpha"},
		{ID: "c", Repo: "zeta"},
	}}
	if got := qs.Repos(); !slices.Equal(got, []string{"alpha", "zeta"}) {
		t.Errorf("Repos() = %v, want [alpha zeta]", got)
	}
	got := qs.ForRepo("zeta")
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Errorf("ForRepo(zeta) = %+v, want questions a and c in file order", got)
	}
	if got := qs.ForRepo("absent"); got != nil {
		t.Errorf("ForRepo(absent) = %v, want nil", got)
	}
}
