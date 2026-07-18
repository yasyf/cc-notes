package notes_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// mustInvestigation creates an investigation from spec, failing on error, and
// returns the folded snapshot.
func mustInvestigation(t *testing.T, c *notes.Client, spec notes.InvestigationSpec) model.Investigation {
	t.Helper()
	inv, _, err := c.CreateInvestigation(t.Context(), spec)
	if err != nil {
		t.Fatalf("CreateInvestigation(%q): %v", spec.Title, err)
	}
	return inv
}

// investigationCommits counts the commits in the investigation's ref chain, so a
// transition verb can be proved to land its ops in exactly one pack commit.
func investigationCommits(t *testing.T, dir string, id model.EntityID) int {
	t.Helper()
	out := gittest.Git(t, dir, "rev-list", "--count", "refs/cc-notes/investigations/"+string(id))
	n, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("parse rev-list count %q: %v", out, err)
	}
	return n
}

// findingByID returns the finding with id, failing if absent.
func findingByID(t *testing.T, findings []model.Finding, id string) model.Finding {
	t.Helper()
	for _, f := range findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("finding %s absent from %+v", id, findings)
	return model.Finding{}
}

// driveTo creates a fresh investigation and drives it to status via the legal
// transition verbs, returning its id.
func driveTo(t *testing.T, c *notes.Client, status model.InvestigationStatus) model.EntityID {
	t.Helper()
	ctx := t.Context()
	// A unique premise keeps repeated driveTo calls on one client from deduping
	// onto a single live investigation.
	inv := mustInvestigation(t, c, notes.InvestigationSpec{Title: "drive", Premise: model.NewNonce()})
	step := func(_ model.Investigation, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("driveTo %s: %v", status, err)
		}
	}
	switch status {
	case model.InvestigationOpen:
	case model.InvestigationRootCaused:
		step(c.RootCause(ctx, inv.ID, "cause"))
	case model.InvestigationFixed:
		step(c.RootCause(ctx, inv.ID, "cause"))
		step(c.Fix(ctx, inv.ID, "fix", nil))
	case model.InvestigationConfirmed:
		step(c.RootCause(ctx, inv.ID, "cause"))
		step(c.Fix(ctx, inv.ID, "fix", nil))
		step(c.Confirm(ctx, inv.ID, "proof"))
	case model.InvestigationExonerated:
		step(c.Exonerate(ctx, inv.ID, "premise falsified"))
	case model.InvestigationAbandoned:
		step(c.Abandon(ctx, inv.ID, "walked away"))
	default:
		t.Fatalf("driveTo: unhandled status %q", status)
	}
	return inv.ID
}

func TestCreateInvestigation(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "internal/pool/pool.go", "package pool\n")

	inv, reused, err := c.CreateInvestigation(ctx, notes.InvestigationSpec{
		Title:    "TestPool deadlock",
		Premise:  "suspect the pool rewrite",
		Tags:     []string{"ci", "deadlock"},
		Anchors:  notes.AnchorSpec{Paths: []string{"internal/pool/pool.go"}, Dirs: []string{"internal/pool"}},
		Findings: []string{"pool rewrite", "unbuffered chan"},
	})
	if err != nil {
		t.Fatalf("CreateInvestigation: %v", err)
	}
	if reused {
		t.Error("fresh create reused an existing investigation")
	}
	if !isHexID(inv.ID) {
		t.Errorf("id %q is not a folded entity id", inv.ID)
	}
	if inv.Premise != "suspect the pool rewrite" {
		t.Errorf("premise = %q", inv.Premise)
	}
	if inv.Status != model.InvestigationOpen {
		t.Errorf("status = %q, want open", inv.Status)
	}
	if !slices.Equal(inv.Tags, []string{"ci", "deadlock"}) {
		t.Errorf("tags = %v", inv.Tags)
	}
	if len(inv.Anchors) != 2 {
		t.Errorf("anchors = %v, want 2", inv.Anchors)
	}
	if len(inv.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(inv.Findings))
	}
	if inv.Findings[0].Text != "pool rewrite" || inv.Findings[1].Text != "unbuffered chan" {
		t.Errorf("finding order = %+v", inv.Findings)
	}
	for _, f := range inv.Findings {
		if f.Status != model.FindingOpen {
			t.Errorf("finding %s status = %q, want open", f.ID, f.Status)
		}
	}
}

