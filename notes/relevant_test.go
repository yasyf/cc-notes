package notes_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// relevantMe matches the identity gittest.InitRepo configures, so AuthorIdent
// resolves it as the local author; relevantOther is a teammate.
const (
	relevantMe    = "test@example.com"
	relevantOther = "other@example.com"
)

// commitFileAs writes path with content in dir, commits it authored by email,
// and returns the resulting HEAD sha. commitFile (document_test.go) covers the
// local-identity case; this helper forces a teammate author for cross-author
// scoring.
func commitFileAs(t *testing.T, dir, email, relpath, content string) model.SHA {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(relpath))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", relpath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", relpath, err)
	}
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "-c", "user.name=Other", "-c", "user.email="+email, "commit", "-q", "-m", "commit "+relpath)
	return model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
}

// mustRelevant runs Relevant, failing on error.
func mustRelevant(t *testing.T, c *notes.Client, dir, target string, filter notes.RelevantFilter) []notes.RelevantEntry {
	t.Helper()
	scored, err := c.Relevant(t.Context(), target, filter)
	if err != nil {
		t.Fatalf("Relevant(%q): %v", target, err)
	}
	_ = dir
	return scored
}

// entryID returns a scored entry's entity id, regardless of kind.
func entryID(e notes.RelevantEntry) model.EntityID {
	switch e.Kind {
	case model.KindDoc:
		return e.Doc.ID
	case model.KindLog:
		return e.Log.ID
	default:
		return e.Note.ID
	}
}

// entryUpdatedAt returns a scored entry's last-update time, regardless of kind.
func entryUpdatedAt(e notes.RelevantEntry) int64 {
	switch e.Kind {
	case model.KindDoc:
		return e.Doc.UpdatedAt
	case model.KindLog:
		return e.Log.UpdatedAt
	default:
		return e.Note.UpdatedAt
	}
}

// scoredIDs returns the ordered entity ids of a scored slice.
func scoredIDs(scored []notes.RelevantEntry) []model.EntityID {
	out := make([]model.EntityID, len(scored))
	for i, e := range scored {
		out[i] = entryID(e)
	}
	return out
}

// findEntry returns the scored entry for id, failing if absent.
func findEntry(t *testing.T, scored []notes.RelevantEntry, id model.EntityID) notes.RelevantEntry {
	t.Helper()
	for _, e := range scored {
		if entryID(e) == id {
			return e
		}
	}
	t.Fatalf("entity %s not in results %v", id, scoredIDs(scored))
	return notes.RelevantEntry{}
}

// makeNote creates a note with the given anchors, returning its folded id.
func makeNote(t *testing.T, c *notes.Client, title string, anchors notes.AnchorSpec) model.EntityID {
	t.Helper()
	n, _, err := c.CreateNote(t.Context(), notes.NoteSpec{Title: title, Anchors: anchors})
	if err != nil {
		t.Fatalf("CreateNote %q: %v", title, err)
	}
	return n.ID
}

func TestRelevantScoringAndReasons(t *testing.T) {
	c, dir := newClient(t)
	commitFile(t, dir, "internal/auth/login.go", "v1\n")

	pathNote := makeNote(t, c, "exact path", notes.AnchorSpec{Paths: []string{"internal/auth/login.go"}})
	dirNote := makeNote(t, c, "dir", notes.AnchorSpec{Dirs: []string{"internal/auth"}})
	sibNote := makeNote(t, c, "sibling", notes.AnchorSpec{Paths: []string{"internal/auth/logout.go"}})
	branchNote := makeNote(t, c, "branch only", notes.AnchorSpec{Branches: []string{"main"}})

	scored := mustRelevant(t, c, dir, "internal/auth/login.go", notes.RelevantFilter{})

	wantIDs := []model.EntityID{pathNote, dirNote, branchNote, sibNote}
	if got := scoredIDs(scored); !slices.Equal(got, wantIDs) {
		t.Fatalf("order = %v, want %v", got, wantIDs)
	}

	cases := []struct {
		name   string
		id     model.EntityID
		score  int
		reason string
	}{
		{"path", pathNote, 100, "path"},
		{"dir", dirNote, 60, "dir"},
		{"branch", branchNote, 40, "branch"},
		{"sibling", sibNote, 15, "sibling"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := findEntry(t, scored, tc.id)
			if e.Score != tc.score {
				t.Errorf("score = %d, want %d", e.Score, tc.score)
			}
			if !slices.Equal(e.Reasons, []string{tc.reason}) {
				t.Errorf("reasons = %v, want [%s]", e.Reasons, tc.reason)
			}
		})
	}
}

