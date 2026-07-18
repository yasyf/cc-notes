package fold_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

func TestFoldInvestigation(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateInvestigation{
			Nonce:   "n",
			Title:   "TestPool deadlock on CI",
			Premise: "Hangs began after 3d55ae2e; suspect the pool rewrite.",
			Tags:    []string{"ci"},
			Anchors: []model.Anchor{{Kind: model.AnchorDir, Value: "internal/pool"}},
		}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.AppendEntry{Text: "bisect reproduces earlier"},
			model.AddFinding{ID: "f1", Text: "commit 3d55ae2e (pool rewrite)"},
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.SetFindingStatus{ID: "f1", Status: model.FindingCleared, Note: "bisect reproduces 4 commits earlier"},
			model.SetRootCause{Text: "Unbuffered results chan leaks a blocked send."},
			model.SetInvestigationStatus{Status: model.InvestigationRootCaused},
		),
		mk("ddd", []string{"ccc"}, "dave", 400, 4,
			model.AddFixCommit{SHA: "fix1"},
			model.SetInvestigationStatus{Status: model.InvestigationFixed},
		),
		mk("eee", []string{"ddd"}, "erin", 500, 5,
			model.AppendEntry{Text: "20 green CI runs since fix1"},
			model.SetInvestigationStatus{Status: model.InvestigationConfirmed},
			model.AddFollowUp{ID: "task1"},
		),
	}
	want := model.Investigation{
		ID:        "aaa",
		Title:     "TestPool deadlock on CI",
		Premise:   "Hangs began after 3d55ae2e; suspect the pool rewrite.",
		Status:    model.InvestigationConfirmed,
		RootCause: "Unbuffered results chan leaks a blocked send.",
		Findings: []model.Finding{
			{ID: "f1", Text: "commit 3d55ae2e (pool rewrite)", Status: model.FindingCleared, Note: "bisect reproduces 4 commits earlier"},
		},
		Entries: []model.LogEntry{
			{Author: "bob", TS: 200, Text: "bisect reproduces earlier"},
			{Author: "erin", TS: 500, Text: "20 green CI runs since fix1"},
		},
		FollowUps:    []model.EntityID{"task1"},
		FixCommits:   []model.SHA{"fix1"},
		Commits:      []model.SHA{},
		Tags:         []string{"ci"},
		Anchors:      []model.Anchor{{Kind: model.AnchorDir, Value: "internal/pool"}},
		SupersededBy: []model.EntityID{},
		Author:       "alice",
		CreatedAt:    100,
		UpdatedAt:    500,
		ClosedAt:     500,
		ClosedBy:     "erin",
		Head:         "eee",
	}
	got, err := fold.Investigation(chain)
	if err != nil {
		t.Fatalf("Investigation() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Investigation() = %+v, want %+v", got, want)
	}
	snap, err := fold.Fold(chain)
	if err != nil {
		t.Fatalf("Fold() error = %v", err)
	}
	dispatched, ok := snap.(model.Investigation)
	if !ok {
		t.Fatalf("Fold() = %T, want model.Investigation", snap)
	}
	if !reflect.DeepEqual(dispatched, want) {
		t.Fatalf("Fold() = %+v, want %+v", dispatched, want)
	}
	if snap.EntityID() != "aaa" {
		t.Fatalf("EntityID() = %q, want %q", snap.EntityID(), "aaa")
	}
}

// TestFoldInvestigationPremiseImmutable confirms the premise comes only from the
// create op: no Set op exists, so the original suspicion survives every later
// edit (including a Title rewrite into a resolution headline).
func TestFoldInvestigationPremiseImmutable(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "suspect pool", Premise: "the pool rewrite hangs"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetTitle{Title: "RESOLVED: unbuffered chan"}, model.SetBody{Body: "buffered the results chan"}),
	}
	got, err := fold.Investigation(chain)
	if err != nil {
		t.Fatalf("Investigation() error = %v", err)
	}
	if got.Premise != "the pool rewrite hangs" {
		t.Fatalf("Premise = %q, want unchanged original suspicion", got.Premise)
	}
	if got.Title != "RESOLVED: unbuffered chan" || got.Body != "buffered the results chan" {
		t.Fatalf("Title/Body = %q/%q, want the evolved headline and resolution", got.Title, got.Body)
	}
}

