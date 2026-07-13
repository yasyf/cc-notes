package notes_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// commitFile writes path with content in dir and commits it, returning the new
// HEAD sha.
func commitFile(t *testing.T, dir, path, content string) model.SHA {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "commit "+path)
	return model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
}

func TestCreateNoteBornVerified(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	head := commitFile(t, dir, "a.go", "v1\n")

	note, reused, err := c.CreateNote(ctx, notes.NoteSpec{Title: "born", Body: "b", Anchors: notes.AnchorSpec{Paths: []string{"a.go"}}})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if reused {
		t.Fatal("first CreateNote reported reused")
	}
	if note.VerifiedAt == 0 {
		t.Fatal("note born with VerifiedAt == 0; a fresh add must be verified")
	}
	if note.VerifiedCommit != head {
		t.Errorf("VerifiedCommit = %q, want %q", note.VerifiedCommit, head)
	}
	if len(note.Witness) != 1 || note.Witness[0].Anchor.Value != "a.go" {
		t.Fatalf("witness = %+v, want one witness on a.go", note.Witness)
	}
	verdict, err := c.NoteVerdict(ctx, note, 90*24*time.Hour, false)
	if err != nil {
		t.Fatalf("NoteVerdict: %v", err)
	}
	if verdict != "" {
		t.Errorf("fresh born-verified note verdict = %q, want empty", verdict)
	}
}

func TestCreateNoteReVerifyOnDedupe(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "a.go", "v1\n")

	first, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "dup", Body: "b", Anchors: notes.AnchorSpec{Paths: []string{"a.go"}}})
	if err != nil {
		t.Fatalf("first CreateNote: %v", err)
	}
	head2 := commitFile(t, dir, "a.go", "v2\n")

	again, reused, err := c.CreateNote(ctx, notes.NoteSpec{Title: "dup", Body: "b", Anchors: notes.AnchorSpec{Paths: []string{"a.go"}}})
	if err != nil {
		t.Fatalf("second CreateNote: %v", err)
	}
	if !reused || again.ID != first.ID {
		t.Fatalf("second CreateNote = reused %v id %s, want reused onto %s", reused, again.ID, first.ID)
	}
	// The dedupe survivor is re-verified against the new HEAD, refreshing its
	// witness and verified_commit rather than keeping first's stale witness.
	if again.VerifiedCommit != head2 {
		t.Errorf("re-verified VerifiedCommit = %q, want %q", again.VerifiedCommit, head2)
	}
	blob := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD:a.go"))
	if len(again.Witness) != 1 || again.Witness[0].OID != blob {
		t.Fatalf("re-verified witness = %+v, want oid %s", again.Witness, blob)
	}
}

func TestEditNoteMaskAndEmpty(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "a.go", "v1\n")
	note, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "old", Body: "b", Tags: []string{"keep", "drop"}})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	if _, err := c.EditNote(ctx, note.ID, notes.NoteEdit{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Fatalf("empty edit = %v, want ErrEmptyEdit", err)
	}

	title, body := "new", "nb"
	edited, err := c.EditNote(ctx, note.ID, notes.NoteEdit{
		Title:      &title,
		Body:       &body,
		AddTags:    []string{"added"},
		RemoveTags: []string{"drop"},
		AddAnchors: notes.AnchorSpec{Paths: []string{"a.go"}},
	})
	if err != nil {
		t.Fatalf("EditNote: %v", err)
	}
	if edited.Title != "new" || edited.Body != "nb" {
		t.Errorf("edited title/body = %q/%q, want new/nb", edited.Title, edited.Body)
	}
	if !hasTag(edited.Tags, "keep") || !hasTag(edited.Tags, "added") || hasTag(edited.Tags, "drop") {
		t.Errorf("tags = %v, want keep+added, no drop", edited.Tags)
	}
	if !hasAnchorValue(edited.Anchors, model.AnchorPath, "a.go") {
		t.Errorf("anchors = %+v, want a.go path anchor", edited.Anchors)
	}
}

