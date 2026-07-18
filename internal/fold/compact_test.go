package fold_test

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

// cp builds a checkpoint commit: a single-parent commit carrying one Checkpoint
// op whose State is the folded snapshot of the covered prefix.
func cp(sha, parent, author string, at int64, lamport uint64, state model.Snapshot, coversLamport uint64, covers ...string) model.PackCommit {
	shas := make([]model.SHA, len(covers))
	for i, c := range covers {
		shas[i] = model.SHA(c)
	}
	op := model.Checkpoint{EntityID: state.EntityID(), State: state, CoversLamport: model.Lamport(coversLamport), CoversShas: shas}
	return mk(sha, []string{parent}, author, at, lamport, op)
}

func shuffled(commits []model.PackCommit, r *rand.Rand) []model.PackCommit {
	out := slices.Clone(commits)
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// TestFoldNoteCompactedEqualsFull folds a note chain that contains a seed-safe
// checkpoint and the identical chain whose checkpoint slot is an empty no-op
// commit. The lamports, parents, authors, and tip are identical; only whether
// the prefix seeds or replays differs. The two folds must be byte-for-byte
// identical, including Head.
func TestFoldNoteCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T0", Body: "B0", Tags: []string{"a"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T1"}, model.AddTag{Tag: "b"})
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}})
	state, err := fold.Note([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "dave", 400, 5, model.SetBody{Body: "B3"}, model.RemoveTag{Tag: "a"})
	c4 := mk("c4", []string{"c3"}, "erin", 500, 6,
		model.AddTag{Tag: "c"},
		model.VerifyNote{Witness: []model.AnchorWitness{{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}, OID: "oid"}}, VerifiedCommit: "c4"},
	)
	compacted := []model.PackCommit{c0, c1, c2, cK, c3, c4}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3, c4}

	gotFull, err := fold.Note(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Note(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	if gotCompact.Head != "c4" {
		t.Fatalf("Head = %q, want c4", gotCompact.Head)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 30 {
		got, err := fold.Note(shuffled(compacted, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, gotFull) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, got, gotFull)
		}
	}
}

// TestFoldDocCompactedEqualsFull is the doc analog of the note test: edits, a
// When change, a tag/anchor mutation, and a verify straddle a seed-safe
// checkpoint. The two folds must be byte-for-byte identical, including Head and
// the seeded When/verify fields.
func TestFoldDocCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateDoc{Nonce: "n", Title: "T0", Body: "B0", When: "W0", Tags: []string{"a"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T1"}, model.SetWhen{When: "W1"}, model.AddTag{Tag: "b"})
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}})
	state, err := fold.Doc([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "dave", 400, 5, model.SetBody{Body: "B3"}, model.SetWhen{When: "W3"}, model.RemoveTag{Tag: "a"})
	c4 := mk("c4", []string{"c3"}, "erin", 500, 6,
		model.AddTag{Tag: "c"},
		model.VerifyNote{Witness: []model.AnchorWitness{{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}, OID: "oid"}}, VerifiedCommit: "c4"},
	)
	compacted := []model.PackCommit{c0, c1, c2, cK, c3, c4}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3, c4}

	gotFull, err := fold.Doc(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Doc(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	if gotCompact.When != "W3" {
		t.Fatalf("When = %q, want W3", gotCompact.When)
	}
	if gotCompact.VerifiedAt != 500 || gotCompact.Head != "c4" {
		t.Fatalf("VerifiedAt/Head = (%d,%q), want (500,c4)", gotCompact.VerifiedAt, gotCompact.Head)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(17, 19))
	for i := range 30 {
		got, err := fold.Doc(shuffled(compacted, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, gotFull) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, got, gotFull)
		}
	}
}

