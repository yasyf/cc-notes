package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/rank"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

const widgetSource = "package pkg\n\ntype Widget struct{}\n\nfunc (w Widget) Spin() int { return 1 }\n"

// clock is the capture time every test freezes against, so no manifest depends
// on when the suite ran.
var clock = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

// fixture is a real repository holding a small cc-notes corpus, a snapshot
// root to freeze it into, and the question set graded against it.
type fixture struct {
	client    *notes.Client
	dir       string
	root      string
	questions []eval.Question
	path      string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := gittest.InitRepo(t)
	t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
	writeFile(t, dir, "pkg/widget.go", widgetSource)
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "root")
	c, err := notes.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}

	spin := verifiedNote(t, c, "Spin returns one",
		"Widget.Spin counts rotor ticks and returns one per revolution.", "pkg/widget.go")
	old := note(t, c, "the rotor is balanced by hand", "Shim the rotor against the jig before every run.")
	fresh := note(t, c, "the rotor is balanced on the jig", "The hand shim was replaced by the jig fixture.")
	if _, err := c.SupersedeNote(t.Context(), old, fresh); err != nil {
		t.Fatalf("SupersedeNote: %v", err)
	}
	if _, _, err := c.CreateLog(t.Context(), notes.LogSpec{Title: "rotor bring-up"}); err != nil {
		t.Fatalf("CreateLog: %v", err)
	}

	f := fixture{
		client: c,
		dir:    dir,
		root:   t.TempDir(),
		questions: []eval.Question{
			{
				ID: "q1", Repo: dir, Category: "factual",
				Query:         "what does Widget.Spin return per revolution",
				GoldEntityIDs: []model.EntityID{spin},
			},
			{
				ID: "q2", Repo: dir, Category: "temporal",
				Query:           "how is the rotor balanced",
				GoldEntityIDs:   []model.EntityID{fresh},
				MustNotRetrieve: []model.EntityID{old},
			},
		},
	}
	f.path = f.writeQuestions(t, f.questions)
	return f
}

// capture folds the fixture's live corpus at the fixed clock.
func (f fixture) capture(t *testing.T) Corpus {
	t.Helper()
	c, err := Capture(t.Context(), f.dir, clock)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	return c
}