func TestCreateInvestigationDedupe(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	spec := notes.InvestigationSpec{Title: "dup", Premise: "same suspicion", Tags: []string{"a"}}

	first, reused, err := c.CreateInvestigation(ctx, spec)
	if err != nil || reused {
		t.Fatalf("first create = reused %v err %v", reused, err)
	}
	second, reused, err := c.CreateInvestigation(ctx, spec)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if !reused || second.ID != first.ID {
		t.Errorf("identical create = id %s reused %v, want reuse of %s", second.ID, reused, first.ID)
	}

	// A different premise is a different investigation, never a duplicate.
	third, reused, err := c.CreateInvestigation(ctx, notes.InvestigationSpec{Title: "dup", Premise: "different suspicion", Tags: []string{"a"}})
	if err != nil {
		t.Fatalf("third create: %v", err)
	}
	if reused || third.ID == first.ID {
		t.Errorf("differing premise reused id %s", third.ID)
	}
}

func TestCreateInvestigationRejectsEmpties(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	if _, _, err := c.CreateInvestigation(ctx, notes.InvestigationSpec{Title: "", Premise: "p"}); !errors.Is(err, notes.ErrEmptyTitle) {
		t.Errorf("empty title = %v, want ErrEmptyTitle", err)
	}
	if _, _, err := c.CreateInvestigation(ctx, notes.InvestigationSpec{Title: "t", Premise: ""}); !errors.Is(err, notes.ErrEmptyPremise) {
		t.Errorf("empty premise = %v, want ErrEmptyPremise", err)
	}
	for _, tc := range []struct {
		name     string
		findings []string
	}{
		{"first finding", []string{"", "valid"}},
		{"later finding", []string{"valid", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := c.CreateInvestigation(ctx, notes.InvestigationSpec{Title: "t", Premise: "p", Findings: tc.findings}); !errors.Is(err, notes.ErrEmptyFinding) {
				t.Errorf("empty finding = %v, want ErrEmptyFinding", err)
			}
		})
	}
}

func TestFindingTextRequired(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	inv := mustInvestigation(t, c, notes.InvestigationSpec{Title: "t", Premise: "p", Findings: []string{"suspect"}})
	if _, err := c.AddFinding(ctx, inv.ID, ""); !errors.Is(err, notes.ErrEmptyFinding) {
		t.Errorf("AddFinding(empty) = %v, want ErrEmptyFinding", err)
	}
	if _, err := c.EditFinding(ctx, inv.ID, inv.Findings[0].ID[:6], ""); !errors.Is(err, notes.ErrEmptyFinding) {
		t.Errorf("EditFinding(empty) = %v, want ErrEmptyFinding", err)
	}
}

func TestCreateInvestigationWithFindingsIsOnePack(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	inv, reused, err := c.CreateInvestigation(ctx, notes.InvestigationSpec{
		Title:    "one pack",
		Premise:  "p",
		Findings: []string{"suspect one", "suspect two"},
	})
	if err != nil || reused {
		t.Fatalf("create = reused %v err %v", reused, err)
	}
	if len(inv.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(inv.Findings))
	}
	// The create op and both AddFinding ops land in a single ref commit.
	if n := investigationCommits(t, dir, inv.ID); n != 1 {
		t.Errorf("open-with-findings = %d commits, want 1", n)
	}
}

func TestCreateInvestigationWithFindingsNeverDedupes(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	spec := notes.InvestigationSpec{Title: "dup", Premise: "same suspicion", Findings: []string{"suspect"}}
	first, reused, err := c.CreateInvestigation(ctx, spec)
	if err != nil || reused {
		t.Fatalf("first create = reused %v err %v", reused, err)
	}
	// A finding-carrying pack is dedupe-ineligible (dedupeCovered excludes
	// AddFinding), so an identical open roots a second record, never a reuse.
	second, reused, err := c.CreateInvestigation(ctx, spec)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if reused || second.ID == first.ID {
		t.Errorf("finding-carrying create deduped: id %s reused %v", second.ID, reused)
	}
}

