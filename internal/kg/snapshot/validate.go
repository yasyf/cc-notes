package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/stale"
	"github.com/yasyf/cc-notes/model"
)

// Errors returned when a snapshot's gold labels are not, or no longer,
// validated against the question set being measured.
var (
	ErrNotValidated   = errors.New("snapshot gold labels have not been validated")
	ErrSnapshotDrift  = errors.New("snapshot changed since its gold labels were validated")
	ErrQuestionsDrift = errors.New("question set changed since its gold labels were validated")
	ErrBrokenLabels   = errors.New("question set names entities the snapshot does not hold")
)

// Field names the label list a defect sits in.
type Field string

// The label lists a question binds to entity ids.
const (
	FieldGold    Field = "gold_entity_ids"
	FieldMustNot Field = "must_not_retrieve"
)

// Defect is one label the snapshot cannot honour: an entity id a question
// names that no record in the corpus answers to. Both directions matter — a
// gold id absent from the corpus makes the question unanswerable, and a
// must-not id absent from it makes the leak check vacuous.
type Defect struct {
	Question string
	Field    Field
	ID       model.EntityID
}

// String renders a defect as one review line.
func (d Defect) String() string {
	return fmt.Sprintf("%s %s names %s, which the snapshot does not hold", d.Question, d.Field, d.ID.Short())
}

// Validation is the record Stamp writes: the snapshot manifest and the
// question file whose labels were checked against it, both by digest. A
// capture rewrites the manifest and an edited label rewrites the question
// file, so either voids the stamp and Open refuses until the labels are
// re-checked.
type Validation struct {
	Version   int       `json:"version"`
	Manifest  string    `json:"manifest_sha256"`
	Questions string    `json:"questions_sha256"`
	Path      string    `json:"questions_path"`
	Checked   int       `json:"questions_checked"`
	At        time.Time `json:"validated_at"`
}

// Labels reports every gold and must-not entity id in qs that the corpus does
// not hold, in question then field order. An empty result is what Stamp
// requires.
func Labels(c Corpus, qs []eval.Question) []Defect {
	held := make(map[model.EntityID]struct{}, len(c.Entities))
	for _, e := range c.Entities {
		held[e.ID] = struct{}{}
	}
	var out []Defect
	for _, q := range qs {
		for _, list := range []struct {
			field Field
			ids   []model.EntityID
		}{{FieldGold, q.GoldEntityIDs}, {FieldMustNot, q.MustNotRetrieve}} {
			for _, id := range list.ids {
				if _, ok := held[id]; !ok {
					out = append(out, Defect{Question: q.ID, Field: list.field, ID: id})
				}
			}
		}
	}
	return out
}

// Stamp records that the question file at path was re-validated against the
// snapshot of repo under root, and returns the defects that stopped it. It
// refuses while any label is broken.
func Stamp(root, repo, path string, qs []eval.Question, now time.Time) ([]Defect, error) {
	c, err := Load(root, repo)
	if err != nil {
		return nil, err
	}
	if defects := Labels(c, qs); len(defects) > 0 {
		return defects, fmt.Errorf("%w: %d in %s", ErrBrokenLabels, len(defects), path)
	}
	dir := Dir(root, repo)
	manifest, err := digestFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return nil, err
	}
	questions, err := digestFile(path)
	if err != nil {
		return nil, err
	}
	body, err := json.MarshalIndent(Validation{
		Version:   Version,
		Manifest:  manifest,
		Questions: questions,
		Path:      path,
		Checked:   len(qs),
		At:        now.UTC(),
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", validatedFile, err)
	}
	if err := os.WriteFile(filepath.Join(dir, validatedFile), append(body, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", validatedFile, err)
	}
	return nil, nil
}

// checkStamp resolves whether the snapshot in dir still carries a validation
// covering the question file at questions.
func checkStamp(dir, questions string) error {
	path := filepath.Join(dir, validatedFile)
	body, err := os.ReadFile(path) //nolint:gosec // G304: the snapshot directory is the operator-supplied path this harness reads.
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s — re-check them and run kgsnap -stamp", ErrNotValidated, dir)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var v Validation
	if err := json.Unmarshal(body, &v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if v.Version != Version {
		return fmt.Errorf("%w: %s is %d (want %d)", ErrUnsupportedVersion, path, v.Version, Version)
	}
	manifest, err := digestFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return err
	}
	if manifest != v.Manifest {
		return fmt.Errorf("%w: %s — re-check the gold labels and run kgsnap -stamp", ErrSnapshotDrift, dir)
	}
	current, err := digestFile(questions)
	if err != nil {
		return err
	}
	if current != v.Questions {
		return fmt.Errorf("%w: %s — re-check the gold labels and run kgsnap -stamp", ErrQuestionsDrift, questions)
	}
	return nil
}

// Delta is what moved between two captures of one repository. Added entities
// carry no label in any gold or must-not set, so they enter every query's
// candidate pool ungraded; Regated entities changed the gate's answer without
// changing their own text. Both are re-validation work a human has to do,
// which is why a refresh prints them and stamps nothing.
type Delta struct {
	Added   []model.EntityID
	Removed []model.EntityID
	Changed []model.EntityID
	Regated []model.EntityID
}

// Empty reports whether the two captures hold the same records, the same text,
// and the same gate verdicts.
func (d Delta) Empty() bool {
	return len(d.Added)+len(d.Removed)+len(d.Changed)+len(d.Regated) == 0
}

// verdict is the gate's answer for one record, as the delta compares it.
type verdict struct {
	gated  bool
	signal stale.Signal
}

// Diff compares two captures of one repository. Both entity lists are sorted
// by id, so every result is too.
func Diff(before, after Corpus) Delta {
	old, fresh := texts(before), texts(after)
	wasGated, isGated := verdicts(before), verdicts(after)
	var d Delta
	for _, e := range after.Entities {
		text, ok := old[e.ID]
		if !ok {
			d.Added = append(d.Added, e.ID)
			continue
		}
		if text != e.Text() {
			d.Changed = append(d.Changed, e.ID)
		}
		if wasGated[e.ID] != isGated[e.ID] {
			d.Regated = append(d.Regated, e.ID)
		}
	}
	for _, e := range before.Entities {
		if _, ok := fresh[e.ID]; !ok {
			d.Removed = append(d.Removed, e.ID)
		}
	}
	return d
}

func texts(c Corpus) map[model.EntityID]string {
	out := make(map[model.EntityID]string, len(c.Entities))
	for _, e := range c.Entities {
		out[e.ID] = e.Text()
	}
	return out
}

func verdicts(c Corpus) map[model.EntityID]verdict {
	out := make(map[model.EntityID]verdict, len(c.Assessments))
	for _, a := range c.Assessments {
		out[a.ID] = verdict{gated: a.Gated, signal: a.Signal}
	}
	return out
}