// TestFoldDocCheckpointOverNonDocMismatch confirms that seeding a doc fold from a
// checkpoint whose State is a non-doc snapshot fails with ErrKindMismatch.
func TestFoldDocCheckpointOverNonDocMismatch(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T2"})
	note, err := fold.Note([]model.PackCommit{c0, c1})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c1", "compactor", 250, 3, note, 2, "c0", "c1")
	if _, err := fold.Doc([]model.PackCommit{c0, c1, cK}); !errors.Is(err, fold.ErrKindMismatch) {
		t.Fatalf("fold err = %v, want ErrKindMismatch", err)
	}
}

// TestFoldLogCompactedEqualsFull is the log analog: appended entries, a title
// edit, and a tag/anchor mutation straddle a seed-safe checkpoint. The two folds
// must be byte-for-byte identical, including the seeded entry slice, its order,
// and Head.
func TestFoldLogCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateLog{Nonce: "n", Title: "T0", Tags: []string{"a"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.AppendEntry{Text: "first"}, model.SetTitle{Title: "T1"}, model.AddTag{Tag: "b"})
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AppendEntry{Text: "second"}, model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}})
	state, err := fold.Log([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "dave", 400, 5, model.AppendEntry{Text: "third"}, model.RemoveTag{Tag: "a"})
	c4 := mk("c4", []string{"c3"}, "erin", 500, 6, model.AppendEntry{Text: "fourth"}, model.AddTag{Tag: "c"})
	compacted := []model.PackCommit{c0, c1, c2, cK, c3, c4}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3, c4}

	gotFull, err := fold.Log(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Log(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	wantEntries := []model.LogEntry{
		{Author: "bob", TS: 200, Text: "first"},
		{Author: "carol", TS: 300, Text: "second"},
		{Author: "dave", TS: 400, Text: "third"},
		{Author: "erin", TS: 500, Text: "fourth"},
	}
	if !reflect.DeepEqual(gotCompact.Entries, wantEntries) {
		t.Fatalf("Entries = %+v, want %+v", gotCompact.Entries, wantEntries)
	}
	if gotCompact.Head != "c4" {
		t.Fatalf("Head = %q, want c4", gotCompact.Head)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(23, 29))
	for i := range 30 {
		got, err := fold.Log(shuffled(compacted, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, gotFull) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, got, gotFull)
		}
	}
}

