package stale

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

const widgetSource = "package pkg\n\ntype Widget struct{}\n\nfunc (w Widget) Spin() int { return 1 }\n"

// openRepo bootstraps a real git repository holding one committed source file,
// with a cc-notes client open on it.
func openRepo(t *testing.T) (*notes.Client, string) {
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
	return c, dir
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// testStaleAfter is the default note staleness threshold (notes.Client's 90d),
// which is also the decay half-life every default policy runs under.
const testStaleAfter = 90 * 24 * time.Hour

// testPolicy is the package default policy pinned to a fixed clock.
func testPolicy(now time.Time) Policy {
	return Policy{
		Now:             now,
		StaleAfter:      testStaleAfter,
		LeaseTTL:        time.Hour,
		HalfLives:       decayHalfLives(testStaleAfter),
		ChurnHalfLife:   ChurnHalfLife,
		DeadRefHalfLife: DeadRefHalfLife,
		ReverifyBelow:   ReverifyBelow,
		PromoteWeight:   PromoteWeight,
		MaxScanBytes:    MaxScanBytes,
	}
}

func assess(t *testing.T, c *notes.Client, dir string, p Policy) []Assessment {
	t.Helper()
	out, err := New(c, dir, p).Assess(t.Context())
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	return out
}

func find(t *testing.T, as []Assessment, id model.EntityID) Assessment {
	t.Helper()
	a, ok := Index(as)[id]
	if !ok {
		t.Fatalf("no assessment for %s in %d results", id.Short(), len(as))
	}
	return a
}

func TestAssessGates(t *testing.T) {
	cases := []struct {
		name        string
		tune        func(*Policy)
		setup       func(*testing.T, *notes.Client, string) model.EntityID
		wantGated   bool
		wantSignal  Signal
		wantVerdict notes.Verdict
	}{
		{
			name: "deleting an anchored path gates the note",
			setup: func(t *testing.T, c *notes.Client, dir string) model.EntityID {
				id := verifiedNote(t, c, "Spin returns one", "pkg/widget.go")
				gittest.Git(t, dir, "rm", "-q", "pkg/widget.go")
				gittest.Git(t, dir, "commit", "-q", "-m", "drop the widget")
				return id
			},
			wantGated:   true,
			wantSignal:  SignalDrift,
			wantVerdict: notes.VerdictDrifted,
		},
		{
			name: "rewriting an anchored path gates the doc",
			setup: func(t *testing.T, c *notes.Client, dir string) model.EntityID {
				d, _, err := c.CreateDoc(t.Context(), notes.DocSpec{
					Title: "spinning a widget", When: "before touching the rotor",
					Anchors: notes.AnchorSpec{Paths: []string{"pkg/widget.go"}},
				})
				if err != nil {
					t.Fatalf("CreateDoc: %v", err)
				}
				if _, err := c.VerifyDoc(t.Context(), d.ID); err != nil {
					t.Fatalf("VerifyDoc: %v", err)
				}
				writeFile(t, dir, "pkg/widget.go", widgetSource+"\nfunc (w Widget) Stop() {}\n")
				gittest.Git(t, dir, "commit", "-q", "-am", "add Stop")
				return d.ID
			},
			wantGated:   true,
			wantSignal:  SignalDrift,
			wantVerdict: notes.VerdictDrifted,
		},
		{
			name: "a superseded note gates",
			setup: func(t *testing.T, c *notes.Client, _ string) model.EntityID {
				old, _ := supersededPair(t, c)
				return old
			},
			wantGated:  true,
			wantSignal: SignalSuperseded,
		},
		{
			name: "an expired note gates",
			setup: func(t *testing.T, c *notes.Client, _ string) model.EntityID {
				id := verifiedNote(t, c, "Spin returns one", "pkg/widget.go")
				if _, err := c.ExpireNote(t.Context(), id, "the rotor was redesigned"); err != nil {
					t.Fatalf("ExpireNote: %v", err)
				}
				return id
			},
			wantGated:   true,
			wantSignal:  SignalExpired,
			wantVerdict: notes.VerdictExpired,
		},
		{
			name: "an exonerated investigation gates",
			setup: func(t *testing.T, c *notes.Client, _ string) model.EntityID {
				iv := investigation(t, c, "the rotor stalls")
				if _, err := c.Exonerate(t.Context(), iv, "the rotor was never involved", false); err != nil {
					t.Fatalf("Exonerate: %v", err)
				}
				return iv
			},
			wantGated:  true,
			wantSignal: SignalExonerated,
		},
		{
			name: "a done task gates",
			setup: func(t *testing.T, c *notes.Client, _ string) model.EntityID {
				id := task(t, c, "wire the rotor", "main")
				if _, err := c.DoneTask(t.Context(), id, true); err != nil {
					t.Fatalf("DoneTask: %v", err)
				}
				return id
			},
			wantGated:  true,
			wantSignal: SignalClosed,
		},
		{
			name: "a cancelled task gates",
			setup: func(t *testing.T, c *notes.Client, _ string) model.EntityID {
				id := task(t, c, "wire the rotor", "main")
				if _, err := c.CancelTask(t.Context(), id); err != nil {
					t.Fatalf("CancelTask: %v", err)
				}
				return id
			},
			wantGated:  true,
			wantSignal: SignalClosed,
		},
		{
			name: "an expired claim lease gates",
			tune: func(p *Policy) { p.LeaseTTL = 0 },
			setup: func(t *testing.T, c *notes.Client, _ string) model.EntityID {
				id := task(t, c, "wire the rotor", "main")
				if _, err := c.ClaimTask(t.Context(), id); err != nil {
					t.Fatalf("ClaimTask: %v", err)
				}
				return id
			},
			wantGated:  true,
			wantSignal: SignalClosed,
		},
		{
			name: "a task on a merged branch gates",
			setup: func(t *testing.T, c *notes.Client, dir string) model.EntityID {
				gittest.Git(t, dir, "checkout", "-q", "-b", "feature")
				writeFile(t, dir, "pkg/rotor.go", "package pkg\n\nfunc Rotor() {}\n")
				gittest.Git(t, dir, "add", "-A")
				gittest.Git(t, dir, "commit", "-q", "-m", "add the rotor")
				id := task(t, c, "wire the rotor", "feature")
				gittest.Git(t, dir, "checkout", "-q", "main")
				gittest.Git(t, dir, "merge", "-q", "--no-ff", "-m", "merge feature", "feature")
				return id
			},
			wantGated:  true,
			wantSignal: SignalReconciled,
		},
		{
			name: "a task on a branch that no longer exists gates",
			setup: func(t *testing.T, c *notes.Client, _ string) model.EntityID {
				return task(t, c, "wire the rotor", "reconciled-away")
			},
			wantGated:  true,
			wantSignal: SignalReconciled,
		},
		{
			name: "a verified note with an intact anchor passes",
			setup: func(t *testing.T, c *notes.Client, _ string) model.EntityID {
				return verifiedNote(t, c, "Spin returns one", "pkg/widget.go")
			},
		},
		{
			name: "a stale verdict is a penalty, never a gate",
			tune: func(p *Policy) { p.StaleAfter = 0 },
			setup: func(t *testing.T, c *notes.Client, _ string) model.EntityID {
				return verifiedNote(t, c, "Spin returns one", "pkg/widget.go")
			},
			wantVerdict: notes.VerdictStale,
		},
		{
			name: "an open task on an unmerged branch passes",
			setup: func(t *testing.T, c *notes.Client, dir string) model.EntityID {
				gittest.Git(t, dir, "branch", "feature")
				gittest.Git(t, dir, "checkout", "-q", "feature")
				writeFile(t, dir, "pkg/rotor.go", "package pkg\n\nfunc Rotor() {}\n")
				gittest.Git(t, dir, "add", "-A")
				gittest.Git(t, dir, "commit", "-q", "-m", "add the rotor")
				return task(t, c, "wire the rotor", "feature")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := openRepo(t)
			id := tc.setup(t, c, dir)
			policy := testPolicy(time.Now())
			if tc.tune != nil {
				tc.tune(&policy)
			}
			got := find(t, assess(t, c, dir, policy), id)
			if got.Gated != tc.wantGated {
				t.Errorf("Gated = %t, want %t (signal %q, detail %q)", got.Gated, tc.wantGated, got.Signal, got.Detail)
			}
			if tc.wantSignal != "" && got.Signal != tc.wantSignal {
				t.Errorf("Signal = %q, want %q (detail %q)", got.Signal, tc.wantSignal, got.Detail)
			}
			if got.Verdict != tc.wantVerdict {
				t.Errorf("Verdict = %q, want %q", got.Verdict, tc.wantVerdict)
			}
			if tc.wantGated && got.Weight != 0 {
				t.Errorf("Weight = %v, want 0 for a gated record", got.Weight)
			}
			if !tc.wantGated && got.Weight <= 0 {
				t.Errorf("Weight = %v, want a positive weight for an injectable record", got.Weight)
			}
		})
	}
}

// TestGateVerdictPrecedence pins which of the notes-side verdicts gate.
// UNVERIFIED and STALE must not: a record nobody has verified yet, or one
// verified longer ago than the threshold, is demoted by S9, never withheld.
// Notes are born verified, so UNVERIFIED is only reachable as folded state.
func TestGateVerdictPrecedence(t *testing.T) {
	cases := []struct {
		name    string
		verdict notes.Verdict
		want    Signal
	}{
		{"drifted gates", notes.VerdictDrifted, SignalDrift},
		{"dangling gates", notes.VerdictDangling, SignalDrift},
		{"expired gates", notes.VerdictExpired, SignalExpired},
		{"unverified does not gate", notes.VerdictUnverified, ""},
		{"stale does not gate", notes.VerdictStale, ""},
		{"fresh does not gate", "", ""},
	}
	br := branches{trunk: "main", live: map[model.Branch]bool{"main": true}, merged: map[model.Branch]bool{"main": true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := record{Verdict: tc.verdict}
			r.Kind = model.KindNote
			if got, detail := gate(r, br); got != tc.want {
				t.Errorf("gate(%q) = %q (%s), want %q", tc.verdict, got, detail, tc.want)
			}
		})
	}
}

