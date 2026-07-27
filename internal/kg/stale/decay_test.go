package stale

import (
	"math"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func TestHalve(t *testing.T) {
	cases := []struct {
		name string
		x, h float64
		want float64
	}{
		{"no elapsed quantity is no penalty", 0, 200, 1},
		{"one half-life halves", 200, 200, 0.5},
		{"two half-lives quarter", 400, 200, 0.25},
		{"a tenth of a half-life barely moves", 20, 200, 0.9330329915368074},
		{"ten half-lives approach zero", 2000, 200, 0.0009765625},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := halve(tc.x, tc.h); math.Abs(got-tc.want) > 1e-12 {
				t.Errorf("halve(%v, %v) = %v, want %v", tc.x, tc.h, got, tc.want)
			}
		})
	}
}

func TestAnchorCovers(t *testing.T) {
	cases := []struct {
		name   string
		anchor model.Anchor
		path   string
		want   bool
	}{
		{"a path anchor matches exactly", model.Anchor{Kind: model.AnchorPath, Value: "pkg/widget.go"}, "pkg/widget.go", true},
		{"a path anchor rejects a sibling", model.Anchor{Kind: model.AnchorPath, Value: "pkg/widget.go"}, "pkg/rotor.go", false},
		{"a dir anchor matches a child", model.Anchor{Kind: model.AnchorDir, Value: "pkg"}, "pkg/widget.go", true},
		{"a dir anchor matches a grandchild", model.Anchor{Kind: model.AnchorDir, Value: "pkg/"}, "pkg/deep/rotor.go", true},
		{"a dir anchor rejects a prefix twin", model.Anchor{Kind: model.AnchorDir, Value: "pkg"}, "pkgtools/widget.go", false},
		{"a root dir anchor matches everything", model.Anchor{Kind: model.AnchorDir, Value: "."}, "pkg/widget.go", true},
		{"a commit anchor has no file extent", model.Anchor{Kind: model.AnchorCommit, Value: "abc1234"}, "pkg/widget.go", false},
		{"a branch anchor has no file extent", model.Anchor{Kind: model.AnchorBranch, Value: "main"}, "pkg/widget.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anchorCovers(tc.anchor, tc.path); got != tc.want {
				t.Errorf("anchorCovers(%+v, %q) = %t, want %t", tc.anchor, tc.path, got, tc.want)
			}
		})
	}
}

func TestChurnCountsOnlyTouchesAfterAttestation(t *testing.T) {
	anchors := []model.Anchor{{Kind: model.AnchorPath, Value: "pkg/widget.go"}}
	touches := []touch{
		{Path: "pkg/widget.go", TS: 100, Lines: 40},
		{Path: "pkg/widget.go", TS: 300, Lines: 12},
		{Path: "pkg/rotor.go", TS: 400, Lines: 99},
	}
	if got := churn(touches, anchors, 200); got != 12 {
		t.Errorf("churn since 200 = %d, want 12 — the pre-attestation touch and the unanchored path must not count", got)
	}
	if got := churn(touches, anchors, 0); got != 52 {
		t.Errorf("churn since 0 = %d, want 52", got)
	}
}

// churnAnchoredPath rewrites the anchored source with a commit dated an hour
// out, so the touch lands after the record's wall-clock attestation.
func churnAnchoredPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("GIT_COMMITTER_DATE", time.Now().Add(time.Hour).Format(time.RFC3339))
	body := "package pkg\n\ntype Widget struct{}\n"
	for i := range 600 {
		body += "\n// line " + string(rune('a'+i%26)) + "\n"
	}
	writeFile(t, dir, "pkg/widget.go", body)
	gittest.Git(t, dir, "commit", "-q", "-am", "churn the widget")
}

func TestAssessChurnPenalizesAndEnqueuesReverify(t *testing.T) {
	c, dir := openRepo(t)
	lg, _, err := c.CreateLog(t.Context(), notes.LogSpec{
		Title: "widget rollout", Entry: "the canary held",
		Anchors: notes.AnchorSpec{Paths: []string{"pkg/widget.go"}},
	})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	churnAnchoredPath(t, dir)

	got := find(t, assess(t, c, dir, testPolicy(time.Now())), lg.ID)
	if got.Gated {
		t.Fatalf("churn gated the log (%q); S7 is a penalty, not a gate", got.Signal)
	}
	churnPenalty, ok := penaltyFor(got, SignalChurn)
	if !ok {
		t.Fatalf("no CHURN penalty in %+v", got.Penalties)
	}
	if churnPenalty.Weight >= ReverifyBelow {
		t.Errorf("CHURN weight = %v, want below %v after ~1200 churned lines", churnPenalty.Weight, ReverifyBelow)
	}
	if !got.Reverify {
		t.Error("Reverify = false, want the churned log enqueued for a fresh verify")
	}
}

// TestChurnOnAWitnessedRecordGatesFirst pins the signal interaction: churning a
// path a note witnessed drifts that note, and S1 gates before S7 can demote.
// S7 therefore only reaches records with no witness — logs, runbooks,
// investigations, and anchors added since the last verify.
func TestChurnOnAWitnessedRecordGatesFirst(t *testing.T) {
	c, dir := openRepo(t)
	id := verifiedNote(t, c, "Spin returns one", "pkg/widget.go")
	churnAnchoredPath(t, dir)

	got := find(t, assess(t, c, dir, testPolicy(time.Now())), id)
	if !got.Gated || got.Signal != SignalDrift {
		t.Errorf("Gated/Signal = %t/%q, want true/%q", got.Gated, got.Signal, SignalDrift)
	}
	if len(got.Penalties) != 0 {
		t.Errorf("Penalties = %+v, want none — a gated record is not also ranked", got.Penalties)
	}
}

func TestAssessDecaysNotesFasterThanDocsAndNeverLogs(t *testing.T) {
	c, dir := openRepo(t)
	ctx := t.Context()
	note := verifiedNote(t, c, "Spin returns one", "pkg/widget.go")
	doc, _, err := c.CreateDoc(ctx, notes.DocSpec{Title: "spinning a widget", When: "before touching the rotor"})
	if err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	lg, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "rotor rollout", Entry: "the canary held"})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}

	as := assess(t, c, dir, testPolicy(time.Now().Add(90*24*time.Hour)))
	noteWeight := decayWeightOf(t, find(t, as, note))
	docWeight := decayWeightOf(t, find(t, as, doc.ID))
	if math.Abs(docWeight-0.5) > 0.01 {
		t.Errorf("doc decay after one 90d half-life = %v, want ~0.5", docWeight)
	}
	if noteWeight >= docWeight {
		t.Errorf("note decay %v >= doc decay %v, want the 60d note half-life to bite harder", noteWeight, docWeight)
	}
	if _, ok := penaltyFor(find(t, as, lg.ID), SignalDecay); ok {
		t.Error("the log decayed; logs are episodic and never go wrong")
	}
}

func penaltyFor(a Assessment, s Signal) (Penalty, bool) {
	for _, p := range a.Penalties {
		if p.Signal == s {
			return p, true
		}
	}
	return Penalty{}, false
}

func decayWeightOf(t *testing.T, a Assessment) float64 {
	t.Helper()
	p, ok := penaltyFor(a, SignalDecay)
	if !ok {
		t.Fatalf("no DECAY penalty on %s in %+v", a.ID.Short(), a.Penalties)
	}
	return p.Weight
}