// TestFoldLogModelSurvivesCompaction seeds a log fold from a checkpoint whose
// State carries model-bearing entries, proving seed() preserves LogEntry.Model
// across compaction. The compacted and full folds must be byte-identical.
func TestFoldLogModelSurvivesCompaction(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateLog{Nonce: "n", Title: "papercuts", Tags: []string{"papercut"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.AppendEntry{Text: "first", Model: "claude-opus-4-8"})
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AppendEntry{Text: "second", Model: "claude-fable-5"})
	state, err := fold.Log([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "dave", 400, 5, model.AppendEntry{Text: "third", Model: "claude-sonnet-5"})
	compacted := []model.PackCommit{c0, c1, c2, cK, c3}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3}

	gotFull, err := fold.Log(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Log(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	wantEntries := []model.LogEntry{
		{Author: "bob", TS: 200, Text: "first", Model: "claude-opus-4-8"},
		{Author: "carol", TS: 300, Text: "second", Model: "claude-fable-5"},
		{Author: "dave", TS: 400, Text: "third", Model: "claude-sonnet-5"},
	}
	if !reflect.DeepEqual(gotCompact.Entries, wantEntries) {
		t.Fatalf("Entries = %+v, want %+v", gotCompact.Entries, wantEntries)
	}
}

// TestFoldLogCheckpointOverNonLogMismatch confirms that seeding a log fold from a
// checkpoint whose State is a non-log snapshot fails with ErrKindMismatch.
func TestFoldLogCheckpointOverNonLogMismatch(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T2"})
	note, err := fold.Note([]model.PackCommit{c0, c1})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c1", "compactor", 250, 3, note, 2, "c0", "c1")
	if _, err := fold.Log([]model.PackCommit{c0, c1, cK}); !errors.Is(err, fold.ErrKindMismatch) {
		t.Fatalf("fold err = %v, want ErrKindMismatch", err)
	}
}

// TestFoldTaskCompactedEqualsFull is the task analog: a claim, comment, renew,
// and status transition straddle a seed-safe checkpoint. The lease fields
// (HeartbeatAt, HeartbeatLamport, StartedAt, ClosedAt) must match the full fold.
func TestFoldTaskCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "Fix", Type: model.TypeBug, Priority: 1, Branch: "main", Labels: []string{"ci"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.Claim{Assignee: "bob"})
	c2 := mk("c2", []string{"c1"}, "bob", 300, 3, model.AddComment{Body: "on it"}, model.AddLabel{Label: "sync"}, model.AddCriterion{ID: "cr1", Text: "first"})
	state, err := fold.Task([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "bob", 400, 5, model.Renew{})
	c4 := mk("c4", []string{"c3"}, "bob", 500, 6, model.AddCriterion{ID: "cr2", Text: "second"}, model.SetCriterionStatus{ID: "cr1", Status: model.CriterionMet}, model.SetStatus{Status: model.StatusDone})
	compacted := []model.PackCommit{c0, c1, c2, cK, c3, c4}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3, c4}

	gotFull, err := fold.Task(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Task(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	if gotFull.HeartbeatLamport != 6 || gotFull.HeartbeatAt != 500 {
		t.Fatalf("heartbeat = (%d,%d), want (6,500)", gotFull.HeartbeatLamport, gotFull.HeartbeatAt)
	}
	if gotFull.StartedAt != 200 || gotFull.ClosedAt != 500 {
		t.Fatalf("started/closed = (%d,%d), want (200,500)", gotFull.StartedAt, gotFull.ClosedAt)
	}
	// cr1 is added in the seeded prefix and flipped to met by a replayed
	// suffix op; cr2 is added after the seed. Append order and the seeded
	// criterion's later mutation must survive seeding from the checkpoint.
	if len(gotFull.Criteria) != 2 ||
		gotFull.Criteria[0].ID != "cr1" || gotFull.Criteria[0].Status != model.CriterionMet ||
		gotFull.Criteria[1].ID != "cr2" || gotFull.Criteria[1].Status != model.CriterionPending {
		t.Fatalf("criteria = %+v, want [cr1=met, cr2=pending] in order", gotFull.Criteria)
	}
}

// TestFoldSprintCompactedEqualsFull is the sprint analog: a status transition,
// dates, labels, comments, and a commit link straddle a seed-safe checkpoint.
// The lifecycle stamps (StartedAt, ClosedAt) must match the full fold.
func TestFoldSprintCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateSprint{Nonce: "n", Title: "S", Description: "d", Project: "p0", Labels: []string{"q3"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetSprintStatus{Status: model.SprintActive}, model.AddLabel{Label: "go"})
	c2 := mk("c2", []string{"c1"}, "bob", 300, 3, model.AddComment{Body: "kickoff"}, model.SetStartDate{Date: 1000})
	state, err := fold.Sprint([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "carol", 400, 5, model.SetSprintStatus{Status: model.SprintCompleted}, model.SetEndDate{Date: 2000})
	c4 := mk("c4", []string{"c3"}, "carol", 500, 6, model.LinkCommit{SHA: "sha1"}, model.AddComment{Body: "done"})
	compacted := []model.PackCommit{c0, c1, c2, cK, c3, c4}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3, c4}

	gotFull, err := fold.Sprint(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Sprint(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	if gotFull.StartedAt != 200 || gotFull.ClosedAt != 400 {
		t.Fatalf("started/closed = (%d,%d), want (200,400)", gotFull.StartedAt, gotFull.ClosedAt)
	}
	if gotFull.Status != model.SprintCompleted {
		t.Fatalf("Status = %q, want completed", gotFull.Status)
	}
	if gotFull.StartDate != 1000 || gotFull.EndDate != 2000 {
		t.Fatalf("start/end date = (%d,%d), want (1000,2000)", gotFull.StartDate, gotFull.EndDate)
	}
	if gotFull.Head != "c4" {
		t.Fatalf("Head = %q, want c4", gotFull.Head)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(3, 5))
	for i := range 30 {
		got, err := fold.Sprint(shuffled(compacted, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, gotFull) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, got, gotFull)
		}
	}
}

// TestFoldProjectCompactedEqualsFull is the project analog: a status transition,
// labels, and comments straddle a seed-safe checkpoint. ClosedAt must match.
func TestFoldProjectCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateProject{Nonce: "n", Title: "P", Description: "d", Labels: []string{"core"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.AddLabel{Label: "active-work"}, model.AddComment{Body: "started"})
	c2 := mk("c2", []string{"c1"}, "bob", 300, 3, model.SetTitle{Title: "Proj X"}, model.LinkCommit{SHA: "sha1"})
	state, err := fold.Project([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "carol", 400, 5, model.SetProjectStatus{Status: model.ProjectCompleted}, model.RemoveLabel{Label: "core"})
	c4 := mk("c4", []string{"c3"}, "carol", 500, 6, model.AddComment{Body: "done"})
	compacted := []model.PackCommit{c0, c1, c2, cK, c3, c4}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3, c4}

	gotFull, err := fold.Project(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Project(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	if gotFull.Status != model.ProjectCompleted || gotFull.ClosedAt != 400 {
		t.Fatalf("status/closed = (%q,%d), want (completed,400)", gotFull.Status, gotFull.ClosedAt)
	}
	if !slices.Equal(gotFull.Labels, []string{"active-work"}) {
		t.Fatalf("Labels = %v, want [active-work]", gotFull.Labels)
	}
	if gotFull.Head != "c4" {
		t.Fatalf("Head = %q, want c4", gotFull.Head)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(11, 13))
	for i := range 30 {
		got, err := fold.Project(shuffled(compacted, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, gotFull) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, got, gotFull)
		}
	}
}

// TestFoldRunbookCompactedEqualsFull folds a runbook chain where a run's start
// and results straddle a seed-safe checkpoint, then a post-checkpoint result
// upsert, finish, comment, and archive land in the suffix. The seeded fold must
// equal the full replay, including the deep-cloned run results.
func TestFoldRunbookCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1,
		model.CreateRunbook{Nonce: "n", Title: "RB", Description: "d", Labels: []string{"ops"}},
		model.AddStep{ID: "s1", Text: "build", Command: "", Position: "a"},
	)
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2,
		model.StartRun{ID: "r1", Task: "task0"},
		model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepDone, Note: "ok"},
	)
	c2 := mk("c2", []string{"c1"}, "bob", 300, 3,
		model.AddStep{ID: "s2", Text: "test", Command: "go test", Position: "i"},
		model.SetRunStepStatus{RunID: "r1", StepID: "s2", Status: model.StepDone, Note: "green"},
	)
	state, err := fold.Runbook([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "carol", 400, 5,
		model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepFailed, Note: "redo"},
		model.FinishRun{ID: "r1", Status: model.RunSucceeded},
	)
	c4 := mk("c4", []string{"c3"}, "carol", 500, 6,
		model.AddComment{Body: "done"},
		model.SetRunbookStatus{Status: model.RunbookArchived},
	)
	compacted := []model.PackCommit{c0, c1, c2, cK, c3, c4}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3, c4}

	gotFull, err := fold.Runbook(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Runbook(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	if gotFull.Status != model.RunbookArchived || gotFull.ArchivedAt != 500 {
		t.Fatalf("status/archived = (%q,%d), want (archived,500)", gotFull.Status, gotFull.ArchivedAt)
	}
	if len(gotFull.Runs) != 1 || gotFull.Runs[0].Status != model.RunSucceeded || gotFull.Runs[0].FinishedAt != 400 {
		t.Fatalf("run = %+v, want succeeded/finished-at-400", gotFull.Runs)
	}
	wantResults := []model.RunbookStepResult{
		{StepID: "s1", Status: model.StepFailed, Note: "redo", Actor: "carol", TS: 400},
		{StepID: "s2", Status: model.StepDone, Note: "green", Actor: "bob", TS: 300},
	}
	if !reflect.DeepEqual(gotFull.Runs[0].Results, wantResults) {
		t.Fatalf("results = %+v, want %+v", gotFull.Runs[0].Results, wantResults)
	}
	if gotFull.Head != "c4" {
		t.Fatalf("Head = %q, want c4", gotFull.Head)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(17, 19))
	for i := range 30 {
		got, err := fold.Runbook(shuffled(compacted, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, gotFull) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, got, gotFull)
		}
	}
}

// TestFoldInvestigationCompactedEqualsFull folds an investigation chain where a
// terminal status, a finding disposition, and timeline entries all land before a
// seed-safe checkpoint, then a reopen-after-terminal, more entries, and a
// re-root-cause land in the suffix. The seeded fold must equal the full replay,
// including entries spanning the seed, the pre-checkpoint finding disposition,
// and the ClosedAt/ClosedBy zeroing when the reopen crosses the seed boundary.
func TestFoldInvestigationCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "T0", Premise: "P", Tags: []string{"a"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2,
		model.AppendEntry{Text: "first"},
		model.AddFinding{ID: "f1", Text: "suspect A"},
		model.SetInvestigationStatus{Status: model.InvestigationConfirmed}, // terminal before the checkpoint
	)
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3,
		model.AppendEntry{Text: "second"},
		model.SetFindingStatus{ID: "f1", Status: model.FindingCleared, Note: "ruled out"}, // disposition before the checkpoint
	)
	state, err := fold.Investigation([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "dave", 400, 5,
		model.AppendEntry{Text: "third"},
		model.SetInvestigationStatus{Status: model.InvestigationOpen}, // reopen-after-terminal across the seed boundary
	)
	c4 := mk("c4", []string{"c3"}, "erin", 500, 6,
		model.AppendEntry{Text: "fourth"},
		model.SetRootCause{Text: "unbuffered chan"},
		model.SetInvestigationStatus{Status: model.InvestigationRootCaused},
	)
	compacted := []model.PackCommit{c0, c1, c2, cK, c3, c4}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3, c4}

	gotFull, err := fold.Investigation(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Investigation(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	wantEntries := []model.LogEntry{
		{Author: "bob", TS: 200, Text: "first"},
		{Author: "carol", TS: 300, Text: "second"},
		{Author: "dave", TS: 400, Text: "third"},
		{Author: "erin", TS: 500, Text: "fourth"},
	}
	if !reflect.DeepEqual(gotCompact.Entries, wantEntries) {
		t.Fatalf("Entries = %+v, want %+v", gotCompact.Entries, wantEntries)
	}
	if gotCompact.Status != model.InvestigationRootCaused {
		t.Fatalf("Status = %q, want root_caused", gotCompact.Status)
	}
	if gotCompact.ClosedAt != 0 || gotCompact.ClosedBy != "" {
		t.Fatalf("closed = (%d,%q), want (0,\"\") after reopen across seed", gotCompact.ClosedAt, gotCompact.ClosedBy)
	}
	if len(gotCompact.Findings) != 1 || gotCompact.Findings[0].Status != model.FindingCleared {
		t.Fatalf("Findings = %+v, want [f1=cleared] surviving the seed", gotCompact.Findings)
	}
	if gotCompact.RootCause != "unbuffered chan" || gotCompact.Head != "c4" {
		t.Fatalf("rootCause/head = (%q,%q), want (unbuffered chan,c4)", gotCompact.RootCause, gotCompact.Head)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(31, 37))
	for i := range 30 {
		got, err := fold.Investigation(shuffled(compacted, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, gotFull) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, got, gotFull)
		}
	}
}