func TestFixRequiresEvidence(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	id := driveTo(t, c, model.InvestigationRootCaused)
	if _, err := c.Fix(ctx, id, "", nil); !errors.Is(err, notes.ErrMissingReason) {
		t.Fatalf("Fix with no commit and no text = %v, want ErrMissingReason", err)
	}
	// The refused fix left the investigation at root_caused.
	inv, err := c.Investigation(ctx, id)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if inv.Status != model.InvestigationRootCaused {
		t.Errorf("status after refused Fix = %q, want unchanged root_caused", inv.Status)
	}
}

func TestInvestigationLegalArc(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	sha := commitFile(t, dir, "internal/pool/pool.go", "package pool\n")
	inv := mustInvestigation(t, c, notes.InvestigationSpec{Title: "deadlock", Premise: "pool rewrite"})

	// open → root_caused sets the cause, appends the entry, and flips the status
	// in exactly one pack commit.
	before := investigationCommits(t, dir, inv.ID)
	rc, err := c.RootCause(ctx, inv.ID, "unbuffered chan leaks a blocked send")
	if err != nil {
		t.Fatalf("RootCause: %v", err)
	}
	if rc.Status != model.InvestigationRootCaused || rc.RootCause != "unbuffered chan leaks a blocked send" {
		t.Errorf("root-cause = status %q cause %q", rc.Status, rc.RootCause)
	}
	if len(rc.Entries) != 1 || rc.Entries[0].Text != "unbuffered chan leaks a blocked send" {
		t.Errorf("entries = %+v, want the cause entry", rc.Entries)
	}
	if got := investigationCommits(t, dir, inv.ID); got != before+1 {
		t.Errorf("RootCause added %d commits, want 1 (atomic)", got-before)
	}

	// root_caused → fixed links the fix commit and appends its entry atomically.
	before = investigationCommits(t, dir, inv.ID)
	fx, err := c.Fix(ctx, inv.ID, "buffered the results chan", []string{string(sha)})
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if fx.Status != model.InvestigationFixed || !slices.Contains(fx.FixCommits, sha) {
		t.Errorf("fix = status %q fixcommits %v", fx.Status, fx.FixCommits)
	}
	if len(fx.Entries) != 2 {
		t.Errorf("entries after fix = %d, want 2", len(fx.Entries))
	}
	if got := investigationCommits(t, dir, inv.ID); got != before+1 {
		t.Errorf("Fix added %d commits, want 1 (atomic)", got-before)
	}

	// fixed → confirmed stamps ClosedAt/ClosedBy from the carrying commit, atomically.
	before = investigationCommits(t, dir, inv.ID)
	cf, err := c.Confirm(ctx, inv.ID, "20 green CI runs, no recurrence")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if cf.Status != model.InvestigationConfirmed {
		t.Errorf("status = %q, want confirmed", cf.Status)
	}
	if cf.ClosedAt == 0 || cf.ClosedBy == "" {
		t.Errorf("confirmed did not stamp closed: at %d by %q", cf.ClosedAt, cf.ClosedBy)
	}
	if len(cf.Entries) != 3 {
		t.Errorf("entries after confirm = %d, want 3", len(cf.Entries))
	}
	if got := investigationCommits(t, dir, inv.ID); got != before+1 {
		t.Errorf("Confirm added %d commits, want 1 (atomic)", got-before)
	}

	// confirmed → open (reopen) zeroes the closed stamps, atomically.
	before = investigationCommits(t, dir, inv.ID)
	ro, err := c.Reopen(ctx, inv.ID, "regressed on CI")
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if ro.Status != model.InvestigationOpen || ro.ClosedAt != 0 || ro.ClosedBy != "" {
		t.Errorf("reopen = status %q closedAt %d closedBy %q", ro.Status, ro.ClosedAt, ro.ClosedBy)
	}
	if len(ro.Entries) != 4 {
		t.Errorf("entries after reopen = %d, want 4", len(ro.Entries))
	}
	if got := investigationCommits(t, dir, inv.ID); got != before+1 {
		t.Errorf("Reopen added %d commits, want 1 (atomic)", got-before)
	}
}