func TestAssessSupersededNamesTheHead(t *testing.T) {
	c, dir := openRepo(t)
	old, fresh := supersededPair(t, c)

	got := find(t, assess(t, c, dir, testPolicy(time.Now())), old)
	if !got.Gated || got.Signal != SignalSuperseded {
		t.Fatalf("Gated/Signal = %t/%q, want true/%q", got.Gated, got.Signal, SignalSuperseded)
	}
	if len(got.Successor) != 1 || got.Successor[0] != fresh {
		t.Errorf("Successor = %v, want [%s]", got.Successor, fresh)
	}
	if head := find(t, assess(t, c, dir, testPolicy(time.Now())), fresh); head.Gated {
		t.Errorf("the superseding note is gated (%q), want it injectable", head.Signal)
	}
}

func TestAssessConfirmedInvestigationIsPromotedAndDecayExempt(t *testing.T) {
	c, dir := openRepo(t)
	ctx := t.Context()
	id := investigation(t, c, "the rotor stalls")
	if _, err := c.RootCause(ctx, id, "Spin returns one on a stalled rotor"); err != nil {
		t.Fatalf("RootCause: %v", err)
	}
	if _, err := c.Fix(ctx, id, "return zero when stalled", nil); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if _, err := c.Confirm(ctx, id, "the rotor spins under load", false); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	policy := testPolicy(time.Now().Add(10 * 365 * 24 * time.Hour))
	policy.HalfLives[model.KindInvestigation] = time.Hour
	got := find(t, assess(t, c, dir, policy), id)
	if got.Gated {
		t.Fatalf("a confirmed root cause is gated (%q), want it promoted", got.Signal)
	}
	if !got.Promoted || got.Weight != PromoteWeight {
		t.Errorf("Promoted/Weight = %t/%v, want true/%v — a decade of decay must not touch it", got.Promoted, got.Weight, PromoteWeight)
	}
}