// TestFoldInvestigationConcurrentCheckpoints is the investigation convergence
// trap: two replicas each dispose a distinct finding and compact over their own
// frontier, then a union merge joins both checkpoints. The newest-coverage
// checkpoint is not seed-safe, so the fold falls back to the full replay where
// both dispositions survive.
func TestFoldInvestigationConcurrentCheckpoints(t *testing.T) {
	c0 := mk("c0", nil, "root", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "T", Premise: "P"})
	c1 := mk("c1", []string{"c0"}, "root", 150, 2,
		model.AddFinding{ID: "f1", Text: "suspect A"},
		model.AddFinding{ID: "f2", Text: "suspect B"},
	)
	a1 := mk("a1", []string{"c1"}, "alice", 200, 3, model.SetFindingStatus{ID: "f1", Status: model.FindingCleared, Note: "a"})
	b1 := mk("b1", []string{"c1"}, "bob", 210, 3, model.SetFindingStatus{ID: "f2", Status: model.FindingConfirmed, Note: "b"})
	stateA, err := fold.Investigation([]model.PackCommit{c0, c1, a1})
	if err != nil {
		t.Fatalf("fold A: %v", err)
	}
	stateB, err := fold.Investigation([]model.PackCommit{c0, c1, b1})
	if err != nil {
		t.Fatalf("fold B: %v", err)
	}
	ka := cp("ka", "a1", "alice", 250, 4, stateA, 3, "c0", "c1", "a1")
	kb := cp("kb", "b1", "bob", 260, 4, stateB, 3, "c0", "c1", "b1")
	merge := mk("mmm", []string{"ka", "kb"}, "alice", 300, 5)
	combined := []model.PackCommit{c0, c1, a1, ka, b1, kb, merge}

	kaEmpty := mk("ka", []string{"a1"}, "alice", 250, 4)
	kbEmpty := mk("kb", []string{"b1"}, "bob", 260, 4)
	plain := []model.PackCommit{c0, c1, a1, kaEmpty, b1, kbEmpty, merge}

	want, err := fold.Investigation(plain)
	if err != nil {
		t.Fatalf("fold plain: %v", err)
	}
	got, err := fold.Investigation(combined)
	if err != nil {
		t.Fatalf("fold combined: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("combined = %+v\nplain = %+v", got, want)
	}
	wantFindings := []model.Finding{
		{ID: "f1", Text: "suspect A", Status: model.FindingCleared, Note: "a"},
		{ID: "f2", Text: "suspect B", Status: model.FindingConfirmed, Note: "b"},
	}
	if !reflect.DeepEqual(got.Findings, wantFindings) {
		t.Fatalf("Findings = %+v, want both dispositions surviving", got.Findings)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(41, 43))
	for i := range 50 {
		s, err := fold.Investigation(shuffled(combined, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(s, want) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, s, want)
		}
	}
}

