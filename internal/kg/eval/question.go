package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

// SchemaVersion is the only question-set schema version this harness reads.
const SchemaVersion = 1

// Session is the ambient state a query is asked from: the branch the agent is
// on and the repository paths it has touched. It mirrors exactly what the
// product sets on every real query (internal/cli, kg query --branch/--path), so
// a configuration that threads it measures what ships and one that leaves it
// zero measures a session-free abstraction. A question that names neither is
// asked from the zero session, and both configurations score it identically.
type Session struct {
	Branch model.Branch `json:"branch,omitempty"`
	Paths  []string     `json:"paths,omitempty"`
}

// Empty reports whether the session carries no ambient state at all.
func (s Session) Empty() bool { return s.Branch == "" && len(s.Paths) == 0 }

// Question is one evaluation item: a natural-language query against the
// cc-notes corpus of the git repository at Repo, asked from Session, with the
// entities a correct retrieval must surface (GoldEntityIDs), the entities it
// must not (MustNotRetrieve — superseded, expired, or otherwise temporally
// wrong records), and whether the right answer is to retrieve nothing
// (ExpectAbstain).
//
// Category and Axis are the two independent breakdown dimensions the report
// groups by. GoldAnswer and Notes are provenance for a human reader; the
// harness scores retrieval, not generation, and reads neither.
type Question struct {
	ID              string           `json:"id"`
	Repo            string           `json:"repo"`
	Query           string           `json:"query"`
	Session         Session          `json:"session,omitempty"`
	Category        string           `json:"category"`
	Axis            string           `json:"axis"`
	GoldEntityIDs   []model.EntityID `json:"gold_entity_ids"`
	GoldAnswer      string           `json:"gold_answer"`
	MustNotRetrieve []model.EntityID `json:"must_not_retrieve"`
	ExpectAbstain   bool             `json:"expect_abstain"`
	Notes           string           `json:"notes"`
}

// QuestionSet is a decoded question file. Corpus is the file's corpus
// descriptor carried verbatim: the harness binds a Question's Repo to an
// on-disk repository itself, so it never interprets the descriptor's shape.
type QuestionSet struct {
	Version   int             `json:"version"`
	Corpus    json.RawMessage `json:"corpus"`
	Questions []Question      `json:"questions"`
}

// Errors returned when a question file does not satisfy the schema.
var (
	ErrUnsupportedVersion = errors.New("unsupported question set version")
	ErrEmptyQuestionSet   = errors.New("question set has no questions")
	ErrInvalidQuestion    = errors.New("invalid question")
)

// LoadQuestions reads and validates the question file at path.
func LoadQuestions(path string) (QuestionSet, error) {
	//nolint:gosec // G304: path is the operator-supplied question-set file for this harness; reading it is the intended behavior.
	data, err := os.ReadFile(path)
	if err != nil {
		return QuestionSet{}, fmt.Errorf("read question set %s: %w", path, err)
	}
	qs, err := DecodeQuestions(data)
	if err != nil {
		return QuestionSet{}, fmt.Errorf("decode question set %s: %w", path, err)
	}
	return qs, nil
}

// DecodeQuestions decodes and validates a question set from JSON. A version
// other than SchemaVersion is ErrUnsupportedVersion, an empty question list is
// ErrEmptyQuestionSet, and a question missing an id or query, carrying a
// duplicate id, expecting abstention while naming gold entities, or expecting a
// non-abstain answer with no gold entities is ErrInvalidQuestion.
func DecodeQuestions(data []byte) (QuestionSet, error) {
	var qs QuestionSet
	if err := json.Unmarshal(data, &qs); err != nil {
		return QuestionSet{}, err
	}
	if qs.Version != SchemaVersion {
		return QuestionSet{}, fmt.Errorf("%w: %d (want %d)", ErrUnsupportedVersion, qs.Version, SchemaVersion)
	}
	if len(qs.Questions) == 0 {
		return QuestionSet{}, ErrEmptyQuestionSet
	}
	seen := make(map[string]struct{}, len(qs.Questions))
	for i, q := range qs.Questions {
		if err := validateQuestion(q, seen); err != nil {
			return QuestionSet{}, fmt.Errorf("question %d: %w", i, err)
		}
		seen[q.ID] = struct{}{}
	}
	return qs, nil
}

func validateQuestion(q Question, seen map[string]struct{}) error {
	if q.ID == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidQuestion)
	}
	if _, dup := seen[q.ID]; dup {
		return fmt.Errorf("%w: duplicate id %q", ErrInvalidQuestion, q.ID)
	}
	if q.Query == "" {
		return fmt.Errorf("%w: %s has an empty query", ErrInvalidQuestion, q.ID)
	}
	if q.ExpectAbstain && len(q.GoldEntityIDs) > 0 {
		return fmt.Errorf("%w: %s expects abstention but names %d gold entities", ErrInvalidQuestion, q.ID, len(q.GoldEntityIDs))
	}
	if !q.ExpectAbstain && len(q.GoldEntityIDs) == 0 {
		return fmt.Errorf("%w: %s names no gold entities and does not expect abstention", ErrInvalidQuestion, q.ID)
	}
	return nil
}

// ForRepo returns the questions naming the repository directory repo, in file
// order.
func (qs QuestionSet) ForRepo(repo string) []Question {
	var out []Question
	for _, q := range qs.Questions {
		if q.Repo == repo {
			out = append(out, q)
		}
	}
	return out
}

// Graded returns the questions naming gold entities, in input order — the only
// ones NDCG, recall, and MRR are defined on, and so the only ones a paired
// comparison or a fold split can carry.
func Graded(questions []Question) []Question {
	var out []Question
	for _, q := range questions {
		if len(q.GoldEntityIDs) > 0 {
			out = append(out, q)
		}
	}
	return out
}

// Sessioned counts the questions carrying ambient session state. A set where
// this is zero cannot distinguish the product's configuration from the
// session-free one, whatever the harness threads.
func Sessioned(questions []Question) int {
	n := 0
	for _, q := range questions {
		if !q.Session.Empty() {
			n++
		}
	}
	return n
}

// Repos returns the distinct repository directories the questions name, sorted.
func (qs QuestionSet) Repos() []string {
	var out []string
	for _, q := range qs.Questions {
		if !slices.Contains(out, q.Repo) {
			out = append(out, q.Repo)
		}
	}
	slices.Sort(out)
	return out
}
