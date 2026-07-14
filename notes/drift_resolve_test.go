package notes

import (
	"slices"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// witnessShortCommitAnchor persists a commit anchor whose value is stored
// verbatim — an abbreviated or otherwise un-canonicalized sha — together with a
// matching witness, so the drift path actually resolves it. The ops go straight
// through the store, bypassing resolveCommits, to reproduce a note written
// before short shas were canonicalized at add time.
func witnessShortCommitAnchor(t *testing.T, c *Client, id model.EntityID, value string, verifiedCommit model.SHA) {
	t.Helper()
	anchor := model.Anchor{Kind: model.AnchorCommit, Value: value}
	ops := []model.Op{
		model.AddAnchor{Anchor: anchor},
		model.VerifyNote{
			Witness:        []model.AnchorWitness{{Anchor: anchor, OID: model.SHA(value)}},
			VerifiedCommit: verifiedCommit,
		},
	}
	if _, err := c.s.Append(t.Context(), refs.For(model.KindNote, id), ops); err != nil {
		t.Fatalf("append short commit anchor %q: %v", value, err)
	}
}

// TestReviewShortCommitAnchor is the litmus for the read-path commit resolver: a
// note witnessed against a short (or foreign) commit sha must be reviewed via
// git resolution, never explode on "invalid sha". A resolvable prefix reachable
// from HEAD reads fresh; an unresolvable one degrades to DRIFTED (best-effort
// skip). Reverting the ResolveCommit call in notes/document.go's driftedOf makes
// both cases fail with "invalid sha".
func TestReviewShortCommitAnchor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value func(full model.SHA) string
		want  Verdict
	}{
		{"resolvable prefix reachable from head is fresh", func(f model.SHA) string { return string(f)[:8] }, ""},
		{"unresolvable prefix degrades to drifted", func(model.SHA) string { return "21aab439" }, VerdictDrifted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := newWBClient(t)
			ctx := t.Context()
			gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
			full := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))

			note, _, err := c.CreateNote(ctx, NoteSpec{Title: "n", Body: "b"})
			if err != nil {
				t.Fatalf("CreateNote: %v", err)
			}
			witnessShortCommitAnchor(t, c, note.ID, tc.value(full), full)

			reloaded, err := c.Note(ctx, note.ID)
			if err != nil {
				t.Fatalf("Note: %v", err)
			}
			verdict, err := c.NoteVerdict(ctx, reloaded, 90*24*time.Hour, false)
			if err != nil {
				t.Fatalf("NoteVerdict: %v", err)
			}
			if verdict != tc.want {
				t.Fatalf("verdict = %q, want %q", verdict, tc.want)
			}

			// Status aggregates the review counts; it must not surface the
			// resolution error either.
			rep, err := c.Status(ctx)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			wantReview := 0
			if tc.want != "" {
				wantReview = 1
			}
			if rep.Notes.NeedsReview != wantReview {
				t.Fatalf("Status Notes.NeedsReview = %d, want %d", rep.Notes.NeedsReview, wantReview)
			}
		})
	}
}

// TestRelevantShortCommitAnchor is the litmus for commitAnchorMerged's read-path
// commit resolver, reached through the public Relevant scan. A note carrying a
// short (or foreign) commit anchor must be scored via git resolution, never
// explode on "invalid sha": a resolvable prefix reachable from HEAD adds the
// merged-commit signal, while an unresolvable one is skipped, leaving only the
// path score. A path anchor surfaces the note in both cases. Reverting the
// ResolveCommit call in notes/relevant.go's commitAnchorMerged makes both cases
// fail with "invalid sha".
func TestRelevantShortCommitAnchor(t *testing.T) {
	const anchoredPath = "a.go"
	for _, tc := range []struct {
		name    string
		value   func(full model.SHA) string
		score   int
		reasons []string
	}{
		{"resolvable prefix adds merged-commit", func(f model.SHA) string { return string(f)[:8] }, scorePath + scoreMergedCommit, []string{reasonPath, reasonMergedCommit}},
		{"unresolvable prefix is skipped", func(model.SHA) string { return "21aab439" }, scorePath, []string{reasonPath}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := newWBClient(t)
			ctx := t.Context()
			gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
			full := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))

			note, _, err := c.CreateNote(ctx, NoteSpec{Title: "n", Body: "b", Anchors: AnchorSpec{Paths: []string{anchoredPath}}})
			if err != nil {
				t.Fatalf("CreateNote: %v", err)
			}
			anchor := model.Anchor{Kind: model.AnchorCommit, Value: tc.value(full)}
			if _, err := c.s.Append(ctx, refs.For(model.KindNote, note.ID), []model.Op{model.AddAnchor{Anchor: anchor}}); err != nil {
				t.Fatalf("append short commit anchor %q: %v", anchor.Value, err)
			}

			scored, err := c.Relevant(ctx, anchoredPath, RelevantFilter{})
			if err != nil {
				t.Fatalf("Relevant: %v", err)
			}
			if len(scored) != 1 {
				t.Fatalf("scored = %d entries, want 1", len(scored))
			}
			got := scored[0]
			if got.Note.ID != note.ID {
				t.Fatalf("scored note = %s, want %s", got.Note.ID, note.ID)
			}
			if got.Score != tc.score {
				t.Fatalf("score = %d, want %d", got.Score, tc.score)
			}
			if !slices.Equal(got.Reasons, tc.reasons) {
				t.Fatalf("reasons = %v, want %v", got.Reasons, tc.reasons)
			}
		})
	}
}