func TestRelevantDirDeepestNoStacking(t *testing.T) {
	c, dir := newClient(t)
	commitFile(t, dir, "internal/auth/oauth/token.go", "v1\n")

	// A dir anchor two levels up still matches a nested path.
	ancestor := makeNote(t, c, "ancestor dir", notes.AnchorSpec{Dirs: []string{"internal"}})
	// Overlapping dir anchors do not stack: the deepest wins, scored once.
	stacked := makeNote(t, c, "stacked dirs", notes.AnchorSpec{Dirs: []string{"internal", "internal/auth"}})

	scored := mustRelevant(t, c, dir, "internal/auth/oauth/token.go", notes.RelevantFilter{})
	for _, id := range []model.EntityID{ancestor, stacked} {
		e := findEntry(t, scored, id)
		if e.Score != 60 || !slices.Equal(e.Reasons, []string{"dir"}) {
			t.Errorf("%s = score %d reasons %v, want 60 [dir]", id, e.Score, e.Reasons)
		}
	}
}

func TestRelevantSiblingScopedToDirectory(t *testing.T) {
	c, dir := newClient(t)
	commitFile(t, dir, "pkg/a.go", "v1\n")

	sib := makeNote(t, c, "sibling note", notes.AnchorSpec{Paths: []string{"pkg/b.go"}})
	unrelated := makeNote(t, c, "unrelated", notes.AnchorSpec{Paths: []string{"other/c.go"}})

	scored := mustRelevant(t, c, dir, "pkg/a.go", notes.RelevantFilter{})
	e := findEntry(t, scored, sib)
	if e.Score != 15 || !slices.Equal(e.Reasons, []string{"sibling"}) {
		t.Fatalf("sibling = score %d reasons %v, want 15 [sibling]", e.Score, e.Reasons)
	}
	if slices.Contains(scoredIDs(scored), unrelated) {
		t.Fatalf("unrelated note surfaced: %v", scoredIDs(scored))
	}
}

func TestRelevantMergedCommitAndBranch(t *testing.T) {
	c, dir := newClient(t)
	first := commitFile(t, dir, "core/x.go", "v1\n")

	// A feature branch whose tip later merges into main.
	gittest.Git(t, dir, "branch", "feature")
	gittest.Git(t, dir, "checkout", "-q", "feature")
	commitFile(t, dir, "core/y.go", "v1\n")
	gittest.Git(t, dir, "checkout", "-q", "main")
	gittest.Git(t, dir, "merge", "-q", "--no-ff", "-m", "merge feature", "feature")

	mergedCommit := makeNote(t, c, "merged commit", notes.AnchorSpec{Commits: []string{string(first)}})
	mergedBranch := makeNote(t, c, "merged branch", notes.AnchorSpec{Branches: []string{"feature"}})

	scored := mustRelevant(t, c, dir, "unrelated/path.go", notes.RelevantFilter{})
	mc := findEntry(t, scored, mergedCommit)
	if mc.Score != 25 || !slices.Equal(mc.Reasons, []string{"merged-commit"}) {
		t.Fatalf("merged commit = score %d reasons %v, want 25 [merged-commit]", mc.Score, mc.Reasons)
	}
	mb := findEntry(t, scored, mergedBranch)
	if mb.Score != 20 || !slices.Equal(mb.Reasons, []string{"merged-branch"}) {
		t.Fatalf("merged branch = score %d reasons %v, want 20 [merged-branch]", mb.Score, mb.Reasons)
	}
}

func TestRelevantCrossAuthorBoost(t *testing.T) {
	c, dir := newClient(t)
	// Base commit on main; HEAD diverges with a teammate-authored file and a
	// self-authored file, both siblings of the target.
	commitFile(t, dir, "base.go", "v1\n")
	gittest.Git(t, dir, "branch", "feat-base") // mark the merge-base ref
	commitFileAs(t, dir, relevantOther, "pkg/teammate.go", "theirs\n")
	commitFile(t, dir, "pkg/mine.go", "mine\n")

	otherSib := makeNote(t, c, "sibling on teammate file", notes.AnchorSpec{Paths: []string{"pkg/teammate.go"}})
	selfSib := makeNote(t, c, "sibling on self file", notes.AnchorSpec{Paths: []string{"pkg/mine.go"}})

	scored := mustRelevant(t, c, dir, "pkg/target.go", notes.RelevantFilter{Base: "feat-base"})
	other := findEntry(t, scored, otherSib)
	if other.Score != 15+30 {
		t.Fatalf("teammate sibling score = %d, want %d", other.Score, 15+30)
	}
	if !slices.Equal(other.Reasons, []string{"sibling", "cross-author"}) {
		t.Fatalf("teammate sibling reasons = %v, want [sibling cross-author]", other.Reasons)
	}
	self := findEntry(t, scored, selfSib)
	if self.Score != 15 || !slices.Equal(self.Reasons, []string{"sibling"}) {
		t.Fatalf("self sibling = score %d reasons %v, want 15 [sibling] (no cross-author boost)", self.Score, self.Reasons)
	}
	if entryID(scored[0]) != otherSib {
		t.Fatalf("teammate-file sibling should outrank self-file sibling, order = %v", scoredIDs(scored))
	}
}