func TestInvestigationExonerateAndAbandon(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()

	// open → exonerated (premise falsified) stamps closed in one pack commit.
	ex := driveTo(t, c, model.InvestigationOpen)
	before := investigationCommits(t, dir, ex)
	got, err := c.Exonerate(ctx, ex, "bisect reproduces before the suspect commit")
	if err != nil {
		t.Fatalf("Exonerate: %v", err)
	}
	if got.Status != model.InvestigationExonerated || got.ClosedAt == 0 {
		t.Errorf("exonerate = status %q closedAt %d", got.Status, got.ClosedAt)
	}
	if len(got.Entries) != 1 {
		t.Errorf("exonerate entries = %d, want 1", len(got.Entries))
	}
	if n := investigationCommits(t, dir, ex); n != before+1 {
		t.Errorf("Exonerate added %d commits, want 1 (atomic)", n-before)
	}

	// fixed → abandoned with text appends the entry and closes in one pack commit.
	abText := driveTo(t, c, model.InvestigationFixed)
	priorText, err := c.Investigation(ctx, abText)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	before = investigationCommits(t, dir, abText)
	got, err = c.Abandon(ctx, abText, "walked away — reprioritized")
	if err != nil {
		t.Fatalf("Abandon(text): %v", err)
	}
	if got.Status != model.InvestigationAbandoned || got.ClosedAt == 0 {
		t.Errorf("abandon = status %q closedAt %d", got.Status, got.ClosedAt)
	}
	if len(got.Entries) != len(priorText.Entries)+1 {
		t.Errorf("abandon with text = %d entries, want %d", len(got.Entries), len(priorText.Entries)+1)
	}
	if n := investigationCommits(t, dir, abText); n != before+1 {
		t.Errorf("Abandon added %d commits, want 1 (atomic)", n-before)
	}

	// fixed → abandoned with no text appends no entry but still closes.
	ab := driveTo(t, c, model.InvestigationFixed)
	prior, err := c.Investigation(ctx, ab)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, err = c.Abandon(ctx, ab, "")
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if got.Status != model.InvestigationAbandoned || got.ClosedAt == 0 {
		t.Errorf("abandon = status %q closedAt %d", got.Status, got.ClosedAt)
	}
	if len(got.Entries) != len(prior.Entries) {
		t.Errorf("abandon with empty text added an entry: %d → %d", len(prior.Entries), len(got.Entries))
	}
}

func TestInvestigationIllegalTransitions(t *testing.T) {
	rootCause := func(ctx context.Context, c *notes.Client, id model.EntityID) error {
		_, e := c.RootCause(ctx, id, "x")
		return e
	}
	fix := func(ctx context.Context, c *notes.Client, id model.EntityID) error {
		_, e := c.Fix(ctx, id, "cause", nil)
		return e
	}
	confirm := func(ctx context.Context, c *notes.Client, id model.EntityID) error {
		_, e := c.Confirm(ctx, id, "proof")
		return e
	}
	exonerate := func(ctx context.Context, c *notes.Client, id model.EntityID) error {
		_, e := c.Exonerate(ctx, id, "reason")
		return e
	}
	abandon := func(ctx context.Context, c *notes.Client, id model.EntityID) error {
		_, e := c.Abandon(ctx, id, "reason")
		return e
	}
	reopen := func(ctx context.Context, c *notes.Client, id model.EntityID) error {
		_, e := c.Reopen(ctx, id, "reason")
		return e
	}

	cases := []struct {
		name string
		from model.InvestigationStatus
		call func(context.Context, *notes.Client, model.EntityID) error
	}{
		{"root-cause a root_caused", model.InvestigationRootCaused, rootCause},
		{"root-cause a confirmed", model.InvestigationConfirmed, rootCause},
		{"root-cause an exonerated", model.InvestigationExonerated, rootCause},
		{"root-cause an abandoned", model.InvestigationAbandoned, rootCause},
		{"fix an open", model.InvestigationOpen, fix},
		{"fix a fixed", model.InvestigationFixed, fix},
		{"fix a confirmed", model.InvestigationConfirmed, fix},
		{"fix an exonerated", model.InvestigationExonerated, fix},
		{"fix an abandoned", model.InvestigationAbandoned, fix},
		{"confirm an open", model.InvestigationOpen, confirm},
		{"confirm a root_caused", model.InvestigationRootCaused, confirm},
		{"confirm a confirmed", model.InvestigationConfirmed, confirm},
		{"confirm an exonerated", model.InvestigationExonerated, confirm},
		{"confirm an abandoned", model.InvestigationAbandoned, confirm},
		{"exonerate a fixed", model.InvestigationFixed, exonerate},
		{"exonerate a confirmed", model.InvestigationConfirmed, exonerate},
		{"exonerate an exonerated", model.InvestigationExonerated, exonerate},
		{"exonerate an abandoned", model.InvestigationAbandoned, exonerate},
		{"abandon a confirmed", model.InvestigationConfirmed, abandon},
		{"abandon an exonerated", model.InvestigationExonerated, abandon},
		{"abandon an abandoned", model.InvestigationAbandoned, abandon},
		{"reopen an open", model.InvestigationOpen, reopen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := newClient(t)
			ctx := t.Context()
			id := driveTo(t, c, tc.from)
			before, err := c.Investigation(ctx, id)
			if err != nil {
				t.Fatalf("load before transition: %v", err)
			}
			beforeCommits := investigationCommits(t, dir, id)
			err = tc.call(ctx, c, id)
			if !errors.Is(err, notes.ErrIllegalTransition) {
				t.Fatalf("%s = %v, want ErrIllegalTransition", tc.name, err)
			}
			if !strings.Contains(err.Error(), string(tc.from)) {
				t.Errorf("error %q does not name the current status %q", err, tc.from)
			}
			after, err := c.Investigation(ctx, id)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Errorf("snapshot after refused transition = %+v, want unchanged %+v", after, before)
			}
			if got := investigationCommits(t, dir, id); got != beforeCommits {
				t.Errorf("commits after refused transition = %d, want unchanged %d", got, beforeCommits)
			}
		})
	}
}