// TestFoldRunbookConcurrentCheckpoints is the runbook convergence trap: two
// replicas each start a distinct run and compact over their own frontier, then
// a union merge joins both checkpoints. The newest-coverage checkpoint is not
// seed-safe (the other branch's ops have lamport <= its CoversLamport), so the
// fold falls back to the full replay where both runs survive.
func TestFoldRunbookConcurrentCheckpoints(t *testing.T) {
	c0 := mk("c0", nil, "root", 100, 1, model.CreateRunbook{Nonce: "n", Title: "RB"})
	c1 := mk("c1", []string{"c0"}, "root", 150, 2, model.AddStep{ID: "s1", Text: "base", Command: "", Position: "a"})
	a1 := mk("a1", []string{"c1"}, "alice", 200, 3,
		model.StartRun{ID: "rA", Task: "taskA"},
		model.SetRunStepStatus{RunID: "rA", StepID: "s1", Status: model.StepDone, Note: "a"},
	)
	b1 := mk("b1", []string{"c1"}, "bob", 210, 3,
		model.StartRun{ID: "rB", Task: "taskB"},
		model.SetRunStepStatus{RunID: "rB", StepID: "s1", Status: model.StepFailed, Note: "b"},
	)
	stateA, err := fold.Runbook([]model.PackCommit{c0, c1, a1})
	if err != nil {
		t.Fatalf("fold A: %v", err)
	}
	stateB, err := fold.Runbook([]model.PackCommit{c0, c1, b1})
	if err != nil {
		t.Fatalf("fold B: %v", err)
	}
	ka := cp("ka", "a1", "alice", 250, 4, stateA, 3, "c0", "c1", "a1")
	kb := cp("kb", "b1", "bob", 260, 4, stateB, 3, "c0", "c1", "b1")
	merge := mk("mmm", []string{"ka", "kb"}, "alice", 300, 5)
	combined := []model.PackCommit{c0, c1, a1, ka, b1, kb, merge}

	kaEmpty := mk("ka", []string{"a1"}, "alice", 250, 4)
	kbEmpty := mk("kb", []string{"b1"}, "bob", 260, 4)
	plain := []model.PackCommit{c0, c1, a1, kaEmpty, b1, kbEmpty, merge}

	want, err := fold.Runbook(plain)
	if err != nil {
		t.Fatalf("fold plain: %v", err)
	}
	got, err := fold.Runbook(combined)
	if err != nil {
		t.Fatalf("fold combined: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("combined = %+v\nplain = %+v", got, want)
	}
	if len(got.Runs) != 2 || got.Runs[0].ID != "rA" || got.Runs[1].ID != "rB" {
		t.Fatalf("runs = %+v, want [rA rB] both surviving", got.Runs)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(23, 29))
	for i := range 50 {
		s, err := fold.Runbook(shuffled(combined, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(s, want) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, s, want)
		}
	}
}

// TestFoldTwoConcurrentCheckpoints is the convergence trap: two replicas diverge
// from a common prefix, each appends an op and compacts over its own frontier,
// then a union merge joins both checkpoints. The newest-coverage checkpoint is
// not seed-safe (the other branch's op has a lamport <= its CoversLamport), so
// the fold falls back to the full history where both checkpoints are no-ops and
// every op replays through its original commit. The result must equal folding
// the same history without checkpoints, on every input ordering.
func TestFoldTwoConcurrentCheckpoints(t *testing.T) {
	c0 := mk("c0", nil, "root", 100, 1, model.CreateNote{Nonce: "n", Title: "T0", Tags: []string{"base"}})
	c1 := mk("c1", []string{"c0"}, "root", 150, 2, model.AddTag{Tag: "common"})
	a1 := mk("a1", []string{"c1"}, "alice", 200, 3, model.SetTitle{Title: "from-a"}, model.AddTag{Tag: "a"})
	b1 := mk("b1", []string{"c1"}, "bob", 210, 3, model.SetTitle{Title: "from-b"}, model.AddTag{Tag: "b"})

	stateA, err := fold.Note([]model.PackCommit{c0, c1, a1})
	if err != nil {
		t.Fatalf("fold A: %v", err)
	}
	stateB, err := fold.Note([]model.PackCommit{c0, c1, b1})
	if err != nil {
		t.Fatalf("fold B: %v", err)
	}
	ka := cp("ka", "a1", "alice", 250, 4, stateA, 3, "c0", "c1", "a1")
	kb := cp("kb", "b1", "bob", 260, 4, stateB, 3, "c0", "c1", "b1")
	merge := mk("mmm", []string{"ka", "kb"}, "alice", 300, 5)
	combined := []model.PackCommit{c0, c1, a1, ka, b1, kb, merge}

	// Equivalent history with no checkpoints: the checkpoint commits become
	// empty no-op commits at the same identities, the merge joins the same tips.
	kaEmpty := mk("ka", []string{"a1"}, "alice", 250, 4)
	kbEmpty := mk("kb", []string{"b1"}, "bob", 260, 4)
	plain := []model.PackCommit{c0, c1, a1, kaEmpty, b1, kbEmpty, merge}

	want, err := fold.Note(plain)
	if err != nil {
		t.Fatalf("fold plain: %v", err)
	}
	got, err := fold.Note(combined)
	if err != nil {
		t.Fatalf("fold combined: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("combined = %+v\nplain = %+v", got, want)
	}
	// b1 sorts after a1 in linearization (same lamport, later time), so b1's
	// title wins — confirm the covered-only-by-a checkpoint did not drop it.
	if got.Title != "from-b" {
		t.Fatalf("Title = %q, want from-b", got.Title)
	}
	if !slices.Equal(got.Tags, []string{"a", "b", "base", "common"}) {
		t.Fatalf("Tags = %v, want [a b base common]", got.Tags)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(7, 9))
	for i := range 50 {
		s, err := fold.Note(shuffled(combined, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(s, want) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, s, want)
		}
	}
}

// TestFoldSeedSafeUsesSeed proves the fast path reads State, not the covered
// ops: the checkpoint's State carries a sentinel tag no op in the chain adds.
// A seeded fold surfaces it; a full replay of the covered ops would not.
func TestFoldSeedSafeUsesSeed(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T", Tags: []string{"keep"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.AddTag{Tag: "more"})
	base, err := fold.Note([]model.PackCommit{c0, c1})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	seed := base
	seed.Tags = append(slices.Clone(seed.Tags), "sentinel")
	cK := cp("cK", "c1", "compactor", 250, 3, seed, 2, "c0", "c1")
	c2 := mk("c2", []string{"cK"}, "carol", 300, 4, model.AddTag{Tag: "late"})
	got, err := fold.Note([]model.PackCommit{c0, c1, cK, c2})
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if !slices.Contains(got.Tags, "sentinel") {
		t.Fatalf("Tags = %v, want sentinel present (seed not used)", got.Tags)
	}
	if !slices.Contains(got.Tags, "late") {
		t.Fatalf("Tags = %v, want late present (suffix not replayed)", got.Tags)
	}
}

// TestFoldCompactionIdempotent compacts at the tip, then compacts the result.
// The folded snapshot is preserved across both — only Head advances to the
// newest checkpoint commit.
func TestFoldCompactionIdempotent(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T", Tags: []string{"x"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T2"})
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AddTag{Tag: "y"})
	base, err := fold.Note([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, base, 3, "c0", "c1", "c2")
	once, err := fold.Note([]model.PackCommit{c0, c1, c2, cK})
	if err != nil {
		t.Fatalf("fold once: %v", err)
	}
	want := base
	want.Head = "cK"
	if !reflect.DeepEqual(once, want) {
		t.Fatalf("once = %+v, want %+v", once, want)
	}
	cK2 := cp("cK2", "cK", "compactor", 360, 5, once, 4, "c0", "c1", "c2", "cK")
	twice, err := fold.Note([]model.PackCommit{c0, c1, c2, cK, cK2})
	if err != nil {
		t.Fatalf("fold twice: %v", err)
	}
	want.Head = "cK2"
	if !reflect.DeepEqual(twice, want) {
		t.Fatalf("twice = %+v, want %+v", twice, want)
	}
}

// TestFoldCheckpointDoesNotMaskCorruptChain confirms a checkpoint never hides a
// shallow history: dropping a covered commit that a later commit still parents
// fails loudly with ErrMissingParent rather than being papered over by the seed.
func TestFoldCheckpointDoesNotMaskCorruptChain(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T2"})
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AddTag{Tag: "y"})
	base, err := fold.Note([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, base, 3, "c0", "c1", "c2")
	c3 := mk("c3", []string{"cK"}, "dave", 400, 5, model.AddTag{Tag: "z"})
	// c1 is removed but c2 still parents it: the chain is shallow.
	corrupt := []model.PackCommit{c0, c2, cK, c3}
	if _, err := fold.Note(corrupt); !errors.Is(err, fold.ErrMissingParent) {
		t.Fatalf("fold corrupt err = %v, want ErrMissingParent", err)
	}
}