func TestReverifyQueueOrdersWeakestFirst(t *testing.T) {
	as := []Assessment{
		{ID: "aaa", Weight: 0.4, Reverify: true},
		{ID: "bbb", Weight: 0.9},
		{ID: "ccc", Weight: 0.1, Reverify: true},
		{ID: "ddd", Weight: 0, Gated: true},
	}
	got := ReverifyQueue(as)
	want := []model.EntityID{"ccc", "aaa"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ReverifyQueue = %v, want %v", got, want)
	}
}

func TestAssessEmptyCorpus(t *testing.T) {
	c, dir := openRepo(t)
	if got := assess(t, c, dir, testPolicy(time.Now())); len(got) != 0 {
		t.Errorf("Assess over a cc-notes-free repo = %d assessments, want 0", len(got))
	}
}

func verifiedNote(t *testing.T, c *notes.Client, title, path string) model.EntityID {
	t.Helper()
	n, _, err := c.CreateNote(t.Context(), notes.NoteSpec{Title: title, Anchors: notes.AnchorSpec{Paths: []string{path}}})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if _, err := c.VerifyNote(t.Context(), n.ID); err != nil {
		t.Fatalf("VerifyNote: %v", err)
	}
	return n.ID
}

func supersededPair(t *testing.T, c *notes.Client) (old, fresh model.EntityID) {
	t.Helper()
	first, _, err := c.CreateNote(t.Context(), notes.NoteSpec{Title: "Spin returns one"})
	if err != nil {
		t.Fatalf("CreateNote old: %v", err)
	}
	second, _, err := c.CreateNote(t.Context(), notes.NoteSpec{Title: "Spin returns the tick count"})
	if err != nil {
		t.Fatalf("CreateNote fresh: %v", err)
	}
	if _, err := c.SupersedeNote(t.Context(), first.ID, second.ID); err != nil {
		t.Fatalf("SupersedeNote: %v", err)
	}
	return first.ID, second.ID
}

func investigation(t *testing.T, c *notes.Client, title string) model.EntityID {
	t.Helper()
	iv, _, err := c.CreateInvestigation(t.Context(), notes.InvestigationSpec{Title: title, Premise: "the rotor stalls under load"})
	if err != nil {
		t.Fatalf("CreateInvestigation: %v", err)
	}
	return iv.ID
}

func task(t *testing.T, c *notes.Client, title string, branch model.Branch) model.EntityID {
	t.Helper()
	created, err := c.CreateTask(t.Context(), notes.TaskSpec{Title: title, Branch: branch})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return created.Task.ID
}