// TestFoldInvestigationClosedStamp pins ClosedAt/ClosedBy: every terminal status
// stamps them from the carrying commit, and leaving terminal (reopen) zeroes
// both, replayed across a chain that toggles in and out of terminal.
func TestFoldInvestigationClosedStamp(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "T", Premise: "P"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetInvestigationStatus{Status: model.InvestigationConfirmed}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.SetInvestigationStatus{Status: model.InvestigationOpen}),
		mk("ddd", []string{"ccc"}, "dave", 400, 4, model.SetInvestigationStatus{Status: model.InvestigationExonerated}),
		mk("eee", []string{"ddd"}, "erin", 500, 5, model.SetInvestigationStatus{Status: model.InvestigationAbandoned}),
	}
	cases := []struct {
		name       string
		prefix     int
		wantStatus model.InvestigationStatus
		wantClosed int64
		wantBy     model.Actor
	}{
		{"confirmed stamps", 2, model.InvestigationConfirmed, 200, "bob"},
		{"reopen clears", 3, model.InvestigationOpen, 0, ""},
		{"exonerated stamps", 4, model.InvestigationExonerated, 400, "dave"},
		{"abandoned stamps", 5, model.InvestigationAbandoned, 500, "erin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Investigation(chain[:tc.prefix])
			if err != nil {
				t.Fatalf("Investigation() error = %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.ClosedAt != tc.wantClosed {
				t.Fatalf("ClosedAt = %d, want %d", got.ClosedAt, tc.wantClosed)
			}
			if got.ClosedBy != tc.wantBy {
				t.Fatalf("ClosedBy = %q, want %q", got.ClosedBy, tc.wantBy)
			}
		})
	}
}

// TestFoldInvestigationFindings covers the criterion-style finding sub-list:
// idempotent add, text edit, status+note disposition, and remove.
func TestFoldInvestigationFindings(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "T", Premise: "P"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.AddFinding{ID: "f1", Text: "suspect A"},
			model.AddFinding{ID: "f2", Text: "suspect B"},
			model.AddFinding{ID: "f1", Text: "duplicate ignored"}, // idempotent
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.SetFindingText{ID: "f1", Text: "suspect A (refined)"},
			model.SetFindingStatus{ID: "f1", Status: model.FindingCleared, Note: "ruled out"},
			model.SetFindingStatus{ID: "f2", Status: model.FindingConfirmed},
		),
		mk("ddd", []string{"ccc"}, "dave", 400, 4,
			model.AddFinding{ID: "f3", Text: "suspect C"},
			model.RemoveFinding{ID: "f3"},
			model.RemoveFinding{ID: "missing"}, // no-op
		),
	}
	got, err := fold.Investigation(chain)
	if err != nil {
		t.Fatalf("Investigation() error = %v", err)
	}
	want := []model.Finding{
		{ID: "f1", Text: "suspect A (refined)", Status: model.FindingCleared, Note: "ruled out"},
		{ID: "f2", Text: "suspect B", Status: model.FindingConfirmed},
	}
	if !reflect.DeepEqual(got.Findings, want) {
		t.Fatalf("Findings = %+v, want %+v", got.Findings, want)
	}
}

// TestFoldInvestigationEmptySlices confirms a bare investigation folds to
// non-nil empty findings and entries, never nil.
func TestFoldInvestigationEmptySlices(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "T", Premise: "P"}),
	}
	got, err := fold.Investigation(chain)
	if err != nil {
		t.Fatalf("Investigation() error = %v", err)
	}
	if got.Findings == nil || got.Entries == nil {
		t.Fatalf("Findings/Entries = %v/%v, want non-nil empty slices", got.Findings, got.Entries)
	}
	if got.Status != model.InvestigationOpen {
		t.Fatalf("Status = %q, want open", got.Status)
	}
}