func TestInvestigationTransitionRetract(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	// root_caused → open (retract) is legal and reopens without a closed stamp.
	id := driveTo(t, c, model.InvestigationRootCaused)
	got, err := c.Reopen(ctx, id, "cause was wrong")
	if err != nil {
		t.Fatalf("Reopen from root_caused: %v", err)
	}
	if got.Status != model.InvestigationOpen {
		t.Errorf("status = %q, want open", got.Status)
	}

	// fixed → root_caused (fix did not hold) is legal.
	id = driveTo(t, c, model.InvestigationFixed)
	got, err = c.RootCause(ctx, id, "deeper cause")
	if err != nil {
		t.Fatalf("RootCause from fixed: %v", err)
	}
	if got.Status != model.InvestigationRootCaused {
		t.Errorf("status = %q, want root_caused", got.Status)
	}
}

func TestReopenRequiresReason(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	id := driveTo(t, c, model.InvestigationConfirmed)
	if _, err := c.Reopen(ctx, id, ""); !errors.Is(err, notes.ErrMissingReason) {
		t.Fatalf("Reopen with empty reason = %v, want ErrMissingReason", err)
	}
	// The refused reopen left the terminal status intact.
	inv, err := c.Investigation(ctx, id)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if inv.Status != model.InvestigationConfirmed {
		t.Errorf("status = %q, want unchanged confirmed", inv.Status)
	}
}

func TestRootCauseRequiresText(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	inv := mustInvestigation(t, c, notes.InvestigationSpec{Title: "t", Premise: "p"})
	if _, err := c.RootCause(ctx, inv.ID, ""); !errors.Is(err, notes.ErrMissingReason) {
		t.Fatalf("RootCause with empty text = %v, want ErrMissingReason", err)
	}
}

func TestFixResolvesCommitsStrictly(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	id := driveTo(t, c, model.InvestigationRootCaused)
	unknown := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if _, err := c.Fix(ctx, id, "", []string{unknown}); !errors.Is(err, notes.ErrNotFound) {
		t.Fatalf("Fix with unknown commit = %v, want ErrNotFound", err)
	}
	// A failed commit resolution must not have transitioned the investigation.
	inv, err := c.Investigation(ctx, id)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if inv.Status != model.InvestigationRootCaused {
		t.Errorf("status after failed Fix = %q, want unchanged root_caused", inv.Status)
	}
	if len(inv.FixCommits) != 0 {
		t.Errorf("fix commits after failed Fix = %v, want none", inv.FixCommits)
	}
}