func TestRelevantCrossAuthorNeverMatchesAlone(t *testing.T) {
	c, dir := newClient(t)
	commitFile(t, dir, "base.go", "v1\n")
	gittest.Git(t, dir, "branch", "feat-base")
	commitFileAs(t, dir, relevantOther, "far/teammate.go", "theirs\n")

	// A note anchored only to the teammate file, far from the target, has no
	// path/dir/sibling match — cross-author cannot surface it alone.
	makeNote(t, c, "far teammate note", notes.AnchorSpec{Paths: []string{"far/teammate.go"}})

	scored := mustRelevant(t, c, dir, "pkg/target.go", notes.RelevantFilter{Base: "feat-base"})
	if len(scored) != 0 {
		t.Fatalf("cross-author surfaced a note alone: %v", scoredIDs(scored))
	}
}

func TestRelevantAttachedDropsLooseSignals(t *testing.T) {
	c, dir := newClient(t)
	commitFile(t, dir, "internal/auth/login.go", "v1\n")

	pathNote := makeNote(t, c, "exact path", notes.AnchorSpec{Paths: []string{"internal/auth/login.go"}})
	dirNote := makeNote(t, c, "dir", notes.AnchorSpec{Dirs: []string{"internal/auth"}})
	makeNote(t, c, "sibling", notes.AnchorSpec{Paths: []string{"internal/auth/logout.go"}})
	makeNote(t, c, "branch only", notes.AnchorSpec{Branches: []string{"main"}})

	scored := mustRelevant(t, c, dir, "internal/auth/login.go", notes.RelevantFilter{Attached: true})
	if got := scoredIDs(scored); !slices.Equal(got, []model.EntityID{pathNote, dirNote}) {
		t.Fatalf("--attached ids = %v, want [%s %s]", got, pathNote, dirNote)
	}
}

func TestRelevantVerdictFoldsIn(t *testing.T) {
	c, dir := newClient(t)
	commitFile(t, dir, "svc/handler.go", "v1\n")

	noteID := makeNote(t, c, "path note", notes.AnchorSpec{Paths: []string{"svc/handler.go"}})
	logSnap, _, err := c.CreateLog(t.Context(), notes.LogSpec{Title: "path log", Entry: "e", Anchors: notes.AnchorSpec{Paths: []string{"svc/handler.go"}}})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}

	// Dirty the working tree without committing: the committed blob is unchanged.
	if err := os.WriteFile(filepath.Join(dir, "svc", "handler.go"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatalf("write dirty: %v", err)
	}

	plain := mustRelevant(t, c, dir, "svc/handler.go", notes.RelevantFilter{})
	if v := findEntry(t, plain, noteID).Verdict; v != "" {
		t.Fatalf("plain note verdict = %q, want fresh (committed blob unchanged)", v)
	}
	if v := findEntry(t, plain, logSnap.ID).Verdict; v != "" {
		t.Fatalf("log verdict = %q, want empty; a log never drifts", v)
	}

	wt := mustRelevant(t, c, dir, "svc/handler.go", notes.RelevantFilter{Worktree: true})
	if v := findEntry(t, wt, noteID).Verdict; v != notes.VerdictDrifted {
		t.Fatalf("worktree note verdict = %q, want %q", v, notes.VerdictDrifted)
	}
	if v := findEntry(t, wt, logSnap.ID).Verdict; v != "" {
		t.Fatalf("worktree log verdict = %q, want empty; a log never drifts", v)
	}
}

func TestRelevantSortTotalOrder(t *testing.T) {
	c, dir := newClient(t)
	commitFile(t, dir, "x/a.go", "v1\n")

	// Three entries tie at scorePath (two notes + a doc), forcing the UpdatedAt
	// then id tiebreaks; the dir and branch notes descend below them.
	makeNote(t, c, "path one", notes.AnchorSpec{Paths: []string{"x/a.go"}})
	makeNote(t, c, "path two", notes.AnchorSpec{Paths: []string{"x/a.go"}})
	if _, _, err := c.CreateDoc(t.Context(), notes.DocSpec{Title: "path doc", When: "editing x", Anchors: notes.AnchorSpec{Paths: []string{"x/a.go"}}}); err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	makeNote(t, c, "dir note", notes.AnchorSpec{Dirs: []string{"x"}})
	makeNote(t, c, "branch note", notes.AnchorSpec{Branches: []string{"main"}})

	scored := mustRelevant(t, c, dir, "x/a.go", notes.RelevantFilter{})
	if len(scored) != 5 {
		t.Fatalf("results = %d, want 5: %v", len(scored), scoredIDs(scored))
	}
	for i := 0; i+1 < len(scored); i++ {
		a, b := scored[i], scored[i+1]
		if a.Score != b.Score {
			if a.Score < b.Score {
				t.Fatalf("score order violated at %d: %d before %d", i, a.Score, b.Score)
			}
			continue
		}
		if ua, ub := entryUpdatedAt(a), entryUpdatedAt(b); ua != ub {
			if ua < ub {
				t.Fatalf("updatedAt order violated at %d: %d before %d (equal score)", i, ua, ub)
			}
			continue
		}
		if entryID(a) >= entryID(b) {
			t.Fatalf("id order violated at %d: %s before %s (equal score+updatedAt)", i, entryID(a), entryID(b))
		}
	}
}