func TestEditNoteRemoveAnchorsVerbatim(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	head := commitFile(t, dir, "a.go", "v1\n")
	note, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "anchored", Body: "b"})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	// AddAnchors resolves the revision to a full sha before storing.
	withCommit, err := c.EditNote(ctx, note.ID, notes.NoteEdit{AddAnchors: notes.AnchorSpec{Commits: []string{"HEAD"}}})
	if err != nil {
		t.Fatalf("EditNote add commit anchor: %v", err)
	}
	if !hasAnchorValue(withCommit.Anchors, model.AnchorCommit, string(head)) {
		t.Fatalf("anchors = %+v, want resolved commit %s", withCommit.Anchors, head)
	}

	// RemoveAnchors is matched verbatim: "HEAD" does not match the stored full
	// sha, so the anchor survives.
	kept, err := c.EditNote(ctx, note.ID, notes.NoteEdit{RemoveAnchors: notes.AnchorSpec{Commits: []string{"HEAD"}}})
	if err != nil {
		t.Fatalf("EditNote verbatim remove: %v", err)
	}
	if !hasAnchorValue(kept.Anchors, model.AnchorCommit, string(head)) {
		t.Fatal("verbatim RemoveAnchors resolved \"HEAD\" and removed the full-sha anchor")
	}

	// Removing the full sha verbatim drops it.
	dropped, err := c.EditNote(ctx, note.ID, notes.NoteEdit{RemoveAnchors: notes.AnchorSpec{Commits: []string{string(head)}}})
	if err != nil {
		t.Fatalf("EditNote remove full sha: %v", err)
	}
	if hasAnchorValue(dropped.Anchors, model.AnchorCommit, string(head)) {
		t.Errorf("anchors = %+v, want the commit anchor removed", dropped.Anchors)
	}
}

func TestNoteVerdictDriftAndWorktree(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "a.go", "v1\n")
	note, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "drift", Body: "b", Anchors: notes.AnchorSpec{Paths: []string{"a.go"}}})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	big := 90 * 24 * time.Hour

	// A commit that moves HEAD's blob off the witness drifts the note.
	commitFile(t, dir, "a.go", "v2\n")
	v, err := c.NoteVerdict(ctx, note, big, false)
	if err != nil {
		t.Fatalf("NoteVerdict committed drift: %v", err)
	}
	if v != notes.VerdictDrifted {
		t.Errorf("verdict = %q, want DRIFTED after the anchored file changed", v)
	}

	// Re-verify against the new HEAD, then dirty the worktree only.
	reverified, err := c.VerifyNote(ctx, note.ID)
	if err != nil {
		t.Fatalf("VerifyNote: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("v3-uncommitted\n"), 0o600); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}
	committed, err := c.NoteVerdict(ctx, reverified, big, false)
	if err != nil {
		t.Fatalf("NoteVerdict worktree=false: %v", err)
	}
	if committed != "" {
		t.Errorf("verdict (committed view) = %q, want fresh; the edit is uncommitted", committed)
	}
	work, err := c.NoteVerdict(ctx, reverified, big, true)
	if err != nil {
		t.Fatalf("NoteVerdict worktree=true: %v", err)
	}
	if work != notes.VerdictDrifted {
		t.Errorf("verdict (worktree view) = %q, want DRIFTED against the dirty file", work)
	}
}

func TestReviewNotesVerdicts(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "a.go", "v1\n")

	stale, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "stale-note", Body: "b"})
	if err != nil {
		t.Fatalf("CreateNote stale: %v", err)
	}
	expired, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "expired-note", Body: "b"})
	if err != nil {
		t.Fatalf("CreateNote expired: %v", err)
	}
	if _, err := c.ExpireNote(ctx, expired.ID, "outdated"); err != nil {
		t.Fatalf("ExpireNote: %v", err)
	}

	// A negative threshold forces every verified note past staleness.
	got, err := c.ReviewNotes(ctx, -time.Hour)
	if err != nil {
		t.Fatalf("ReviewNotes: %v", err)
	}
	verdicts := reviewVerdicts(got)
	if verdicts[expired.ID] != notes.VerdictExpired {
		t.Errorf("expired note verdict = %q, want EXPIRED", verdicts[expired.ID])
	}
	if verdicts[stale.ID] != notes.VerdictStale {
		t.Errorf("stale note verdict = %q, want STALE", verdicts[stale.ID])
	}
}