func TestResolveFinding(t *testing.T) {
	c, _ := newClient(t)
	inv := mustInvestigation(t, c, notes.InvestigationSpec{Title: "t", Premise: "p", Findings: []string{"suspect one", "suspect two"}})
	f0 := inv.Findings[0]

	if got, err := notes.ResolveFinding(inv, string(f0.ID)); err != nil || got.ID != f0.ID {
		t.Errorf("ResolveFinding(full) = %s/%v, want %s", got.ID, err, f0.ID)
	}
	if got, err := notes.ResolveFinding(inv, f0.ID[:6]); err != nil || got.ID != f0.ID {
		t.Errorf("ResolveFinding(prefix) = %s/%v, want %s", got.ID, err, f0.ID)
	}
	if _, err := notes.ResolveFinding(inv, "zzzzzz"); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("ResolveFinding(unknown) = %v, want ErrNotFound", err)
	}
	// An empty prefix is refused outright, never silently resolved.
	if _, err := notes.ResolveFinding(inv, ""); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("ResolveFinding(\"\") = %v, want ErrNotFound", err)
	}
	// Even against a sole finding, an empty prefix must not resolve it.
	sole := mustInvestigation(t, c, notes.InvestigationSpec{Title: "t2", Premise: "p2", Findings: []string{"only suspect"}})
	if _, err := notes.ResolveFinding(sole, ""); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("ResolveFinding(sole, \"\") = %v, want ErrNotFound", err)
	}
}

// TestResolveFindingShortIDs proves the ambiguous-candidate rendering is
// length-safe: findings imported with sub-7-char ids must not panic on slicing.
func TestResolveFindingShortIDs(t *testing.T) {
	inv := model.Investigation{Findings: []model.Finding{
		{ID: "ab", Text: "one"},
		{ID: "ac", Text: "two"},
	}}
	_, err := notes.ResolveFinding(inv, "a")
	if !errors.Is(err, notes.ErrAmbiguous) {
		t.Fatalf("ResolveFinding(2-char ids, %q) = %v, want ErrAmbiguous", "a", err)
	}
	if !strings.Contains(err.Error(), "ab") || !strings.Contains(err.Error(), "ac") {
		t.Errorf("ambiguous error %q must list both short ids", err)
	}
}

func TestFindingsCRUD(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	inv := mustInvestigation(t, c, notes.InvestigationSpec{Title: "t", Premise: "p", Findings: []string{"suspect one"}})
	fid := inv.Findings[0].ID

	// Add a second finding.
	got, err := c.AddFinding(ctx, inv.ID, "suspect two")
	if err != nil {
		t.Fatalf("AddFinding: %v", err)
	}
	if len(got.Findings) != 2 {
		t.Fatalf("findings after add = %d, want 2", len(got.Findings))
	}

	// Edit the first finding's text by prefix.
	got, err = c.EditFinding(ctx, inv.ID, fid[:6], "suspect one (revised)")
	if err != nil {
		t.Fatalf("EditFinding: %v", err)
	}
	if findingByID(t, got.Findings, fid).Text != "suspect one (revised)" {
		t.Errorf("edited finding text = %q", findingByID(t, got.Findings, fid).Text)
	}

	// Dispose it cleared with evidence.
	got, err = c.SetFindingCleared(ctx, inv.ID, fid[:6], "bisect reproduces earlier")
	if err != nil {
		t.Fatalf("SetFindingCleared: %v", err)
	}
	f := findingByID(t, got.Findings, fid)
	if f.Status != model.FindingCleared || f.Note != "bisect reproduces earlier" {
		t.Errorf("cleared finding = %+v", f)
	}

	// Re-dispose confirmed.
	got, err = c.SetFindingConfirmed(ctx, inv.ID, fid[:6], "pinned to this line")
	if err != nil {
		t.Fatalf("SetFindingConfirmed: %v", err)
	}
	if f := findingByID(t, got.Findings, fid); f.Status != model.FindingConfirmed || f.Note != "pinned to this line" {
		t.Errorf("confirmed finding = %+v", f)
	}

	// An empty why is refused for both dispositions.
	if _, err := c.SetFindingCleared(ctx, inv.ID, fid[:6], ""); !errors.Is(err, notes.ErrMissingReason) {
		t.Errorf("SetFindingCleared(empty why) = %v, want ErrMissingReason", err)
	}
	if _, err := c.SetFindingConfirmed(ctx, inv.ID, fid[:6], ""); !errors.Is(err, notes.ErrMissingReason) {
		t.Errorf("SetFindingConfirmed(empty why) = %v, want ErrMissingReason", err)
	}

	// Remove the first finding.
	got, err = c.RemoveFinding(ctx, inv.ID, fid[:6])
	if err != nil {
		t.Fatalf("RemoveFinding: %v", err)
	}
	if len(got.Findings) != 1 || got.Findings[0].ID == fid {
		t.Errorf("findings after remove = %+v, want the first gone", got.Findings)
	}

	// An unknown prefix is refused.
	if _, err := c.RemoveFinding(ctx, inv.ID, "zzzzzz"); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("RemoveFinding(unknown) = %v, want ErrNotFound", err)
	}
	_ = dir
}