// TestFoldInvestigationRejectsForeignOps pins the accept set. The folder is
// total but discriminating: an op that does not apply to an investigation (a
// task op, a note-hygiene op, a comment, a runbook op, or a set_when) is a
// runtime ErrKindMismatch, and investigation ops on a foreign chain likewise.
func TestFoldInvestigationRejectsForeignOps(t *testing.T) {
	invRoot := mk("aaa", nil, "alice", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "T", Premise: "P"})
	noteRoot := mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"})
	cases := []struct {
		name string
		op   model.Op
		root model.PackCommit
		via  func([]model.PackCommit) error
	}{
		{"add_comment on investigation", model.AddComment{Body: "x"}, invRoot, investigationErr},
		{"set_status on investigation", model.SetStatus{Status: model.StatusDone}, invRoot, investigationErr},
		{"set_when on investigation", model.SetWhen{When: "x"}, invRoot, investigationErr},
		{"add_criterion on investigation", model.AddCriterion{ID: "c1", Text: "x"}, invRoot, investigationErr},
		{"verify_note on investigation", model.VerifyNote{VerifiedCommit: "h"}, invRoot, investigationErr},
		{"mark_stale on investigation", model.MarkStale{Reason: "x"}, invRoot, investigationErr},
		{"set_sprint_status on investigation", model.SetSprintStatus{Status: model.SprintActive}, invRoot, investigationErr},
		{"add_step on investigation", model.AddStep{ID: "s1", Text: "x", Position: "a"}, invRoot, investigationErr},
		{"set_root_cause on note", model.SetRootCause{Text: "x"}, noteRoot, noteErr},
		{"add_finding on note", model.AddFinding{ID: "f1", Text: "x"}, noteRoot, noteErr},
		{"set_investigation_status on note", model.SetInvestigationStatus{Status: model.InvestigationOpen}, noteRoot, noteErr},
		{"add_fix_commit on note", model.AddFixCommit{SHA: "s"}, noteRoot, noteErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain := []model.PackCommit{tc.root, mk("bbb", []string{"aaa"}, "bob", 200, 2, tc.op)}
			if err := tc.via(chain); !errors.Is(err, fold.ErrKindMismatch) {
				t.Fatalf("err = %v, want ErrKindMismatch", err)
			}
		})
	}
}

// TestFoldInvestigationKindMismatch confirms a wrong-kind fold and a
// checkpoint over a non-investigation both fail with ErrKindMismatch.
func TestFoldInvestigationKindMismatch(t *testing.T) {
	invRoot := mk("aaa", nil, "alice", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "T", Premise: "P"})
	if _, err := fold.Note([]model.PackCommit{invRoot}); !errors.Is(err, fold.ErrKindMismatch) {
		t.Fatalf("investigation folded as note err = %v, want ErrKindMismatch", err)
	}
	noteRoot := mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"})
	if _, err := fold.Investigation([]model.PackCommit{noteRoot}); !errors.Is(err, fold.ErrKindMismatch) {
		t.Fatalf("note folded as investigation err = %v, want ErrKindMismatch", err)
	}

	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T2"})
	note, err := fold.Note([]model.PackCommit{c0, c1})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c1", "compactor", 250, 3, note, 2, "c0", "c1")
	if _, err := fold.Investigation([]model.PackCommit{c0, c1, cK}); !errors.Is(err, fold.ErrKindMismatch) {
		t.Fatalf("checkpoint over non-investigation err = %v, want ErrKindMismatch", err)
	}
}

// TestFoldInvestigationConvergentAppends is the cross-branch merge convergence
// trap: two branches each append a timeline entry, then a union merge joins
// them. Both replicas fold to the identical entry slice in deterministic
// linearization order, with no reconcile.
func TestFoldInvestigationConvergentAppends(t *testing.T) {
	c0 := mk("c0", nil, "root", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "T", Premise: "P"})
	c1 := mk("c1", []string{"c0"}, "root", 150, 2, model.AppendEntry{Text: "opened"})
	a1 := mk("a1", []string{"c1"}, "alice", 200, 3, model.AppendEntry{Text: "from-alice"})
	b1 := mk("b1", []string{"c1"}, "bob", 210, 3, model.AppendEntry{Text: "from-bob"})
	merge := mk("mmm", []string{"a1", "b1"}, "alice", 300, 4)
	combined := []model.PackCommit{c0, c1, a1, b1, merge}

	want := []model.LogEntry{
		{Author: "root", TS: 150, Text: "opened"},
		{Author: "alice", TS: 200, Text: "from-alice"},
		{Author: "bob", TS: 210, Text: "from-bob"},
	}
	for i, input := range permutations(combined) {
		got, err := fold.Investigation(input)
		if err != nil {
			t.Fatalf("permutation %d: Investigation() error = %v", i, err)
		}
		if !reflect.DeepEqual(got.Entries, want) {
			t.Fatalf("permutation %d: Entries = %+v, want %+v", i, got.Entries, want)
		}
	}
}