// freeze captures and writes, returning the corpus that landed on disk.
func (f fixture) freeze(t *testing.T) Corpus {
	t.Helper()
	if _, err := Write(f.root, f.capture(t)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return f.load(t)
}

func (f fixture) load(t *testing.T) Corpus {
	t.Helper()
	c, err := Load(f.root, f.dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func (f fixture) stamp(t *testing.T) {
	t.Helper()
	defects, err := Stamp(f.root, f.dir, f.path, f.questions, clock)
	if err != nil {
		t.Fatalf("Stamp: %v (defects %v)", err, defects)
	}
}

func (f fixture) writeQuestions(t *testing.T, qs []eval.Question) string {
	t.Helper()
	body, err := json.MarshalIndent(eval.QuestionSet{Version: eval.SchemaVersion, Questions: qs}, "", "  ")
	if err != nil {
		t.Fatalf("encode question set: %v", err)
	}
	path := filepath.Join(t.TempDir(), "questions.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write question set: %v", err)
	}
	return path
}

// score is the harness's own report over a corpus, rendered as the table a
// reader would compare by eye. Two corpora that score identically produce
// identical strings.
func score(t *testing.T, c Corpus, qs []eval.Question) string {
	t.Helper()
	report, err := eval.Run(t.Context(), qs, []eval.Config{
		{Name: "bm25", Build: func(seed int64, _ eval.Question) eval.Retriever { return eval.NewBM25(c.Entities, seed) }},
		{Name: "fused", Build: func(seed int64, _ eval.Question) eval.Retriever {
			opts := rank.DefaultOptions()
			opts.Seed = seed
			return rank.New(c.Entities, c.Graph, c.Assessments, opts)
		}},
	}, eval.Options{K: 3, Threshold: 0.1, Seeds: []int64{1, 2, 3, 4, 5}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report.String()
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func note(t *testing.T, c *notes.Client, title, body string) model.EntityID {
	t.Helper()
	n, _, err := c.CreateNote(t.Context(), notes.NoteSpec{Title: title, Body: body})
	if err != nil {
		t.Fatalf("CreateNote %q: %v", title, err)
	}
	return n.ID
}

func verifiedNote(t *testing.T, c *notes.Client, title, body, path string) model.EntityID {
	t.Helper()
	n, _, err := c.CreateNote(t.Context(), notes.NoteSpec{
		Title: title, Body: body, Anchors: notes.AnchorSpec{Paths: []string{path}},
	})
	if err != nil {
		t.Fatalf("CreateNote %q: %v", title, err)
	}
	if _, err := c.VerifyNote(t.Context(), n.ID); err != nil {
		t.Fatalf("VerifyNote: %v", err)
	}
	return n.ID
}

func ids(entities []eval.Entity) []model.EntityID {
	out := make([]model.EntityID, len(entities))
	for i, e := range entities {
		out[i] = e.ID
	}
	return out
}

func TestCaptureWriteLoadRoundTripsTheMeasurement(t *testing.T) {
	f := newFixture(t)
	captured := f.capture(t)
	if len(captured.Entities) != 4 {
		t.Fatalf("captured %d entities, want 4", len(captured.Entities))
	}
	if _, err := Write(f.root, captured); err != nil {
		t.Fatalf("Write: %v", err)
	}
	loaded := f.load(t)

	if got, want := ids(loaded.Entities), ids(captured.Entities); !reflect.DeepEqual(got, want) {
		t.Errorf("loaded entity ids = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(loaded.Assessments, captured.Assessments) {
		t.Errorf("loaded assessments = %+v, want %+v", loaded.Assessments, captured.Assessments)
	}
	if !reflect.DeepEqual(loaded.Graph, captured.Graph) {
		t.Errorf("loaded graph = %d/%d/%d nodes/edges/events, want %d/%d/%d",
			len(loaded.Graph.Nodes), len(loaded.Graph.Edges), len(loaded.Graph.Events),
			len(captured.Graph.Nodes), len(captured.Graph.Edges), len(captured.Graph.Events))
	}
	if got, want := score(t, loaded, f.questions), score(t, captured, f.questions); got != want {
		t.Errorf("the frozen corpus scores\n%s\nwant the captured corpus's\n%s", got, want)
	}
	if got, want := loaded.Manifest.CapturedAt, clock; !got.Equal(want) {
		t.Errorf("CapturedAt = %s, want %s", got, want)
	}
	if !loaded.Manifest.Policy.Now.Equal(clock) {
		t.Errorf("Policy.Now = %s, want the capture clock %s", loaded.Manifest.Policy.Now, clock)
	}
}

func TestTwoLoadsOfOneSnapshotAreIdentical(t *testing.T) {
	f := newFixture(t)
	f.freeze(t)
	first, second := f.load(t), f.load(t)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two loads of one snapshot differ")
	}
	if got, want := score(t, second, f.questions), score(t, first, f.questions); got != want {
		t.Errorf("second load scores\n%s\nwant\n%s", got, want)
	}
}

// TestSnapshotHoldsTheNumbersStillWhileTheCorpusDrifts is the freeze itself:
// records written after the capture reach the live fold and move the score,
// and the snapshot does not see them.
func TestSnapshotHoldsTheNumbersStillWhileTheCorpusDrifts(t *testing.T) {
	f := newFixture(t)
	frozen := f.freeze(t)
	before := score(t, frozen, f.questions)

	for i := range 6 {
		note(t, f.client, fmt.Sprintf("rotor balanced rotor balanced %d", i), "rotor balanced on the jig rotor")
	}

	drifted := f.capture(t)
	if len(drifted.Entities) != len(frozen.Entities)+6 {
		t.Fatalf("a fresh capture holds %d entities, want %d — the fixture did not drift",
			len(drifted.Entities), len(frozen.Entities)+6)
	}
	if live := score(t, drifted, f.questions); live == before {
		t.Fatalf("the drifted corpus scores identically to the frozen one, so this test proves nothing:\n%s", live)
	}
	if after := score(t, f.load(t), f.questions); after != before {
		t.Errorf("the snapshot scored\n%s\nafter the drift, want the frozen\n%s", after, before)
	}
}

func TestLoadRejectsATamperedPart(t *testing.T) {
	f := newFixture(t)
	f.freeze(t)
	path := filepath.Join(Dir(f.root, f.dir), corpusPart)
	body, err := os.ReadFile(path) //nolint:gosec // G304: the path is this test's own temp directory.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(body, []byte("{\"id\":\"deadbeef\",\"kind\":\"note\",\"title\":\"smuggled\"}\n")...), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if _, err := Load(f.root, f.dir); !errors.Is(err, ErrPartDigest) {
		t.Errorf("Load over an appended corpus part = %v, want ErrPartDigest", err)
	}
}

func TestLoadRejectsAnotherRepositorysSnapshot(t *testing.T) {
	f := newFixture(t)
	f.freeze(t)
	other := filepath.Join(t.TempDir(), filepath.Base(f.dir))
	if err := os.MkdirAll(other, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", other, err)
	}
	if _, err := Load(f.root, other); !errors.Is(err, ErrRepoMismatch) {
		t.Errorf("Load(%s) = %v, want ErrRepoMismatch", other, err)
	}
}

// TestOpenRefusesUntilTheLabelsAreRevalidated pins the forcing function: every
// way the corpus or the labels can move voids the stamp, and only a fresh
// Stamp lets the harness run again.
func TestOpenRefusesUntilTheLabelsAreRevalidated(t *testing.T) {
	cases := []struct {
		name string
		void func(*testing.T, fixture)
		want error
	}{
		{
			name: "a fresh capture is unstamped",
			void: func(t *testing.T, f fixture) { f.freeze(t) },
			want: ErrNotValidated,
		},
		{
			name: "re-capturing voids the stamp",
			void: func(t *testing.T, f fixture) {
				f.freeze(t)
				f.stamp(t)
				note(t, f.client, "a record written after the stamp", "which no gold set names")
				f.freeze(t)
			},
			want: ErrNotValidated,
		},
		{
			name: "editing a gold label voids the stamp",
			void: func(t *testing.T, f fixture) {
				f.freeze(t)
				f.stamp(t)
				edited := append([]eval.Question(nil), f.questions...)
				edited[0].GoldEntityIDs = edited[1].GoldEntityIDs
				body, err := json.MarshalIndent(eval.QuestionSet{Version: eval.SchemaVersion, Questions: edited}, "", "  ")
				if err != nil {
					t.Fatalf("encode question set: %v", err)
				}
				if err := os.WriteFile(f.path, body, 0o600); err != nil {
					t.Fatalf("write question set: %v", err)
				}
			},
			want: ErrQuestionsDrift,
		},
		{
			name: "hand-editing the manifest voids the stamp",
			void: func(t *testing.T, f fixture) {
				f.freeze(t)
				f.stamp(t)
				path := filepath.Join(Dir(f.root, f.dir), manifestFile)
				body, err := os.ReadFile(path) //nolint:gosec // G304: the path is this test's own temp directory.
				if err != nil {
					t.Fatalf("read %s: %v", path, err)
				}
				if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
			},
			want: ErrSnapshotDrift,
		},
		{
			name: "a stamped snapshot opens",
			void: func(t *testing.T, f fixture) {
				f.freeze(t)
				f.stamp(t)
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.void(t, f)
			_, err := Open(f.root, f.dir, f.path)
			if !errors.Is(err, tc.want) {
				t.Errorf("Open = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestLabelsCatchesAnEntityTheCorpusDoesNotHold is the q032 rot: a must-not id
// that no record answers to makes the leak check vacuous, and nothing in the
// question schema notices.
func TestLabelsCatchesAnEntityTheCorpusDoesNotHold(t *testing.T) {
	f := newFixture(t)
	frozen := f.freeze(t)
	rotted := []eval.Question{
		{
			ID: "q1", Repo: f.dir, Query: "what does Spin return",
			GoldEntityIDs: []model.EntityID{"0000000000000000000000000000000000000000"},
		},
		{
			ID: "q2", Repo: f.dir, Query: "how is the rotor balanced",
			GoldEntityIDs:   f.questions[1].GoldEntityIDs,
			MustNotRetrieve: []model.EntityID{"1111111111111111111111111111111111111111"},
		},
	}
	want := []Defect{
		{Question: "q1", Field: FieldGold, ID: "0000000000000000000000000000000000000000"},
		{Question: "q2", Field: FieldMustNot, ID: "1111111111111111111111111111111111111111"},
	}
	if got := Labels(frozen, rotted); !reflect.DeepEqual(got, want) {
		t.Errorf("Labels = %v, want %v", got, want)
	}
	if got := Labels(frozen, f.questions); len(got) != 0 {
		t.Errorf("Labels over the graded set = %v, want none", got)
	}

	defects, err := Stamp(f.root, f.dir, f.path, rotted, clock)
	if !errors.Is(err, ErrBrokenLabels) {
		t.Errorf("Stamp over rotted labels = %v, want ErrBrokenLabels", err)
	}
	if !reflect.DeepEqual(defects, want) {
		t.Errorf("Stamp defects = %v, want %v", defects, want)
	}
	if _, err := Open(f.root, f.dir, f.path); !errors.Is(err, ErrNotValidated) {
		t.Errorf("Open after a refused stamp = %v, want ErrNotValidated", err)
	}
}

func TestDiffReportsWhatARefreshChanged(t *testing.T) {
	f := newFixture(t)
	changed := note(t, f.client, "the tick counter width", "The counter is 16 bits wide.")
	before := f.capture(t)

	added := note(t, f.client, "the jig fixture torque spec", "40 Nm, checked every rebuild.")
	body := "The counter is 16 bits wide and wraps silently."
	if _, err := f.client.EditNote(t.Context(), changed, notes.NoteEdit{Body: &body}); err != nil {
		t.Fatalf("EditNote: %v", err)
	}
	after := f.capture(t)

	got := Diff(before, after)
	if !reflect.DeepEqual(got.Added, []model.EntityID{added}) {
		t.Errorf("Added = %v, want [%s]", got.Added, added)
	}
	if !reflect.DeepEqual(got.Changed, []model.EntityID{changed}) {
		t.Errorf("Changed = %v, want [%s]", got.Changed, changed)
	}
	if len(got.Removed) != 0 {
		t.Errorf("Removed = %v, want none", got.Removed)
	}
	if got.Empty() {
		t.Error("Empty = true over a refresh that added and rewrote a record")
	}
	if same := Diff(before, before); !same.Empty() {
		t.Errorf("Diff of a capture with itself = %+v, want empty", same)
	}
}

func TestDiffReportsARecordTheGateNewlyWithholds(t *testing.T) {
	f := newFixture(t)
	target := verifiedNote(t, f.client, "the rotor jig alignment", "Align the jig to the rotor axis.", "pkg/widget.go")
	before := f.capture(t)

	if _, err := f.client.ExpireNote(t.Context(), target, "the jig was retired"); err != nil {
		t.Fatalf("ExpireNote: %v", err)
	}
	after := f.capture(t)

	if got := Diff(before, after); !reflect.DeepEqual(got.Regated, []model.EntityID{target}) {
		t.Errorf("Regated = %v, want [%s]", got.Regated, target)
	}
}