func TestAppendInvestigation(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	inv := mustInvestigation(t, c, notes.InvestigationSpec{Title: "t", Premise: "p"})

	got, err := c.AppendInvestigation(ctx, inv.ID, notes.InvestigationAppend{Text: "bisect reproduces at 3d55ae2e~4", Model: "claude-opus-4-8"})
	if err != nil {
		t.Fatalf("AppendInvestigation: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Text != "bisect reproduces at 3d55ae2e~4" || got.Entries[0].Model != "claude-opus-4-8" {
		t.Errorf("entries = %+v", got.Entries)
	}

	// An empty append with no attachment records nothing.
	if _, err := c.AppendInvestigation(ctx, inv.ID, notes.InvestigationAppend{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Errorf("empty append = %v, want ErrEmptyEdit", err)
	}

	// Append an attachment: text may be empty when an attachment rides along.
	src := filepath.Join(t.TempDir(), "stacks.txt")
	if err := os.WriteFile(src, []byte("goroutine dump"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	att, _, err := c.AttachFile(ctx, src)
	if err != nil {
		t.Fatalf("AttachFile: %v", err)
	}
	got, err = c.AppendInvestigation(ctx, inv.ID, notes.InvestigationAppend{Attachments: []model.Attachment{att}})
	if err != nil {
		t.Fatalf("AppendInvestigation(attach): %v", err)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Name != "stacks.txt" {
		t.Errorf("attachments = %+v", got.Attachments)
	}

	// A colliding attachment without replace is refused.
	var aee *notes.AttachmentExistsError
	if _, err := c.AppendInvestigation(ctx, inv.ID, notes.InvestigationAppend{Attachments: []model.Attachment{att}}); !errors.As(err, &aee) {
		t.Errorf("colliding attach = %v, want *AttachmentExistsError", err)
	}
	_ = dir
}

func TestEditInvestigation(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "internal/pool/pool.go", "package pool\n")
	inv := mustInvestigation(t, c, notes.InvestigationSpec{Title: "old title", Premise: "immutable premise", Tags: []string{"a"}})

	newTitle := "resolved: pool deadlock"
	newBody := "buffering the results chan fixed it"
	got, err := c.EditInvestigation(ctx, inv.ID, notes.InvestigationEdit{
		Title:      &newTitle,
		Body:       &newBody,
		AddTags:    []string{"ci"},
		RemoveTags: []string{"a"},
		AddAnchors: notes.AnchorSpec{Paths: []string{"internal/pool/pool.go"}},
	})
	if err != nil {
		t.Fatalf("EditInvestigation: %v", err)
	}
	if got.Title != newTitle || got.Body != newBody {
		t.Errorf("title/body = %q/%q", got.Title, got.Body)
	}
	if got.Premise != "immutable premise" {
		t.Errorf("premise changed to %q — it must be immutable", got.Premise)
	}
	if !slices.Equal(got.Tags, []string{"ci"}) {
		t.Errorf("tags = %v, want [ci]", got.Tags)
	}
	if len(got.Anchors) != 1 {
		t.Errorf("anchors = %v, want 1", got.Anchors)
	}

	if _, err := c.EditInvestigation(ctx, inv.ID, notes.InvestigationEdit{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Errorf("empty edit = %v, want ErrEmptyEdit", err)
	}
}

func TestRemoveInvestigation(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	inv := mustInvestigation(t, c, notes.InvestigationSpec{Title: "gone", Premise: "p"})

	got, err := c.RemoveInvestigation(ctx, inv.ID)
	if err != nil {
		t.Fatalf("RemoveInvestigation: %v", err)
	}
	if !got.Deleted {
		t.Error("removed investigation Deleted = false")
	}
	// A tombstoned investigation drops out of the live listing.
	live, err := c.Investigations(ctx, notes.InvestigationFilter{})
	if err != nil {
		t.Fatalf("Investigations: %v", err)
	}
	if slices.ContainsFunc(live, func(i model.Investigation) bool { return i.ID == inv.ID }) {
		t.Error("tombstoned investigation still in live listing")
	}
}

func TestInvestigationsFilter(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	openID := driveTo(t, c, model.InvestigationOpen)
	rootID := driveTo(t, c, model.InvestigationRootCaused)
	confirmedID := driveTo(t, c, model.InvestigationConfirmed)

	// No status filter returns every live investigation.
	all, err := c.Investigations(ctx, notes.InvestigationFilter{})
	if err != nil {
		t.Fatalf("Investigations: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unfiltered = %d, want 3", len(all))
	}

	// The non-terminal filter drops the confirmed one.
	nonTerminal, err := c.Investigations(ctx, notes.InvestigationFilter{
		Statuses: []model.InvestigationStatus{model.InvestigationOpen, model.InvestigationRootCaused, model.InvestigationFixed},
	})
	if err != nil {
		t.Fatalf("Investigations(status): %v", err)
	}
	ids := make(map[model.EntityID]bool)
	for _, i := range nonTerminal {
		ids[i.ID] = true
	}
	if !ids[openID] || !ids[rootID] || ids[confirmedID] {
		t.Errorf("non-terminal filter = %v, want open+root_caused only", scopedIDs(nonTerminal))
	}
}

func TestSearchInvestigations(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	target := mustInvestigation(t, c, notes.InvestigationSpec{
		Title:    "deadlock arc",
		Premise:  "the mutex ordering inverted under load",
		Findings: []string{"channel starvation"},
	})
	if _, err := c.RootCause(ctx, target.ID, "lock acquired out of order"); err != nil {
		t.Fatalf("RootCause: %v", err)
	}
	mustInvestigation(t, c, notes.InvestigationSpec{Title: "unrelated", Premise: "a totally different thing"})

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"title", "deadlock"},
		{"premise body", "mutex ordering"},
		{"root cause body", "out of order"},
		{"finding text body", "channel starvation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := c.SearchInvestigations(ctx, tc.query, notes.SearchFilter{Limit: -1})
			if err != nil {
				t.Fatalf("SearchInvestigations(%q): %v", tc.query, err)
			}
			if len(hits) != 1 || hits[0].ID != target.ID {
				t.Errorf("hits = %v, want [%s]", scopedIDs(hits), target.ID)
			}
		})
	}
}

func TestInvestigationFollowUps(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	inv := mustInvestigation(t, c, notes.InvestigationSpec{Title: "t", Premise: "p"})
	task := mustTask(t, c, notes.TaskSpec{Title: "remediation", Branch: "main"})

	got, err := c.AddFollowUp(ctx, inv.ID, task.ID)
	if err != nil {
		t.Fatalf("AddFollowUp: %v", err)
	}
	if !slices.Contains(got.FollowUps, task.ID) {
		t.Errorf("follow-ups = %v, want to contain %s", got.FollowUps, task.ID)
	}

	got, err = c.RemoveFollowUp(ctx, inv.ID, task.ID)
	if err != nil {
		t.Fatalf("RemoveFollowUp: %v", err)
	}
	if slices.Contains(got.FollowUps, task.ID) {
		t.Errorf("follow-ups after remove = %v, want empty", got.FollowUps)
	}
}

// scopedIDs returns the ordered entity ids of an investigation slice.
func scopedIDs(invs []model.Investigation) []model.EntityID {
	out := make([]model.EntityID, len(invs))
	for i, inv := range invs {
		out[i] = inv.ID
	}
	return out
}