// TestFoldInvestigationStatusRace pins the concurrent-resolve invariant: two
// branches each append an entry and set a different status, then merge. LWW
// keeps one status winner (the later-sorting branch), but the loser's
// AppendEntry survives in the timeline — the renderer must assume verdict-bearing
// prose can appear as an ordinary entry.
func TestFoldInvestigationStatusRace(t *testing.T) {
	c0 := mk("c0", nil, "root", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "T", Premise: "P"})
	c1 := mk("c1", []string{"c0"}, "root", 150, 2, model.AppendEntry{Text: "triaged"})
	a1 := mk("a1", []string{"c1"}, "alice", 200, 3,
		model.AppendEntry{Text: "alice: root-caused"},
		model.SetInvestigationStatus{Status: model.InvestigationRootCaused},
	)
	b1 := mk("b1", []string{"c1"}, "bob", 210, 3,
		model.AppendEntry{Text: "bob: fixed"},
		model.SetInvestigationStatus{Status: model.InvestigationFixed},
	)
	merge := mk("mmm", []string{"a1", "b1"}, "alice", 300, 4)
	combined := []model.PackCommit{c0, c1, a1, b1, merge}

	// b1 sorts after a1 (same lamport, later author-time), so b1's status wins.
	wantEntries := []model.LogEntry{
		{Author: "root", TS: 150, Text: "triaged"},
		{Author: "alice", TS: 200, Text: "alice: root-caused"},
		{Author: "bob", TS: 210, Text: "bob: fixed"},
	}
	for i, input := range permutations(combined) {
		got, err := fold.Investigation(input)
		if err != nil {
			t.Fatalf("permutation %d: Investigation() error = %v", i, err)
		}
		if got.Status != model.InvestigationFixed {
			t.Fatalf("permutation %d: Status = %q, want fixed (later branch wins)", i, got.Status)
		}
		if !reflect.DeepEqual(got.Entries, wantEntries) {
			t.Fatalf("permutation %d: Entries = %+v, want %+v (both survive)", i, got.Entries, wantEntries)
		}
	}
}

// TestFoldInvestigationFindingRace disposes two distinct findings on concurrent
// branches, then merges: both dispositions survive because the two ops touch
// different finding ids, so neither is an LWW loser.
func TestFoldInvestigationFindingRace(t *testing.T) {
	c0 := mk("c0", nil, "root", 100, 1, model.CreateInvestigation{Nonce: "n", Title: "T", Premise: "P"})
	c1 := mk("c1", []string{"c0"}, "root", 150, 2,
		model.AddFinding{ID: "f1", Text: "suspect A"},
		model.AddFinding{ID: "f2", Text: "suspect B"},
	)
	a1 := mk("a1", []string{"c1"}, "alice", 200, 3, model.SetFindingStatus{ID: "f1", Status: model.FindingCleared, Note: "alice ruled out A"})
	b1 := mk("b1", []string{"c1"}, "bob", 210, 3, model.SetFindingStatus{ID: "f2", Status: model.FindingConfirmed, Note: "bob confirmed B"})
	merge := mk("mmm", []string{"a1", "b1"}, "alice", 300, 4)
	combined := []model.PackCommit{c0, c1, a1, b1, merge}

	want := []model.Finding{
		{ID: "f1", Text: "suspect A", Status: model.FindingCleared, Note: "alice ruled out A"},
		{ID: "f2", Text: "suspect B", Status: model.FindingConfirmed, Note: "bob confirmed B"},
	}
	for i, input := range permutations(combined) {
		got, err := fold.Investigation(input)
		if err != nil {
			t.Fatalf("permutation %d: Investigation() error = %v", i, err)
		}
		if !reflect.DeepEqual(got.Findings, want) {
			t.Fatalf("permutation %d: Findings = %+v, want %+v (both dispositions survive)", i, got.Findings, want)
		}
	}
}

// investigationErr folds commits as an investigation and returns only the error.
func investigationErr(commits []model.PackCommit) error {
	_, err := fold.Investigation(commits)
	return err
}