func TestReviewNotesDangling(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "a.go", "v1\n")
	old, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "old", Body: "b"})
	if err != nil {
		t.Fatalf("CreateNote old: %v", err)
	}
	repl, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "replacement", Body: "b"})
	if err != nil {
		t.Fatalf("CreateNote repl: %v", err)
	}
	if _, err := c.SupersedeNote(ctx, old.ID, repl.ID); err != nil {
		t.Fatalf("SupersedeNote: %v", err)
	}
	// A live supersede target is not dangling.
	for _, r := range mustReview(t, c) {
		if r.Note.ID == old.ID {
			t.Fatalf("superseded note flagged with a live target: %q", r.Verdict)
		}
	}
	// Tombstoning the target dangles the edge.
	if _, err := c.RemoveNote(ctx, repl.ID); err != nil {
		t.Fatalf("RemoveNote: %v", err)
	}
	if reviewVerdicts(mustReview(t, c))[old.ID] != notes.VerdictDangling {
		t.Errorf("verdict = %q, want DANGLING after the target was tombstoned", reviewVerdicts(mustReview(t, c))[old.ID])
	}
}

func TestSupersedeValidatesTarget(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "a.go", "v1\n")
	old, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "old", Body: "b"})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if _, err := c.SupersedeNote(ctx, old.ID, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); err == nil {
		t.Fatal("SupersedeNote onto a nonexistent target succeeded, want an error")
	}
	reloaded, err := c.Note(ctx, old.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.SupersededBy) != 0 {
		t.Errorf("SupersededBy = %v after a failed supersede, want none (no mutation)", reloaded.SupersededBy)
	}
}

func TestSearchAndFilterNotes(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "a.go", "v1\n")
	titleHit, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "widget design", Body: "x", Tags: []string{"design"}})
	if err != nil {
		t.Fatalf("CreateNote titleHit: %v", err)
	}
	bodyHit, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "unrelated", Body: "the widget broke"})
	if err != nil {
		t.Fatalf("CreateNote bodyHit: %v", err)
	}
	if _, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "nothing here", Body: "nope"}); err != nil {
		t.Fatalf("CreateNote miss: %v", err)
	}

	ranked, err := c.SearchNotes(ctx, "widget", notes.SearchFilter{Limit: 20})
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	if len(ranked) != 2 || ranked[0].ID != titleHit.ID || ranked[1].ID != bodyHit.ID {
		t.Fatalf("ranked = %v, want title-tier before body-tier", noteIDs(ranked))
	}

	labelled, err := c.SearchNotes(ctx, "widget", notes.SearchFilter{Labels: []string{"design"}, Limit: 20})
	if err != nil {
		t.Fatalf("SearchNotes labelled: %v", err)
	}
	if len(labelled) != 1 || labelled[0].ID != titleHit.ID {
		t.Fatalf("labelled search = %v, want only the design note", noteIDs(labelled))
	}

	byTag, err := c.Notes(ctx, notes.DocumentFilter{Labels: []string{"design"}})
	if err != nil {
		t.Fatalf("Notes filter: %v", err)
	}
	if len(byTag) != 1 || byTag[0].ID != titleHit.ID {
		t.Fatalf("Notes label filter = %v, want only the design note", noteIDs(byTag))
	}
}

func TestExpireNoteRoundTrip(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "a.go", "v1\n")
	note, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "e", Body: "b"})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	expired, err := c.ExpireNote(ctx, note.ID, "outdated")
	if err != nil {
		t.Fatalf("ExpireNote: %v", err)
	}
	if expired.StaleAt == 0 || expired.StaleReason != "outdated" {
		t.Errorf("expired = StaleAt %d reason %q, want set", expired.StaleAt, expired.StaleReason)
	}
	cleared, err := c.UnexpireNote(ctx, note.ID)
	if err != nil {
		t.Fatalf("UnexpireNote: %v", err)
	}
	if cleared.StaleAt != 0 {
		t.Errorf("StaleAt = %d after unexpire, want 0", cleared.StaleAt)
	}
}

func mustReview(t *testing.T, c *notes.Client) []notes.NoteReview {
	t.Helper()
	got, err := c.ReviewNotes(t.Context(), 90*24*time.Hour)
	if err != nil {
		t.Fatalf("ReviewNotes: %v", err)
	}
	return got
}

func reviewVerdicts(rs []notes.NoteReview) map[model.EntityID]notes.Verdict {
	m := make(map[model.EntityID]notes.Verdict, len(rs))
	for _, r := range rs {
		m[r.Note.ID] = r.Verdict
	}
	return m
}

func noteIDs(ns []model.Note) []model.EntityID {
	out := make([]model.EntityID, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func hasAnchorValue(anchors []model.Anchor, kind model.AnchorKind, value string) bool {
	for _, a := range anchors {
		if a.Kind == kind && a.Value == value {
			return true
		}
	}
	return false
}
